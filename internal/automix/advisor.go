package automix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultAdvisorTimeout = 3000 * time.Millisecond
	maxAdvisorResponse    = 8 << 10
)

type AdvisorConfig struct {
	Endpoint        string
	Protocol        string
	Model           string
	APIKey          string
	Timeout         time.Duration
	Client          *http.Client
	MaxTokens       int
	TokenLimitField string
	Temperature     *float64
	Effort          string
}

type Advisor struct {
	config AdvisorConfig
}

type Planner interface {
	Plan(context.Context, Request) (Decision, error)
}

func NewAdvisor(config AdvisorConfig) (*Advisor, error) {
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	if err := validateEndpoint(config.Endpoint); err != nil {
		return nil, err
	}
	if config.Protocol == "" {
		config.Protocol = ProtocolChat
	} else {
		base, err := BaseEndpoint(config.Endpoint)
		if err != nil {
			return nil, err
		}
		config.Endpoint = base + map[string]string{
			ProtocolChat: "/chat/completions", ProtocolResponses: "/responses",
			ProtocolMessages: "/messages", ProtocolGemini: "",
		}[config.Protocol]
	}
	if err := validateParameters(config); err != nil {
		return nil, err
	}
	if config.Timeout <= 0 || config.Timeout > 5*time.Second {
		config.Timeout = defaultAdvisorTimeout
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: config.Timeout}
	}
	client := *config.Client
	// 即使是同域重定向，也不能把用户配置的密钥转交给另一个接口。
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.Client = &client
	if config.Temperature != nil {
		value := *config.Temperature
		config.Temperature = &value
	}
	return &Advisor{config: config}, nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Plan 仅发送特征 JSON；模型不能看到 PCM、歌曲身份或播放器凭据。
func (advisor *Advisor) Plan(ctx context.Context, request Request) (Decision, error) {
	if strings.TrimSpace(advisor.config.Model) == "" {
		return Decision{}, errors.New("automix: model is required")
	}
	if len(request.Candidates) == 0 {
		return Decision{}, errors.New("automix: no candidate transition")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return Decision{}, err
	}
	if len(payload) > MaxRequestBytes {
		return Decision{}, errors.New("automix: feature request is too large")
	}
	body, endpoint, err := advisor.encode(string(payload))
	if err != nil {
		return Decision{}, err
	}
	timeout := advisor.config.Timeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = max(time.Until(deadline), 1*time.Millisecond)
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	advisor.authorize(httpRequest)
	response, err := advisor.config.Client.Do(httpRequest)
	if err != nil {
		return Decision{}, errors.New("automix: connection failed or timed out")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Decision{}, &HTTPError{Status: response.StatusCode}
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxAdvisorResponse+1))
	if err != nil {
		return Decision{}, err
	}
	if len(responseBody) > maxAdvisorResponse {
		return Decision{}, errors.New("automix: advisor response is too large")
	}
	text, err := advisor.decode(responseBody)
	if err != nil {
		return Decision{}, err
	}
	decision, err := ParseDecision([]byte(text), request)
	if err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("automix: advisor endpoint must be an absolute URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("automix: advisor endpoint cannot contain credentials")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLocalHost(parsed.Hostname())) {
		return errors.New("automix: advisor endpoint must use HTTPS")
	}
	return nil
}

func ChatEndpoint(base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if err := validateEndpoint(base); err != nil {
		return "", err
	}
	if !strings.HasSuffix(base, "/chat/completions") {
		base += "/chat/completions"
	}
	return base, nil
}

func isLocalHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

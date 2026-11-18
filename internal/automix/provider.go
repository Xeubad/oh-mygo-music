package automix

import (
	"errors"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	ProtocolChat      = "chat"
	ProtocolResponses = "responses"
	ProtocolMessages  = "messages"
	ProtocolGemini    = "gemini"
)

// Provider 仅存非敏感选项；凭据由应用按配置身份和地址放入安全存储。
type Provider struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Preset          string   `json:"preset,omitempty"`
	Protocol        string   `json:"protocol"`
	Endpoint        string   `json:"endpoint"`
	Model           string   `json:"model,omitempty"`
	TimeoutMs       int      `json:"timeoutMs,omitempty"`
	MaxTokens       int      `json:"maxTokens,omitempty"`
	TokenLimitField string   `json:"tokenLimitField,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	Effort          string   `json:"effort,omitempty"`
}

type Preset struct {
	ID, Name, Endpoint, Protocol string
}

// 模型从服务端获取或手填，避免随发行包固定过时的模型名称。
var Presets = []Preset{
	{"custom", "Custom provider", "", ProtocolChat},
	{"magpie", "Magpie", "http://127.0.0.1:3425/v1", ProtocolChat},
	{"openai", "OpenAI", "https://api.openai.com/v1", ProtocolResponses},
	{"deepseek", "DeepSeek", "https://api.deepseek.com", ProtocolChat},
	{"kimi", "Kimi", "https://api.moonshot.cn/v1", ProtocolChat},
	{"glm", "GLM", "https://open.bigmodel.cn/api/paas/v4", ProtocolChat},
	{"qwen", "Qwen", "https://dashscope.aliyuncs.com/compatible-mode/v1", ProtocolChat},
	{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", ProtocolChat},
	{"anthropic", "Anthropic", "https://api.anthropic.com/v1", ProtocolMessages},
	{"gemini", "Google Gemini", "https://generativelanguage.googleapis.com/v1beta", ProtocolGemini},
	{"ollama", "Ollama", "http://127.0.0.1:11434/v1", ProtocolChat},
}

func BaseEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/models"} {
		if strings.HasSuffix(endpoint, suffix) {
			endpoint = strings.TrimSuffix(endpoint, suffix)
			break
		}
	}
	if strings.HasSuffix(endpoint, ":generateContent") {
		if marker := strings.LastIndex(endpoint, "/models/"); marker >= 0 {
			endpoint = endpoint[:marker]
		}
	}
	parsed, _ := url.Parse(endpoint)
	parsed.Scheme, parsed.Host = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (provider Provider) Config() AdvisorConfig {
	return AdvisorConfig{
		Endpoint: provider.Endpoint, Protocol: provider.Protocol, Model: strings.TrimSpace(provider.Model),
		Timeout: time.Duration(provider.TimeoutMs) * time.Millisecond, MaxTokens: provider.MaxTokens,
		Temperature: provider.Temperature, Effort: provider.Effort, TokenLimitField: provider.TokenLimitField,
	}
}

func (provider Provider) Validate() error {
	if strings.TrimSpace(provider.Name) == "" || len(provider.Name) > 100 || len(provider.Model) > 256 {
		return errors.New("invalid provider name or model")
	}
	config := provider.Config()
	if _, err := NewAdvisor(config); err != nil {
		return err
	}
	if provider.TimeoutMs < 100 || provider.TimeoutMs > 5000 {
		return errors.New("timeout must be between 100 and 5000 ms")
	}
	return nil
}

func validateParameters(config AdvisorConfig) error {
	switch config.Protocol {
	case ProtocolChat, ProtocolResponses, ProtocolMessages, ProtocolGemini:
	default:
		return errors.New("unsupported API protocol")
	}
	if config.MaxTokens < 0 || config.MaxTokens > 8192 {
		return errors.New("output limit must be between 0 and 8192 tokens")
	}
	if config.TokenLimitField != "" && config.TokenLimitField != "max_tokens" && config.TokenLimitField != "max_completion_tokens" {
		return errors.New("invalid token limit field")
	}
	if value := config.Temperature; value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	switch config.Effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return errors.New("invalid reasoning effort")
	}
	if config.Effort != "" && config.Protocol != ProtocolChat && config.Protocol != ProtocolResponses {
		return errors.New("reasoning effort is available for Chat and Responses")
	}
	return nil
}

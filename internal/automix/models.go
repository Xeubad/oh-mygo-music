package automix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Model struct {
	ID   string
	Name string
}

// Models 只列出服务提供的标识；不靠模型名字猜测其能力，也不发生成请求。
func (advisor *Advisor) Models(parent context.Context) ([]Model, error) {
	base, err := BaseEndpoint(advisor.config.Endpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	endpoint := base + "/models"
	seen := map[string]bool{}
	var models []Model
	for page := 0; page < 10; page++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		advisor.authorize(request)
		response, err := advisor.config.Client.Do(request)
		if err != nil {
			return nil, errors.New("model list connection failed or timed out")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, &HTTPError{Status: response.StatusCode}
		}
		if readErr != nil || len(body) > 1<<20 {
			return nil, errors.New("model list is unreadable or too large")
		}
		type item struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			GeminiName  string `json:"displayName"`
		}
		var envelope struct {
			Data     []item `json:"data"`
			Models   []item `json:"models"`
			HasMore  bool   `json:"has_more"`
			LastID   string `json:"last_id"`
			NextPage string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, errors.New("invalid model list")
		}
		items := envelope.Data
		if advisor.config.Protocol == ProtocolGemini {
			items = envelope.Models
		}
		for _, value := range items {
			id, name := value.ID, value.DisplayName
			if advisor.config.Protocol == ProtocolGemini {
				id, name = strings.TrimPrefix(value.Name, "models/"), value.GeminiName
			}
			if name == "" {
				name = id
			}
			if id == "" || seen[id] || len(id) > 256 || len(models) >= 5000 {
				continue
			}
			seen[id] = true
			models = append(models, Model{ID: id, Name: name})
		}
		if !envelope.HasMore && envelope.NextPage == "" {
			sort.Slice(models, func(first, second int) bool { return models[first].ID < models[second].ID })
			return models, nil
		}
		query := url.Values{}
		if envelope.NextPage != "" {
			query.Set("pageToken", envelope.NextPage)
		} else if envelope.LastID != "" {
			query.Set("after_id", envelope.LastID)
		} else {
			return nil, errors.New("model list has incomplete pagination")
		}
		endpoint = base + "/models?" + query.Encode()
	}
	return nil, errors.New("model list has too many pages")
}

type HTTPError struct{ Status int }

func (failure *HTTPError) Error() string { return http.StatusText(failure.Status) }

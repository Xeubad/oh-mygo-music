// Package ai talks to OpenAI-compatible chat completion services for the
// playlist suggestions.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config is the listener's AI service: any endpoint that answers the OpenAI
// chat completions protocol.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	// WebSearch asks the service for its own web search, when it has one.
	WebSearch     bool
	WebSearchTool string
}

// Message is one turn of a completion.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client asks a service for completions.
type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// Complete asks for the reply to messages and returns its text.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	text, err := c.complete(ctx, messages, c.cfg.WebSearch)
	if err != nil && c.cfg.WebSearch {
		return c.complete(ctx, messages, false)
	}
	return text, err
}

func (c *Client) complete(ctx context.Context, messages []Message, web bool) (string, error) {
	if c.cfg.BaseURL == "" || c.cfg.Model == "" {
		return "", fmt.Errorf("ai: base URL and model are required")
	}
	request := map[string]any{
		"model":       c.cfg.Model,
		"messages":    messages,
		"temperature": 0.2,
	}
	if web {
		tool := c.cfg.WebSearchTool
		if tool == "" {
			tool = "web_search"
		}
		request["tools"] = []map[string]string{{"type": tool}}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("ai: encode request: %w", err)
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ai: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("ai: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ai: %s: %s", resp.Status, brief(data))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("ai: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("ai: response has no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// brief keeps an error body short enough for a settings message.
func brief(data []byte) string {
	text := strings.TrimSpace(string(data))
	if r := []rune(text); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return text
}

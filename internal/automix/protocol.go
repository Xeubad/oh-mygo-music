package automix

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const decisionInstruction = `Return exactly one JSON object with string fields "candidate_id" and "profile". Choose only an offered candidate and profile. Do not return frame positions, explanations, markdown, or extra fields.`

func (advisor *Advisor) authorize(request *http.Request) {
	if advisor.config.Protocol == ProtocolMessages {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	if advisor.config.APIKey == "" {
		return
	}
	switch advisor.config.Protocol {
	case ProtocolMessages:
		request.Header.Set("x-api-key", advisor.config.APIKey)
	case ProtocolGemini:
		request.Header.Set("x-goog-api-key", advisor.config.APIKey)
	default:
		request.Header.Set("Authorization", "Bearer "+advisor.config.APIKey)
	}
}

func (advisor *Advisor) encode(features string) ([]byte, string, error) {
	config := advisor.config
	endpoint := config.Endpoint
	messages := []chatMessage{{Role: "system", Content: decisionInstruction}, {Role: "user", Content: features}}
	payload := map[string]any{"model": config.Model}
	switch config.Protocol {
	case ProtocolResponses:
		payload["input"] = messages
		payload["store"] = false
		if config.MaxTokens > 0 {
			payload["max_output_tokens"] = config.MaxTokens
		}
		if config.Effort != "" {
			payload["reasoning"] = map[string]string{"effort": config.Effort}
		}
	case ProtocolMessages:
		payload["system"] = decisionInstruction
		payload["messages"] = messages[1:]
		// Messages 要求显式输出上限；短候选决策无需大段自由生成。
		payload["max_tokens"] = 512
		if config.MaxTokens > 0 {
			payload["max_tokens"] = config.MaxTokens
		}
	case ProtocolGemini:
		model := strings.TrimPrefix(config.Model, "models/")
		endpoint += "/models/" + url.PathEscape(model) + ":generateContent"
		payload = map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]string{{"text": decisionInstruction}}},
			"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": features}}}},
		}
		options := map[string]any{}
		if config.MaxTokens > 0 {
			options["maxOutputTokens"] = config.MaxTokens
		}
		if config.Temperature != nil {
			options["temperature"] = *config.Temperature
		}
		if len(options) > 0 {
			payload["generationConfig"] = options
		}
	default:
		payload["messages"] = messages
		if config.MaxTokens > 0 {
			field := config.TokenLimitField
			if field == "" {
				field = "max_tokens"
			}
			payload[field] = config.MaxTokens
		}
		if config.Effort != "" {
			payload["reasoning_effort"] = config.Effort
		}
	}
	if config.Temperature != nil && config.Protocol != ProtocolGemini {
		payload["temperature"] = *config.Temperature
	}
	body, err := json.Marshal(payload)
	return body, endpoint, err
}

func (advisor *Advisor) decode(body []byte) (string, error) {
	var envelope struct {
		Status  string `json:"status"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Output []struct {
			Type    string                        `json:"type"`
			Content []struct{ Type, Text string } `json:"content"`
		} `json:"output"`
		StopReason string                        `json:"stop_reason"`
		Content    []struct{ Type, Text string } `json:"content"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string
					Thought bool
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", errors.New("automix: invalid advisor envelope")
	}
	var text strings.Builder
	switch advisor.config.Protocol {
	case ProtocolResponses:
		if envelope.Status != "completed" {
			return "", errors.New("automix: incomplete advisor decision")
		}
		for _, output := range envelope.Output {
			if output.Type != "message" {
				continue
			}
			for _, part := range output.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		}
	case ProtocolMessages:
		if envelope.StopReason != "end_turn" {
			return "", errors.New("automix: incomplete advisor decision")
		}
		for _, part := range envelope.Content {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
	case ProtocolGemini:
		if len(envelope.Candidates) == 0 || envelope.Candidates[0].FinishReason != "STOP" {
			return "", errors.New("automix: incomplete advisor decision")
		}
		for _, part := range envelope.Candidates[0].Content.Parts {
			if !part.Thought {
				text.WriteString(part.Text)
			}
		}
	default:
		if len(envelope.Choices) == 0 {
			return "", errors.New("automix: advisor returned no choices")
		}
		first := envelope.Choices[0]
		if first.FinishReason != "" && first.FinishReason != "stop" {
			return "", errors.New("automix: incomplete advisor decision")
		}
		text.WriteString(first.Message.Content)
	}
	if text.Len() == 0 {
		return "", errors.New("automix: advisor returned no decision text")
	}
	return text.String(), nil
}

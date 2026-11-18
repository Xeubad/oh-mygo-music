package automix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Responses 配置必须调用所选协议并携带可选参数，模型只能返回合法候选。
func TestAdvisorResponsesUsesConfiguredProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("协议路径或鉴权错误")
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["max_output_tokens"] != float64(512) || payload["messages"] != nil || payload["input"] == nil {
			t.Errorf("Responses 参数错误: %v", payload)
		}
		_, _ = response.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"candidate_id\":\"mix-1\",\"profile\":\"original\"}"}]}]}`))
	}))
	defer server.Close()
	advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL + "/v1", Protocol: ProtocolResponses, Model: "model", APIKey: "synthetic", MaxTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Candidates: []Candidate{{ID: "mix-1", Profiles: []string{"original"}}}}
	if _, err := advisor.Plan(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

// 各原生协议使用自己的鉴权和消息字段，思考片段不能被当作最终决策。
func TestAdvisorNativeProtocolRequestsAndDecisionText(t *testing.T) {
	for _, protocol := range []string{ProtocolMessages, ProtocolGemini} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if request.URL.RawQuery != "" || request.Header.Get("Authorization") != "" {
					t.Error("原生鉴权不应使用查询参数或 Bearer")
				}
				switch protocol {
				case ProtocolMessages:
					if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "test-key" || request.Header.Get("anthropic-version") != "2023-06-01" || payload["max_tokens"] != float64(512) || payload["system"] == nil {
						t.Errorf("Messages 请求错误: %s %v", request.URL.Path, payload)
					}
					_, _ = response.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":"ignore"},{"type":"text","text":"{\"candidate_id\":\"mix-1\",\"profile\":\"original\"}"}]}`))
				case ProtocolGemini:
					if request.URL.Path != "/v1/models/gemini-test:generateContent" || request.Header.Get("x-goog-api-key") != "test-key" || payload["contents"] == nil || payload["systemInstruction"] == nil || payload["model"] != nil {
						t.Errorf("Gemini 请求错误: %s %v", request.URL.Path, payload)
					}
					_, _ = response.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ignore","thought":true},{"text":"{\"candidate_id\":\"mix-1\",\"profile\":\"original\"}"}]}}]}`))
				}
			}))
			defer server.Close()
			advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL + "/v1", Protocol: protocol, Model: "gemini-test", APIKey: "test-key"})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := advisor.Plan(context.Background(), Request{Candidates: []Candidate{{ID: "mix-1", Profiles: []string{"original"}}}})
			if err != nil || decision.CandidateID != "mix-1" {
				t.Fatalf("原生协议没有得到有效决策: %+v %v", decision, err)
			}
		})
	}
}

// HTTP 失败只暴露状态码，服务返回的原文可能回显密钥，不能显示给用户或进入日志。
func TestAdvisorHTTPFailureDoesNotExposeProviderBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte("echoed-key-do-not-show"))
	}))
	defer server.Close()
	advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = advisor.Plan(context.Background(), Request{Candidates: []Candidate{{ID: "mix-1", Profiles: []string{"original"}}}})
	if err == nil || strings.Contains(err.Error(), "echoed-key") {
		t.Fatalf("服务错误泄露原文或被吞掉: %v", err)
	}
}

// Chat 的可选参数未配置时不发送，限制字段可适应不同兼容服务。
func TestAdvisorChatOptionalParametersRespectCompatibility(t *testing.T) {
	for _, field := range []string{"", "max_tokens", "max_completion_tokens"} {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["temperature"] != nil || payload["reasoning_effort"] != nil {
				t.Error("发送了未选用的参数")
			}
			expected := field
			if expected == "" {
				expected = "max_tokens"
			}
			if payload[expected] != float64(256) {
				t.Errorf("限制字段错误: %v", payload)
			}
			_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"{\"candidate_id\":\"mix-1\",\"profile\":\"original\"}"}}]}`))
		}))
		advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL, Model: "test", MaxTokens: 256, TokenLimitField: field})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := advisor.Plan(context.Background(), Request{Candidates: []Candidate{{ID: "mix-1", Profiles: []string{"original"}}}}); err != nil {
			t.Fatal(err)
		}
		server.Close()
	}
}

// 完整 Gemini 生成地址也能还原基础路径，获取模型时不能把生成后缀误当目录。
func TestProviderNormalizesCompleteEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"https://models.example/v1/chat/completions",
		"https://models.example/v1/responses/",
		"https://models.example/v1/messages",
		"https://models.example/v1/models/gemini-test:generateContent",
	} {
		base, err := BaseEndpoint(endpoint)
		if err != nil || base != "https://models.example/v1" {
			t.Fatalf("基础地址解析错误: %s -> %s %v", endpoint, base, err)
		}
	}
}

// 获取模型列表只做 GET，保留手填模型的可能，过滤重复标识并兼容网关显示名。
func TestAdvisorListsModelsWithoutGeneration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			t.Errorf("获取模型意外执行了生成: %s %s", request.Method, request.URL.Path)
		}
		_, _ = response.Write([]byte(`{"data":[{"id":"b","display_name":"Model B"},{"id":"a"},{"id":"b"},{"id":""}]}`))
	}))
	defer server.Close()
	advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL + "/v1", Protocol: ProtocolChat, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	models, err := advisor.Models(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "a" || models[1].Name != "Model B" {
		t.Fatalf("模型列表错误: %+v %v", models, err)
	}
}

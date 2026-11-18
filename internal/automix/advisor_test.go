package automix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 顾问请求只含特征 JSON，模型响应必须经过本地候选校验。
func TestAdvisorSendsFeatureJSONAndValidatesDecision(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 120, .1))
	second.Feed(rhythmFixture(16, 124, .15))
	featureRequest := BuildRequest(first.Snapshot(), second.Snapshot(), 16*SampleRate, 0)
	if len(featureRequest.Candidates) == 0 {
		t.Fatal("测试没有生成候选")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("没有发送配置的 API key")
			return
		}
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if payload.Model != "test-model" || len(payload.Messages) != 2 || payload.Messages[1].Role != "user" {
			t.Error("请求结构错误")
			return
		}
		if strings.Contains(payload.Messages[1].Content, "pcm") || strings.Contains(payload.Messages[1].Content, "audio") {
			t.Error("特征请求携带了原始音频字段")
			return
		}
		var sent Request
		if err := json.Unmarshal([]byte(payload.Messages[1].Content), &sent); err != nil {
			t.Error(err)
			return
		}
		if sent.Schema != featureRequest.Schema || len(sent.Candidates) != len(featureRequest.Candidates) {
			t.Error("特征 JSON 没有完整发送")
			return
		}
		candidate := sent.Candidates[0]
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"` +
			`{\"candidate_id\":\"` + candidate.ID + `\",\"profile\":\"` + candidate.Profiles[0] + `\"}` +
			`"}}]}`))
	}))
	defer server.Close()
	advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL, Model: "test-model", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := advisor.Plan(context.Background(), featureRequest)
	if err != nil {
		t.Fatal(err)
	}
	if decision.CandidateID != featureRequest.Candidates[0].ID || decision.Profile != featureRequest.Candidates[0].Profiles[0] {
		t.Fatalf("决策没有经过候选协议: %+v", decision)
	}
}

// 超时、坏 envelope 和未知候选必须回退，不能让云服务控制播放器时钟。
func TestAdvisorRejectsTimeoutAndMalformedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		switch request.URL.Path {
		case "/timeout":
			<-request.Context().Done()
			return
		case "/malformed":
			_, _ = response.Write([]byte(`{"choices":[]}`))
			return
		}
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"{\"candidate_id\":\"invented\",\"profile\":\"original\"}"}}]}`))
	}))
	defer server.Close()
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 120, .1))
	second.Feed(rhythmFixture(16, 124, .15))
	featureRequest := BuildRequest(first.Snapshot(), second.Snapshot(), 16*SampleRate, 0)
	for _, scenario := range []struct {
		name    string
		timeout time.Duration
	}{
		{"timeout", 20 * time.Millisecond},
		{"malformed", time.Second},
		{"invented", time.Second},
	} {
		advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL + "/" + scenario.name, Model: "test", Timeout: scenario.timeout})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := advisor.Plan(ctx, featureRequest); err == nil {
			t.Fatalf("%s 场景没有安全失败", scenario.name)
		}
	}
}

func TestAdvisorEndpointRequiresHTTPSOrLocalhost(t *testing.T) {
	for _, endpoint := range []string{
		"http://api.example.com/v1/chat/completions",
		"https://user:pass@example.com/v1/chat/completions",
		"not-a-url",
		"ftp://localhost/api",
		"https://example.com/v1?key=synthetic",
	} {
		if _, err := NewAdvisor(AdvisorConfig{Endpoint: endpoint, Model: "test"}); err == nil {
			t.Fatalf("接受了不安全 endpoint: %s", endpoint)
		}
	}
}

// 兼容服务重定向时不能泄露密钥或重复发送歌曲特征。
func TestAdvisorDoesNotFollowRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			t.Error("重定向接口收到了请求")
			return
		}
		http.Redirect(response, request, "/other", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	advisor, err := NewAdvisor(AdvisorConfig{Endpoint: server.URL, Model: "test", APIKey: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = advisor.Plan(context.Background(), Request{Candidates: []Candidate{{ID: "mix-1", Profiles: []string{"original"}}}})
	if err == nil {
		t.Fatal("重定向未被拒绝")
	}
}

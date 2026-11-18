package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mygo-music/internal/automix"
	"mygo-music/internal/i18n"
)

// 旧接口只迁移一次且沿用原凭据位置，不开启 AI 或覆盖已经保存的多供应商配置。
func TestAISettingsMigrateLegacyWithoutEnablingAI(t *testing.T) {
	preferences := defaultSettings()
	preferences.AutoMixEndpoint, preferences.AutoMixModel = "https://legacy.example/v1", "legacy-model"
	legacyKey := autoMixCredentialKey(preferences.AutoMixEndpoint)
	preferences.migrateAIProviders()
	if len(preferences.AIProviders) != 1 || preferences.AutoMixAIEnabled || preferences.AIProviders[0].Model != "legacy-model" {
		t.Fatal("旧配置迁移丢失数据或开启 AI")
	}
	if aiCredentialKey(preferences.AIProviders[0]) != legacyKey {
		t.Fatal("迁移后不能找到原安全凭据")
	}
	preferences.migrateAIProviders()
	if len(preferences.AIProviders) != 1 {
		t.Fatal("重复迁移产生了重复供应商")
	}
}

// 未保存密钥的本机网关允许空鉴权获取模型，切换编辑对象后旧结果不能进入新草稿。
func TestAIProviderProbeDiscardsCanceledModelList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			t.Errorf("模型列表请求错误: %s %s", request.Method, request.URL.Path)
		}
		_, _ = response.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer server.Close()
	old := getAutoMixSecret
	getAutoMixSecret = func(string) ([]byte, error) { return nil, mygo.ErrSecretNotFound }
	t.Cleanup(func() { getAutoMixSecret = old })
	a := newTestApp()
	a.beginAIProvider("")
	a.ai.draft.Endpoint = server.URL + "/v1"
	posted := posts(a)
	a.probeAIProvider("models")
	runPost(t, posted)
	if len(a.ai.models) != 1 || a.ai.models[0].ID != "model-a" || a.ai.job != "" {
		t.Fatal("未保存密钥的网关无法获取模型")
	}
	a.probeAIProvider("models")
	a.beginAIProvider("")
	runPost(t, posted)
	if len(a.ai.models) != 0 || a.ai.job != "" {
		t.Fatal("已取消请求修改了新供应商")
	}
}

// 连接测试真正核验配置的模型决策格式，只发送模拟候选，不开启或改变播放。
func TestAIProviderConnectionProbeUsesSyntheticCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("连接测试没有使用草稿中的接口与密钥")
		}
		var payload struct {
			Model    string                     `json:"model"`
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		var feature automix.Request
		if len(payload.Messages) != 2 || payload.Model != "test-model" {
			t.Error("测试没有使用选择的模型")
			return
		}
		if err := json.Unmarshal([]byte(payload.Messages[1].Content), &feature); err != nil || len(feature.Current.Envelope) != 0 || len(feature.Next.Envelope) != 0 || len(feature.Candidates) != 1 || feature.Candidates[0].ID != "connection-test" {
			t.Error("测试请求携带真实音乐或未使用模拟候选")
		}
		_, _ = response.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"candidate_id\":\"connection-test\",\"profile\":\"original\"}"}}]}`))
	}))
	defer server.Close()
	a := newTestApp()
	a.beginAIProvider("")
	a.ai.draft.Endpoint, a.ai.draft.Model, a.ai.keyDraft = server.URL+"/v1", "test-model", "test-key"
	posted := posts(a)
	a.probeAIProvider("test")
	runPost(t, posted)
	if a.ai.job != "" || !strings.Contains(a.ai.status, "Connection passed") || a.settings.AutoMixAIEnabled || a.pl.Playing() {
		t.Fatalf("测试结果或播放状态错误: %s", a.ai.status)
	}
}

// 系统凭据库异步读取旧配置时切换供应商，旧结果不能替换新供应商的运行中模型。
func TestAIProviderSwitchRejectsOldCredentialRead(t *testing.T) {
	a := newTestApp()
	first := automix.Provider{ID: "first", Name: "First", Endpoint: "https://first.example/v1", Protocol: automix.ProtocolChat, Model: "one", TimeoutMs: 1200}
	second := first
	second.ID, second.Name, second.Model, second.Endpoint = "second", "Second", "two", "https://second.example/v1"
	a.settings.AIProviders = []automix.Provider{first, second}
	a.settings.AutoMixEnabled, a.settings.AutoMixAIEnabled = true, true
	a.settings.AutoMixProviderID = first.ID
	called, release := make(chan struct{}), make(chan struct{})
	previous := getAutoMixSecret
	getAutoMixSecret = func(key string) ([]byte, error) {
		if key == aiCredentialKey(first) {
			close(called)
			<-release
		}
		return []byte("test-key"), nil
	}
	t.Cleanup(func() { getAutoMixSecret = previous })
	posted := posts(a)
	a.configureAutoMixAdvisor()
	<-called
	a.selectAIProvider(second.ID)
	runPost(t, posted)
	active := a.autoMix.advisor
	if active == nil {
		t.Fatal("新供应商没有建立配置")
	}
	close(release)
	runPost(t, posted)
	if a.autoMix.advisor != active {
		t.Fatal("旧凭据读取覆盖了新供应商")
	}
}

// 供应商列表中的删除操作需要二次确认，删除当前配置后仍保持本机 AutoMix。
func TestAIProviderDeleteRequiresConfirmationAndKeepsLocalPlayback(t *testing.T) {
	a := newTestApp()
	a.settingsPage, a.settings.path = "ai", filepath.Join(t.TempDir(), "settings.json")
	a.settings.AutoMixEnabled = true
	provider := automix.Provider{ID: "one", Name: "One", Protocol: automix.ProtocolChat, Endpoint: "https://relay.example/v1", Model: "test", TimeoutMs: 1200}
	a.settings.AIProviders, a.settings.AutoMixProviderID = []automix.Provider{provider}, provider.ID
	a.ai.draft, a.ai.editing = provider, true
	old := deleteAutoMixSecret
	deleted := ""
	deleteAutoMixSecret = func(key string) error { deleted = key; return nil }
	t.Cleanup(func() { deleteAutoMixSecret = old })
	posted := posts(a)
	tester := ui.NewTester(a.settingsView, 1000, 1600)
	settled(tester)
	button, ok := tester.AccessByRole(ui.RoleButton, "Delete provider")
	if !ok {
		t.Fatal("没有删除操作入口")
	}
	tester.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
	tester.Frame()
	if len(a.settings.AIProviders) != 1 || !a.ai.confirmDelete || deleted != "" {
		t.Fatal("未确认就删除了供应商")
	}
	button, ok = tester.AccessByRole(ui.RoleButton, "Confirm delete")
	if !ok {
		t.Fatal("没有二次确认入口")
	}
	tester.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
	tester.Frame()
	runPost(t, posted)
	if len(a.settings.AIProviders) != 0 || a.settings.AutoMixProviderID != "" || !a.settings.AutoMixEnabled || deleted != aiCredentialKey(provider) {
		t.Fatal("删除配置影响了本机播放或未清理凭据")
	}
}

// AI 页面在内存中导出供应商列表和宽窄编辑帧图，不读取任何真实凭据。
func TestAISettingsFrames(t *testing.T) {
	directory := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	for _, scenario := range []struct {
		name                    string
		width, height           int
		editing, advanced, dark bool
	}{
		{"ai-providers.png", 1040, 880, false, false, false},
		{"ai-provider-editor.png", 1040, 1360, true, true, false},
		{"ai-provider-compact.png", 720, 920, true, false, false},
		{"ai-provider-dark.png", 1040, 1360, true, true, true},
	} {
		a := newTestApp()
		a.settingsPage = "ai"
		provider := automix.Provider{ID: "relay", Name: "Magpie", Preset: "magpie", Protocol: automix.ProtocolChat, Endpoint: "http://127.0.0.1:3425/v1", Model: "group/music-fast", TimeoutMs: 1200}
		a.settings.AIProviders = []automix.Provider{provider, {ID: "ds", Name: "DeepSeek", Preset: "deepseek", Protocol: automix.ProtocolChat, Endpoint: "https://api.deepseek.com", Model: "my-model", TimeoutMs: 1200}}
		a.settings.AutoMixProviderID = "relay"
		a.ai.draft, a.ai.editing, a.ai.advanced = provider, scenario.editing, scenario.advanced
		a.ai.keyStatus = T("Key saved securely")
		a.ai.models = []automix.Model{{ID: "group/music-fast", Name: "Fast"}, {ID: "group/music-quality", Name: "Quality"}}
		tester := ui.NewTester(a.settingsView, scenario.width, scenario.height)
		if scenario.dark {
			tester.SetDark(true)
		}
		settled(tester)
		saveFrame(t, directory, scenario.name, tester.Image())
	}
}

// 两个供应商即使共用代理地址，凭据也必须隔离；改地址不能沿用旧站点密钥。
func TestAIProviderCredentialIdentityIncludesProviderAndEndpoint(t *testing.T) {
	first := automix.Provider{ID: "first", Endpoint: "https://relay.example/v1"}
	second := first
	second.ID = "second"
	if aiCredentialKey(first) == aiCredentialKey(second) {
		t.Fatal("同地址的不同配置共享了密钥")
	}
	second = first
	second.Endpoint = "https://other.example/v1"
	if aiCredentialKey(first) == aiCredentialKey(second) {
		t.Fatal("更换服务地址后仍会发送旧密钥")
	}
}

// 保存供应商将凭据放入安全存储，普通配置只保留非敏感参数，模型未获取也可手填。
func TestAIProviderSavePersistsConfigWithoutCredential(t *testing.T) {
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.beginAIProvider("")
	a.ai.draft.Name, a.ai.draft.Endpoint, a.ai.draft.Model = "My relay", "https://relay.example/v1", "my-model"
	a.ai.keyDraft = "synthetic-provider-secret"
	previous := setAutoMixSecret
	var saved string
	setAutoMixSecret = func(_ string, value []byte) error { saved = string(value); return nil }
	t.Cleanup(func() { setAutoMixSecret = previous })
	posted := posts(a)
	a.saveAIProvider()
	runPost(t, posted)
	if saved != "synthetic-provider-secret" || len(a.settings.AIProviders) != 1 || a.ai.keyDraft != "" {
		t.Fatal("供应商未保存或密钥草稿未清理")
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	var restored settings
	if err := json.Unmarshal(data, &restored); err != nil || len(restored.AIProviders) != 1 || restored.AIProviders[0].Model != "my-model" {
		t.Fatal("供应商配置无法跨重启恢复")
	}
	if strings.Contains(string(data), saved) {
		t.Fatal("凭据泄露到了普通配置")
	}
}

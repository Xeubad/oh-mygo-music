package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mygo-music/internal/i18n"

	"github.com/egoist/mygo/ui"
)

// 合成来源只实现公开控制契约，不读取凭据、不访问网络。
type customSourcesUIFixture struct {
	*fakeSource
	mu              sync.Mutex
	items           []map[string]any
	active          bool
	legacy          bool
	credentialState string
	updating        bool
	statusReads     int
}

func (s *customSourcesUIFixture) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case "setResolverKey":
		if !s.legacy || input["value"] != "synthetic-legacy-key" || input["temporary"] != true {
			return nil, fmt.Errorf("unexpected legacy credential command")
		}
		s.credentialState = "temporary"
		return json.RawMessage(`{"ok":true}`), nil
	case "status":
		s.statusReads++
		if s.legacy {
			return json.Marshal(map[string]any{"settings": map[string]any{"targetQuality": "flac"}, "resolverCredential": map[string]any{"state": s.credentialState}})
		}
		state := "ready"
		if s.updating {
			state = "updating"
		}
		return json.Marshal(map[string]any{
			"settings":      map[string]any{"targetQuality": "flac", "minimumQuality": "128k", "platformOrder": []string{"wy", "tx", "qs"}, "candidateTimeoutMs": 5000, "roundTimeoutMs": 20000, "stallTimeoutMs": 3000, "recoveryTimeoutMs": 12000, "consecutiveFailureLimit": 3, "refreshIntervalMs": 300000},
			"customSources": map[string]any{"version": 1, "active": s.active, "state": state, "items": s.items},
		})
	case "moveSource":
		if input["id"] != "two" || input["direction"] != "up" {
			return nil, fmt.Errorf("unexpected move")
		}
		s.items[0], s.items[1] = s.items[1], s.items[0]
		return json.RawMessage(`{"ok":true}`), nil
	case "setSourceEnabled":
		for _, item := range s.items {
			if item["id"] == input["id"] {
				item["enabled"] = input["enabled"]
				item["state"] = "disabled"
			}
		}
		return json.RawMessage(`{"ok":true}`), nil
	case "replaceSource":
		if input["id"] != "one" || input["url"] != "https://fixture.invalid/replacement?key=synthetic-only" {
			return nil, fmt.Errorf("unexpected replacement")
		}
		s.items[0]["name"] = "Replacement source"
		return json.RawMessage(`{"ok":true}`), nil
	case "addSource":
		if input["url"] != "https://fixture.invalid/source?key=synthetic-only" {
			return nil, fmt.Errorf("unexpected synthetic link")
		}
		s.items = append(s.items, map[string]any{"id": "one", "name": "Fixture source", "version": "1", "platforms": []string{"wy", "tx", "qs"}, "enabled": true, "state": "ready", "updateState": "idle"})
		s.active = true
		return json.RawMessage(`{"ok":true}`), nil
	default:
		return nil, fmt.Errorf("unexpected action %s", action)
	}
}

func customSourcesSettings(t *testing.T) (*customSourcesUIFixture, *ui.Tester, chan func()) {
	t.Helper()
	a := newTestApp()
	post := posts(a)
	src := &customSourcesUIFixture{fakeSource: fake(a)}
	a.library.src = src
	tt := ui.NewTester(func(c *ui.Context) { a.aggregateSettings(c, a.library, paletteFor(c.Theme())) }, 760, 1200)
	tt.Frame()
	runPost(t, post)
	tt.Frame()
	return src, tt, post
}

// 旧插件缺少 customSources 时仍能经原控件提交临时密钥，不改用音源管理。
func TestAggregateCustomSourcesLegacyCredentialCompatibility(t *testing.T) {
	src, tt, post := customSourcesSettings(t)
	src.mu.Lock()
	src.legacy = true
	src.mu.Unlock()
	if err := tt.Click("Reload aggregate status"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Resolver Key")
	tt.Type("synthetic-legacy-key")
	if err := tt.Click("Use Once"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ ResolverCredential struct{ State string } }
	if err := json.Unmarshal(raw, &got); err != nil || got.ResolverCredential.State != "temporary" {
		t.Fatalf("旧密钥路径失效：%s %v", raw, err)
	}
}

// 菜单操作按稳定音源身份提交，排序与停用结果从来源的公开状态观察。
func TestAggregateCustomSourcesMoveAndDisableFromSettings(t *testing.T) {
	src, tt, post := customSourcesSettings(t)
	src.mu.Lock()
	src.active = true
	src.items = []map[string]any{
		{"id": "one", "name": "First", "platforms": []string{"wy"}, "enabled": true, "state": "ready"},
		{"id": "two", "name": "Second", "platforms": []string{"tx"}, "enabled": true, "state": "ready"},
	}
	src.mu.Unlock()
	if err := tt.Click("Reload aggregate status"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	if err := tt.Click("Source Actions Second"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("Move Source Up"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleSwitch, "Enable Source Second")
	runPost(t, post)
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CustomSources struct {
			Items []struct {
				ID      string
				Enabled bool
			}
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.CustomSources.Items) != 2 || got.CustomSources.Items[0].ID != "two" || got.CustomSources.Items[0].Enabled || got.CustomSources.Items[1].ID != "one" || !got.CustomSources.Items[1].Enabled {
		t.Fatalf("菜单未按目标排序并停用：%s", raw)
	}
}

// 替换复用空白链接输入而非回显旧链接，成功后清空目标和草稿并显示新的来源状态。
func TestAggregateCustomSourcesReplaceFromSettings(t *testing.T) {
	src, tt, post := customSourcesSettings(t)
	if err := tt.Click("Add Source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Source Link")
	tt.Type("https://fixture.invalid/source?key=synthetic-only")
	if err := tt.Click("Confirm Add Source"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	if err := tt.Click("Source Actions Fixture source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("Replace Source Link"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	button, ok := tt.AccessByRole(ui.RoleButton, "Replace Source")
	if !ok || !button.Disabled {
		t.Fatal("替换入口未使用空白草稿")
	}
	clickSettingRole(t, tt, ui.RoleTextField, "Source Link")
	tt.Type("https://fixture.invalid/replacement?key=synthetic-only")
	if err := tt.Click("Replace Source"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Replacement source") {
		t.Fatalf("替换未提交：%s", raw)
	}
	button, ok = tt.AccessByRole(ui.RoleButton, "Add Source")
	if !ok || button.Disabled {
		t.Fatal("替换后没有恢复添加入口")
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Source Link"); ok {
		t.Fatal("替换后敏感输入没有收起")
	}
}

// 启动更新只在需要时重取公开状态，保留用户正在编辑的普通参数和链接草稿。
func TestAggregateCustomSourcesPollKeepsDrafts(t *testing.T) {
	src, tt, post := customSourcesSettings(t)
	src.mu.Lock()
	src.updating = true
	src.mu.Unlock()
	if err := tt.Click("Reload aggregate status"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	if err := tt.Click("Advanced Settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Add Source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Candidate Timeout (ms)")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("4321")
	clickSettingRole(t, tt, ui.RoleTextField, "Source Link")
	tt.Type("https://fixture.invalid/source?key=synthetic-only")
	src.mu.Lock()
	src.updating = false
	before := src.statusReads
	src.mu.Unlock()
	tt.Advance(3 * time.Second)
	tt.Frame()
	runPost(t, post)
	tt.Frame()
	n, ok := tt.AccessByRole(ui.RoleTextField, "Candidate Timeout (ms)")
	if !ok || n.Value != "4321" {
		t.Fatal("自动重取覆盖了普通设置草稿")
	}
	src.mu.Lock()
	reads := src.statusReads
	src.mu.Unlock()
	if reads != before+1 {
		t.Fatal("更新状态未触发单次公开重取")
	}
	tt.Advance(10 * time.Second)
	tt.Frame()
	src.mu.Lock()
	after := src.statusReads
	src.mu.Unlock()
	if after != reads {
		t.Fatal("更新结束后仍轮询")
	}
	// 密码输入的无障碍值刻意为空，通过随后成功提交验证草稿仍在。
	if err := tt.Click("Confirm Add Source"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil || !strings.Contains(string(raw), "Fixture source") {
		t.Fatal("自动重取丢失了链接草稿")
	}
}

// 添加从公开设置提交链接，公开状态新增音源但不泄露链接，成功后输入清空。
func TestAggregateCustomSourcesAddFromSettings(t *testing.T) {
	src, tt, post := customSourcesSettings(t)
	if err := tt.Click("Add Source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Source Link")
	tt.Type("https://fixture.invalid/source?key=synthetic-only")
	if err := tt.Click("Confirm Add Source"); err != nil {
		t.Fatal(err)
	}
	runPost(t, post)
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CustomSources struct {
			Active bool
			Items  []struct{ ID string }
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.CustomSources.Active || len(got.CustomSources.Items) != 1 || got.CustomSources.Items[0].ID != "one" {
		t.Fatalf("设置未提交添加：%s", raw)
	}
	button, ok := tt.AccessByRole(ui.RoleButton, "Add Source")
	if !ok || button.Disabled {
		t.Fatal("添加成功后没有恢复添加入口")
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Source Link"); ok {
		t.Fatal("添加成功后没有清空并收起链接输入")
	}
	if strings.Contains(string(raw), "synthetic-only") || strings.Contains(string(raw), "fixture.invalid") || strings.Contains(strings.Join(tt.Texts(), " "), "synthetic-only") {
		t.Fatal("链接被公开状态或界面回填")
	}
	if dir := os.Getenv("MM_FRAMES"); dir != "" {
		lang := i18n.Language()
		i18n.SetLanguage(i18n.Chinese)
		defer i18n.SetLanguage(lang)
		settled(tt)
		saveFrame(t, dir, "aggregate-custom-sources.png", tt.Image())
	}
}

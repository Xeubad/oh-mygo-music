package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/automix"
	"mygo-music/internal/i18n"
)

// 打开本机 AutoMix 不依赖 AI 配置，重启后仍保留，旧过渡选择不被改义。
func TestAutoMixLocalSwitchPersistsWithoutAI(t *testing.T) {
	a := newTestApp()
	a.settingsPage = "playback"
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tester := ui.NewTester(a.settingsView, 1000, 1200)
	settled(tester)
	control, ok := tester.AccessByRole(ui.RoleSwitch, "Smart transitions (AutoMix)")
	if !ok {
		t.Fatal("本机 AutoMix 没有设置入口")
	}
	tester.ClickAt(control.Bounds.X+control.Bounds.W/2, control.Bounds.Y+control.Bounds.H/2)
	tester.Frame()
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.AutoMixEnabled || restored.AutoMixAIEnabled || !restored.GaplessEnabled || restored.CrossfadeEnabled {
		t.Fatal("本机开关依赖 AI，或改变了已有过渡偏好")
	}
	if !restored.playbackTransitions().AutoMix {
		t.Fatal("本机设置没有送达播放器")
	}
}

// AutoMix 接管衔接时保留且锁定手动偏好，关闭后原偏好可继续编辑。
func TestAutoMixTemporarilyOwnsManualTransitionControls(t *testing.T) {
	a := newTestApp()
	a.settingsPage = "playback"
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.settings.AutoMixEnabled = true
	a.settings.CrossfadeEnabled = true
	a.settings.CrossfadeDurationMs = 7000
	a.settings.GaplessEnabled = false
	tester := ui.NewTester(a.settingsView, 1000, 1600)
	settled(tester)
	click := func(label string) {
		t.Helper()
		control, ok := tester.AccessByRole(ui.RoleSwitch, label)
		if !ok {
			t.Fatalf("找不到开关 %q", label)
		}
		tester.ClickAt(control.Bounds.X+control.Bounds.W/2, control.Bounds.Y+control.Bounds.H/2)
		tester.Frame()
	}
	click("Crossfade")
	click("Gapless playback")
	if !a.settings.CrossfadeEnabled || a.settings.GaplessEnabled || a.settings.CrossfadeDurationMs != 7000 {
		t.Fatal("自动模式中的手动控件修改了保存的偏好")
	}
	click("Smart transitions (AutoMix)")
	click("Gapless playback")
	if a.settings.AutoMixEnabled || !a.settings.CrossfadeEnabled || !a.settings.GaplessEnabled {
		t.Fatal("关闭自动模式后原手动设置无法继续使用")
	}
}

// API key 只能写安全凭据库；普通设置和无障碍字段不能回填已保存的密钥。
func TestAutoMixKeyNeverEntersSettings(t *testing.T) {
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.beginAIProvider("")
	a.ai.draft.Endpoint = "https://model.example.com/v1"
	a.ai.draft.Model = "any-text-model"
	const credential = "automix-test-key"
	a.ai.keyDraft = credential
	previous := setAutoMixSecret
	saved := ""
	setAutoMixSecret = func(key string, value []byte) error {
		saved = string(value)
		return nil
	}
	t.Cleanup(func() { setAutoMixSecret = previous })
	posted := posts(a)
	a.saveAIProvider()
	runPost(t, posted)
	if saved != credential || a.ai.keyDraft != "" {
		t.Fatal("凭据保存或草稿清理失败")
	}
	if err := a.settings.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), saved) {
		t.Fatal("API key 泄露到 settings.json")
	}
}

// 删除旧接口的凭据晚到时，不能清除用户刚切换的新接口配置。
func TestAutoMixOldKeyRemovalKeepsNewEndpoint(t *testing.T) {
	a := newTestApp()
	old := automix.Provider{ID: "old", Name: "Old", Endpoint: "https://old-model.example.com/v1"}
	a.ai.draft = old
	key := aiCredentialKey(old)
	a.ai.temporary = map[string]string{key: "old-session-key"}
	previous := deleteAutoMixSecret
	deleteAutoMixSecret = func(requested string) error {
		if requested != key {
			t.Errorf("删除了其他接口的密钥: %s", requested)
		}
		return nil
	}
	t.Cleanup(func() { deleteAutoMixSecret = previous })
	posted := posts(a)
	a.removeAIKey()
	a.ai.draft = automix.Provider{ID: "new", Endpoint: "https://new-model.example.com/v1"}
	a.ai.epoch++
	a.settings.AIProviders = []automix.Provider{a.ai.draft}
	a.settings.AutoMixProviderID = "new"
	advisor, err := automix.NewAdvisor(automix.AdvisorConfig{
		Endpoint: "https://new-model.example.com/v1/chat/completions",
		Model:    "new-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	a.autoMix.advisor = advisor
	a.autoMix.status = "new endpoint ready"
	epoch := a.autoMix.epoch
	runPost(t, posted)
	if a.autoMix.advisor != advisor || a.autoMix.epoch != epoch || a.autoMix.status != "new endpoint ready" || a.ai.saving {
		t.Fatal("旧接口删除结果污染了新接口配置")
	}
	if a.ai.temporary[key] != "" {
		t.Fatal("已删除接口的临时凭据仍可复用")
	}
}

// 本机与可选云增强设置在内存中导出帧图，验收无需桌面窗口。
func TestAutoMixSettingsFrame(t *testing.T) {
	directory := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	for _, cloud := range []bool{false, true} {
		a := newTestApp()
		a.settingsPage = "playback"
		a.settings.AutoMixEnabled = true
		a.settings.AutoMixAIEnabled = cloud
		a.settings.AutoMixEndpoint = "https://model.example.com/v1"
		a.settings.AutoMixModel = "my-model"
		tester := ui.NewTester(a.settingsView, 1000, 1280)
		settled(tester)
		name := "automix-local.png"
		if cloud {
			name = "automix-ai.png"
		}
		saveFrame(t, directory, name, tester.Image())
	}
}

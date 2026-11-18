package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
)

// 打开本机 AutoMix 后立即保存，旧过渡偏好不被改义。
func TestAutoMixLocalSwitchPersists(t *testing.T) {
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
	if !restored.AutoMixEnabled || !restored.GaplessEnabled || restored.CrossfadeEnabled {
		t.Fatal("本机开关改变了已有过渡偏好")
	}
	if !restored.playbackTransitions().AutoMix {
		t.Fatal("本机设置没有送达播放器")
	}
}

// 移除云端功能后仍能读取旧设置，保存时不再输出失效的服务商配置。
func TestAutoMixIgnoresRetiredCloudSettings(t *testing.T) {
	preferences := defaultSettings()
	if err := json.Unmarshal([]byte(`{"autoMixEnabled":true,"volume":0.35,"gaplessEnabled":false,"autoMixAIEnabled":true,"aiProviders":[{"id":"retired","endpoint":"https://model.example/v1"}],"autoMixProviderId":"retired"}`), &preferences); err != nil {
		t.Fatal(err)
	}
	if !preferences.playbackTransitions().AutoMix || preferences.Volume != .35 || preferences.GaplessEnabled {
		t.Fatal("旧云端设置影响了本机过渡或原有播放选择")
	}
	encoded, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"autoMixAIEnabled", "autoMixEndpoint", "autoMixModel", "aiProviders", "autoMixProviderId"} {
		if _, exists := saved[retired]; exists {
			t.Fatalf("仍在保存已移除的云端配置 %s", retired)
		}
	}
}

// AutoMix 接管衔接时锁定手动偏好，关闭后仍可继续编辑。
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
		t.Fatal("自动模式修改了保存的手动偏好")
	}
	click("Smart transitions (AutoMix)")
	click("Gapless playback")
	if a.settings.AutoMixEnabled || !a.settings.CrossfadeEnabled || !a.settings.GaplessEnabled {
		t.Fatal("关闭自动模式后原手动设置无法继续使用")
	}
}

// 本机智能过渡设置在内存中导出帧图，验收无需桌面窗口。
func TestAutoMixSettingsFrame(t *testing.T) {
	directory := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	a := newTestApp()
	a.settingsPage = "playback"
	a.settings.AutoMixEnabled = true
	tester := ui.NewTester(a.settingsView, 1000, 1000)
	settled(tester)
	saveFrame(t, directory, "automix-local.png", tester.Image())
}

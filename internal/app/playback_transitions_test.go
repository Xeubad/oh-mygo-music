package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
)

// 旧配置补齐过渡默认值后，用户关闭效果再保存读取，关闭意图与特殊时长必须保留。
func TestPlaybackTransitionChoicesSurviveRestart(t *testing.T) {
	preferences := defaultSettings()
	if err := json.Unmarshal([]byte(`{"volume":0.35}`), &preferences); err != nil {
		t.Fatal(err)
	}
	if !preferences.GaplessEnabled || !preferences.PlaybackFadeEnabled {
		t.Fatal("旧配置没有补齐音频过渡")
	}
	preferences.GaplessEnabled = false
	preferences.PlaybackFadeEnabled = false
	encoded, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.GaplessEnabled || restored.PlaybackFadeEnabled || restored.PauseFadeOutMs != 180 || restored.PlayFadeInMs != 260 || restored.Volume != .35 {
		t.Fatalf("重启丢失选择: %+v", restored)
	}
}

// 关闭效果后写入磁盘并重新读取，时长保持不变，界面变更立即送达播放器。
func TestPlaybackTransitionsSaveSwitchChoices(t *testing.T) {
	a := newTestApp()
	a.settingsPage = "playback"
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tester := ui.NewTester(a.settingsView, 900, 1050)
	settled(tester)
	control, ok := tester.AccessByRole(ui.RoleSwitch, "Fade playback and pause")
	if !ok {
		t.Fatal("播放淡变开关不可操作")
	}
	tester.ClickAt(control.Bounds.X+control.Bounds.W/2, control.Bounds.Y+control.Bounds.H/2)
	tester.Frame()
	if a.settings.PlaybackFadeEnabled {
		t.Fatal("关闭操作未生效")
	}
	encoded, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.PlaybackFadeEnabled || restored.PauseFadeOutMs != 180 || restored.PlayFadeInMs != 260 {
		t.Fatalf("保存丢失开关或时长: %+v", restored)
	}
}

// 无法保存时明确反馈，不能将当前内存选择假装成已保存。
func TestPlaybackTransitionsSaveFailureIsVisible(t *testing.T) {
	a := newTestApp()
	a.settingsPage = "playback"
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	a.settings.path = filepath.Join(blocked, "settings.json")
	tester := ui.NewTester(a.settingsView, 900, 1050)
	settled(tester)
	control, ok := tester.AccessByRole(ui.RoleSwitch, "Crossfade")
	if !ok {
		t.Fatal("交叉淡化开关不可操作")
	}
	tester.ClickAt(control.Bounds.X+control.Bounds.W/2, control.Bounds.Y+control.Bounds.H/2)
	tester.Frame()
	if a.toast == "" {
		t.Fatal("保存失败没有反馈")
	}
}

// 用户选择新的淡化时长后立即保存，重新渲染不能把选项还原成原值。
func TestPlaybackTransitionsChooseDuration(t *testing.T) {
	a := newTestApp()
	a.settingsPage = "playback"
	a.settings.CrossfadeEnabled = true
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tester := ui.NewTester(a.settingsView, 1000, 1040)
	settled(tester)
	if err := tester.Click("5.0 s"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("6.0 s"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if a.settings.CrossfadeDurationMs != 6000 {
		t.Fatalf("选择没有保留: %d", a.settings.CrossfadeDurationMs)
	}
	encoded, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CrossfadeDurationMs != 6000 {
		t.Fatal("时长没有自动保存")
	}
}

// 音频过渡的宽屏/紧凑和禁用状态全部在内存渲染，验收时读取图像确认布局。
func TestPlaybackTransitionsFrame(t *testing.T) {
	directory := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	for _, scenario := range []struct {
		name          string
		width, height int
		enabled       bool
	}{{"playback-transitions-wide.png", 1000, 1040, true}, {"playback-transitions-compact.png", 720, 900, true}, {"playback-transitions-disabled.png", 900, 1040, false}} {
		a := newTestApp()
		a.settingsPage = "playback"
		a.settings.CrossfadeEnabled = scenario.enabled
		a.settings.PlaybackFadeEnabled = scenario.enabled
		tester := ui.NewTester(a.settingsView, scenario.width, scenario.height)
		settled(tester)
		saveFrame(t, directory, scenario.name, tester.Image())
	}
}

// 分类和图标在普通、窄屏和深色设置页中都使用真实视图渲染，布局由帧图验收。
func TestSettingsCategoriesFrame(t *testing.T) {
	directory := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	for _, scenario := range []struct {
		name, page    string
		width, height int
		dark          bool
	}{
		{"settings-general-categories.png", "general", 1000, 800, false},
		{"settings-general-compact.png", "general", 640, 800, false},
		{"settings-general-minimum.png", "general", 640, 420, false},
		{"settings-playback-dark.png", "playback", 900, 1000, true},
	} {
		a := newTestApp()
		a.settingsPage = scenario.page
		a.settings.CrossfadeEnabled = true
		tester := ui.NewTester(a.settingsView, scenario.width, scenario.height)
		tester.SetDark(scenario.dark)
		settled(tester)
		saveFrame(t, directory, scenario.name, tester.Image())
	}
}

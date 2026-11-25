package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
)

// 用户在通用设置启用或关闭自启后，系统登记应随之改变。
func TestStartupSwitchEnablesAndDisablesLogin(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("startup settings are offered on Windows and macOS")
	}
	previousRead, previousSet := openAtLogin, setOpenAtLogin
	t.Cleanup(func() { openAtLogin, setOpenAtLogin = previousRead, previousSet })
	enabled := false
	openAtLogin = func() bool { return enabled }
	setOpenAtLogin = func(value bool) error {
		enabled = value
		return nil
	}
	a := newTestApp()
	a.settingsPage = "general"
	tester := ui.NewTester(a.settingsView, 1000, 1000)
	settled(tester)

	clickSettingRole(t, tester, ui.RoleSwitch, "Launch at login")
	tester.Frame()
	if !enabled {
		t.Fatal("enabling startup did not register it with the system")
	}
	clickSettingRole(t, tester, ui.RoleSwitch, "Launch at login")
	tester.Frame()
	if enabled {
		t.Fatal("disabling startup did not remove its system registration")
	}
}

// 系统拒绝变更时，设置页按当前语言提示去哪检查，重试成功后清除错误。
func TestStartupFailureIsVisibleAndRetryable(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("startup settings are offered on Windows and macOS")
	}
	for _, scenario := range []struct {
		language, label, query, windowsHint, macHint string
		enabled                                      bool
	}{
		{i18n.English, "Launch at login", "", "Settings > Apps > Startup", "System Settings > General > Login Items", false},
		{i18n.Chinese, "开机自启", "自启", "设置 → 应用 → 启动", "系统设置 → 通用 → 登录项", true},
	} {
		t.Run(scenario.language, func(t *testing.T) {
			previousLanguage := i18n.Language()
			t.Cleanup(func() { i18n.SetLanguage(previousLanguage) })
			i18n.SetLanguage(scenario.language)
			previousRead, previousSet := openAtLogin, setOpenAtLogin
			t.Cleanup(func() { openAtLogin, setOpenAtLogin = previousRead, previousSet })
			enabled, blocked := scenario.enabled, true
			openAtLogin = func() bool { return enabled }
			setOpenAtLogin = func(value bool) error {
				if blocked {
					return errors.New("permission denied")
				}
				enabled = value
				return nil
			}
			a := newTestApp()
			a.settingsPage = "general"
			a.settingsQuery = scenario.query
			tester := ui.NewTester(a.settingsView, 1000, 1000)
			settled(tester)

			clickSettingRole(t, tester, ui.RoleSwitch, scenario.label)
			tester.Frame()
			control, found := tester.AccessByRole(ui.RoleSwitch, scenario.label)
			if !found || control.Checked != scenario.enabled {
				t.Fatal("a failed change misrepresented the system's startup state")
			}
			wantHint := scenario.windowsHint
			if runtime.GOOS == "darwin" {
				wantHint = scenario.macHint
			}
			if alert, found := tester.AccessByRole(ui.RoleAlert, ""); !found || !strings.Contains(alert.Name, wantHint) || strings.Contains(alert.Name, "permission denied") {
				t.Fatalf("expected a localized startup settings path, got %q", alert.Name)
			}

			blocked = false
			clickSettingRole(t, tester, ui.RoleSwitch, scenario.label)
			tester.Frame()
			if enabled == scenario.enabled {
				t.Fatal("retry did not change the system's startup state")
			}
			if _, found := tester.AccessByRole(ui.RoleAlert, ""); found {
				t.Fatal("a successful retry left the previous failure visible")
			}
		})
	}
}

// 系统设置被外部修改后，重新查看设置和重建应用都应呈现系统当前值。
func TestStartupTracksSystemChanges(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("startup settings are offered on Windows and macOS")
	}
	previousRead, previousSet := openAtLogin, setOpenAtLogin
	t.Cleanup(func() { openAtLogin, setOpenAtLogin = previousRead, previousSet })
	enabled := true
	openAtLogin = func() bool { return enabled }
	setOpenAtLogin = func(bool) error {
		t.Fatal("viewing settings must preserve the system's startup choice")
		return nil
	}
	a := newTestApp()
	a.settingsPage = "general"
	tester := ui.NewTester(a.settingsView, 1000, 1000)
	settled(tester)
	if control, found := tester.AccessByRole(ui.RoleSwitch, "Launch at login"); !found || !control.Checked {
		t.Fatal("an existing startup registration was not reflected")
	}

	tester.SetWindowActive(false)
	enabled = false
	tester.SetWindowActive(true)
	tester.Frame()
	if control, found := tester.AccessByRole(ui.RoleSwitch, "Launch at login"); !found || control.Checked {
		t.Fatal("disabling startup in system settings was not reflected")
	}

	enabled = true
	restarted := newTestApp()
	restarted.settingsPage = "general"
	tester = ui.NewTester(restarted.settingsView, 1000, 1000)
	settled(tester)
	if control, found := tester.AccessByRole(ui.RoleSwitch, "Launch at login"); !found || !control.Checked {
		t.Fatal("restarting the app lost the system's startup choice")
	}
}

// 用户选择的后台启动方式应写入设置，并在旧配置读取时保持关闭。
func TestStartupBackgroundChoiceSurvivesRestart(t *testing.T) {
	preferences := defaultSettings()
	if preferences.LaunchAtLoginBackground {
		t.Fatal("后台启动不应默认开启")
	}
	if err := json.Unmarshal([]byte(`{"launchAtLoginBackground":true}`), &preferences); err != nil {
		t.Fatal(err)
	}
	if !preferences.LaunchAtLoginBackground {
		t.Fatal("没有读取后台启动选择")
	}
	encoded, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.LaunchAtLoginBackground {
		t.Fatal("重启后丢失后台启动选择")
	}
}

// 登录启动且开启后台模式时隐藏主窗口，普通启动或关闭后台模式时显示主窗口。
func TestLoginBackgroundModeControlsInitialWindowVisibility(t *testing.T) {
	for _, scenario := range []struct {
		name, want         string
		opened, background bool
	}{
		{"manual launch", "visible", false, true},
		{"login foreground", "visible", true, false},
		{"login background", "hidden", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := initialWindowHidden(scenario.opened, scenario.background); got != (scenario.want == "hidden") {
				t.Fatalf("initial window visibility = %v, want %s", got, scenario.want)
			}
		})
	}
}

// 启用自启后，用户可以在真实设置页切换登录时后台运行并保存选择。
func TestStartupBackgroundSwitchSavesChoice(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("startup settings are offered on Windows and macOS")
	}
	previousRead, previousSet := openAtLogin, setOpenAtLogin
	t.Cleanup(func() { openAtLogin, setOpenAtLogin = previousRead, previousSet })
	openAtLogin = func() bool { return true }
	setOpenAtLogin = func(bool) error { return nil }

	a := newTestApp()
	a.settingsPage = "general"
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tester := ui.NewTester(a.settingsView, 1000, 1000)
	settled(tester)
	control, found := tester.AccessByRole(ui.RoleSwitch, "Start in background at login")
	if !found || control.Disabled {
		t.Fatal("后台启动开关不可操作")
	}
	clickSettingRole(t, tester, ui.RoleSwitch, "Start in background at login")
	tester.Frame()
	if !a.settings.LaunchAtLoginBackground {
		t.Fatal("后台启动开关没有更新设置")
	}
	encoded, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.LaunchAtLoginBackground {
		t.Fatal("后台启动选择没有保存")
	}
	clickSettingRole(t, tester, ui.RoleSwitch, "Start in background at login")
	tester.Frame()
	if a.settings.LaunchAtLoginBackground {
		t.Fatal("关闭后台启动没有生效")
	}
}

// 保存失败时保留原来的后台启动选项，并在设置页提示重试。
func TestStartupBackgroundSaveFailure(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("startup settings are offered on Windows and macOS")
	}
	previousRead := openAtLogin
	t.Cleanup(func() { openAtLogin = previousRead })
	openAtLogin = func() bool { return true }
	a := newTestApp()
	a.settingsPage = "general"
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	a.settings.path = filepath.Join(blocked, "settings.json")
	tester := ui.NewTester(a.settingsView, 1000, 1000)
	settled(tester)
	clickSettingRole(t, tester, ui.RoleSwitch, "Start in background at login")
	tester.Frame()
	if a.settings.LaunchAtLoginBackground {
		t.Fatal("保存失败后没有恢复原来的后台启动选项")
	}
	if _, found := tester.AccessByRole(ui.RoleAlert, "Could not save background startup setting. Try again."); !found {
		t.Fatal("设置页没有提示后台启动选项保存失败")
	}
}

// 在不同设置窗口宽度下查看自启选项和失败提示。
func TestStartupSettingsFrames(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("startup settings are offered on Windows and macOS")
	}
	dir := framesDir(t)
	previousRead := openAtLogin
	t.Cleanup(func() { openAtLogin = previousRead })
	openAtLogin = func() bool { return true }
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{"wide", 780, 720},
		{"narrow", 640, 420},
	} {
		a := newTestApp()
		a.settingsPage = "general"
		a.settingsQuery = "login"
		a.startupFailed = true
		a.startupBackgroundFailed = true
		tester := ui.NewTester(a.settingsView, size.width, size.height)
		settled(tester)
		saveFrame(t, dir, "startup-"+size.name+".png", tester.Image())
	}
}

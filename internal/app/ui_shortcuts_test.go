package app

import (
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
)

func shortcutSettingsTester(t *testing.T) (*app, *fakeShortcutRegistrar, *ui.Tester) {
	t.Helper()
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("global shortcuts are implemented on Windows and macOS")
	}
	a := newTestApp()
	registrar := newFakeShortcutRegistrar()
	cfg := completeShortcuts(nil)
	cfg.Enabled = true
	a.globalShortcuts = newShortcutManager(cfg, registrar, a.globalShortcutAction)
	a.globalShortcuts.retry()
	a.settingsPage = "shortcuts"
	tester := ui.NewTester(a.settingsView, 780, 1000)
	tester.Frame()
	return a, registrar, tester
}

func clickShortcut(t *testing.T, tester *ui.Tester, label string) {
	t.Helper()
	if err := tester.Click(label); err != nil {
		t.Fatal(err)
	}
}

// A recorded combination applies on release, saves, and restores the suspended actions.
func TestShortcutUIRecord(t *testing.T) {
	a, registrar, tester := shortcutSettingsTester(t)
	a.settings.path = t.TempDir() + "/settings.json"
	clickShortcut(t, tester, "Change Play / Pause shortcut")
	if len(registrar.keys) != 0 || a.shortcutEdit.recording != shortcutPlay {
		t.Fatal("recording did not suspend all actions")
	}
	tester.KeyRepeat(ui.Ctrl|ui.Alt, ui.KeyQ)
	if a.shortcutEdit.candidate != "" {
		t.Fatal("auto-repeat became a recorded key")
	}
	tester.Key(ui.Ctrl|ui.Alt|ui.Shift, ui.KeyP)
	if a.shortcutEdit.recording != "" || len(registrar.keys) != 8 || a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Ctrl+Alt+Shift+P" {
		t.Fatalf("recording did not commit and resume: editor=%+v, active=%d", a.shortcutEdit, len(registrar.keys))
	}
	if a.settings.Shortcuts == nil || a.settings.Shortcuts.Bindings[shortcutPlay].Key != "Ctrl+Alt+Shift+P" {
		t.Fatal("did not persist the recorded combination")
	}
}

// Command combinations record, register and save on macOS.
func TestShortcutUIRecordCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Command is the macOS modifier")
	}
	a, registrar, tester := shortcutSettingsTester(t)
	a.settings.path = t.TempDir() + "/settings.json"
	for _, recorded := range []struct {
		mods ui.Modifiers
		key  ui.Key
		want string
	}{
		{ui.Super, ui.KeyP, "Super+P"},
		{ui.Super | ui.Ctrl | ui.Alt | ui.Shift, ui.KeyLeft, "Super+Ctrl+Alt+Shift+Left"},
	} {
		clickShortcut(t, tester, "Change Play / Pause shortcut")
		tester.Key(recorded.mods, recorded.key)
		if a.shortcutEdit.recording != "" || a.globalShortcuts.config.Bindings[shortcutPlay].Key != recorded.want || registrar.keys[recorded.want] == nil || len(registrar.keys) != 8 {
			t.Fatalf("did not apply %s: editor=%+v, active=%d", recorded.want, a.shortcutEdit, len(registrar.keys))
		}
		if a.settings.Shortcuts == nil || a.settings.Shortcuts.Bindings[shortcutPlay].Key != recorded.want {
			t.Fatalf("did not persist %s", recorded.want)
		}
	}
}

// A key that only its position names is recorded by position on macOS.
func TestShortcutUIRecordPhysicalKey(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("positions matter on macOS")
	}
	a, registrar, tester := shortcutSettingsTester(t)
	clickShortcut(t, tester, "Change Play / Pause shortcut")
	tester.KeyAt(ui.Super, ui.KeyP)
	if a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Super+P" || registrar.keys["Super+P"] == nil {
		t.Fatalf("did not record the position: editor=%+v", a.shortcutEdit)
	}
}

// Invalid input stays in recording; Escape, focus loss and page navigation all release the pause.
func TestShortcutUICancelAndFocusLoss(t *testing.T) {
	for _, ending := range []string{"escape", "blur", "page"} {
		t.Run(ending, func(t *testing.T) {
			a, registrar, tester := shortcutSettingsTester(t)
			clickShortcut(t, tester, "Change Play / Pause shortcut")
			tester.Key(ui.Shift, ui.KeyP)
			if a.shortcutEdit.recording != shortcutPlay || a.shortcutEdit.errors[shortcutPlay] == nil {
				t.Fatal("invalid input ended recording without explanation")
			}
			switch ending {
			case "escape":
				tester.Key(0, ui.KeyEscape)
			case "blur":
				tester.SetWindowActive(false)
				tester.Frame()
			case "page":
				clickShortcut(t, tester, "General")
			}
			if a.shortcutEdit.recording != "" || len(registrar.keys) != 8 || a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Ctrl+Alt+P" {
				t.Fatal("cancel did not preserve and restore original keys")
			}
		})
	}
}

// Recording takes the settings search shortcut itself rather than letting it open the search field.
func TestShortcutUIShieldsLocalCommands(t *testing.T) {
	a, _, tester := shortcutSettingsTester(t)
	clickShortcut(t, tester, "Change Play / Pause shortcut")
	tester.Key(ui.Cmd, ui.KeyF)
	want := "Ctrl+F"
	if runtime.GOOS == "darwin" {
		want = "Super+F"
	}
	if a.globalShortcuts.config.Bindings[shortcutPlay].Key != want || a.settingsQuery != "" {
		t.Fatal("a settings shortcut intercepted recording")
	}
}

// Duplicate and unavailable recorded keys leave the old configuration and bindings usable.
func TestShortcutUIConflict(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			a, registrar, tester := shortcutSettingsTester(t)
			key := ui.KeyM
			if external {
				key = ui.KeyQ
				registrar.blocked["Ctrl+Alt+Q"] = true
			}
			clickShortcut(t, tester, "Change Play / Pause shortcut")
			tester.Key(ui.Ctrl|ui.Alt, key)
			if a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Ctrl+Alt+P" || a.shortcutEdit.errors[shortcutPlay] == nil || len(registrar.keys) != 8 {
				t.Fatal("conflict lost the original binding or error")
			}
		})
	}
}

// Single-action disabling keeps its key; master off/on preserves that choice and retries failures.
func TestShortcutUISwitchAndRetry(t *testing.T) {
	a, registrar, tester := shortcutSettingsTester(t)
	clickSettingRole(t, tester, ui.RoleSwitch, "Enable Play / Pause shortcut")
	if a.globalShortcuts.config.Bindings[shortcutPlay].Enabled || registrar.keys["Ctrl+Alt+P"] != nil {
		t.Fatal("single disable did not release its key")
	}
	clickSettingRole(t, tester, ui.RoleSwitch, "Global Shortcuts")
	if len(registrar.keys) != 0 {
		t.Fatal("master off left keys registered")
	}
	clickSettingRole(t, tester, ui.RoleSwitch, "Global Shortcuts")
	if len(registrar.keys) != 7 || a.globalShortcuts.config.Bindings[shortcutPlay].Enabled {
		t.Fatal("master toggle reset individual choice")
	}
	registrar.blocked["Ctrl+Alt+P"] = true
	clickSettingRole(t, tester, ui.RoleSwitch, "Enable Play / Pause shortcut")
	if a.globalShortcuts.failures[shortcutPlay] == nil || !a.globalShortcuts.config.Bindings[shortcutPlay].Enabled {
		t.Fatal("failure did not preserve enabled intent")
	}
	delete(registrar.blocked, "Ctrl+Alt+P")
	clickShortcut(t, tester, "Retry Play / Pause shortcut")
	if a.globalShortcuts.failures[shortcutPlay] != nil || len(registrar.keys) != 8 {
		t.Fatal("manual retry failed")
	}
}

// Restoring defaults requires confirmation and keeps both disabled choices and edited keys on cancel.
func TestShortcutUIRestoreConfirmation(t *testing.T) {
	a, _, tester := shortcutSettingsTester(t)
	cfg := a.globalShortcuts.config.clone()
	cfg.Enabled = false
	cfg.Bindings[shortcutPlay] = shortcutBinding{"Alt+Q", false}
	a.applyShortcutConfig(cfg, true, "")
	tester.Frame()
	clickShortcut(t, tester, "Restore All Default Keys")
	if a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Alt+Q" {
		t.Fatal("restored before confirmation")
	}
	clickShortcut(t, tester, "Cancel")
	if a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Alt+Q" {
		t.Fatal("cancel changed the configuration")
	}
	clickShortcut(t, tester, "Restore All Default Keys")
	clickShortcut(t, tester, "Restore All")
	binding := a.globalShortcuts.config.Bindings[shortcutPlay]
	if binding.Key != "Ctrl+Alt+P" || binding.Enabled || a.globalShortcuts.config.Enabled {
		t.Fatal("restoring keys reset enabled choices")
	}
}

// Restoring one row changes its key and preserves that row's disabled choice.
func TestShortcutUISingleRestore(t *testing.T) {
	a, _, tester := shortcutSettingsTester(t)
	cfg := a.globalShortcuts.config.clone()
	cfg.Bindings[shortcutPlay] = shortcutBinding{"Alt+Q", false}
	a.applyShortcutConfig(cfg, true, "")
	tester.Frame()
	clickShortcut(t, tester, "Restore Play / Pause shortcut")
	binding := a.globalShortcuts.config.Bindings[shortcutPlay]
	if binding.Key != "Ctrl+Alt+P" || binding.Enabled || !a.globalShortcuts.config.Enabled {
		t.Fatal("single restore changed enabled choices or failed to restore its key")
	}
}

// A remap save error leaves the working key and enabled choice intact in the UI.
func TestShortcutUIWriteFailure(t *testing.T) {
	a, registrar, tester := shortcutSettingsTester(t)
	a.settings.path = t.TempDir()
	clickShortcut(t, tester, "Change Play / Pause shortcut")
	tester.Key(ui.Alt, ui.KeyQ)
	if a.shortcutEdit.errors[shortcutPlay] == nil || a.globalShortcuts.config.Bindings[shortcutPlay].Key != "Ctrl+Alt+P" || registrar.keys["Ctrl+Alt+P"] == nil {
		t.Fatal("write failure replaced the saved or active key")
	}
}

// With MM_FRAMES set, inspect the real settings view at normal and narrow widths, including errors.
func TestShortcutSettingsFrames(t *testing.T) {
	dir := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	for _, width := range []int{780, 640} {
		a, registrar, _ := shortcutSettingsTester(t)
		i18n.SetLanguage(i18n.Chinese)
		a.globalShortcuts.pause()
		registrar.blocked["Ctrl+Alt+P"] = true
		a.globalShortcuts.resume()
		a.shortcutEdit.errors = map[shortcutAction]error{shortcutPlay: errors.New(T("Could not save shortcuts"))}
		cfg := a.globalShortcuts.config.clone()
		cfg.Bindings[shortcutUp] = shortcutBinding{"Ctrl+Alt+Shift+PageUp", true}
		a.applyShortcutConfig(cfg, false, shortcutUp)
		tester := ui.NewTester(a.settingsView, width, 720)
		settled(tester)
		saveFrame(t, dir, fmt.Sprintf("shortcuts-%d.png", width), tester.Image())
		clickShortcut(t, tester, Tf("Change %s shortcut", T("Play / Pause")))
		tester.Key(ui.Shift, ui.KeyP)
		tester.Frame()
		saveFrame(t, dir, fmt.Sprintf("shortcuts-recording-%d.png", width), tester.Image())
	}
}

package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

type fakeShortcutRegistrar struct {
	keys     map[string]func()
	blocked  map[string]bool
	released []string
}

func newFakeShortcutRegistrar() *fakeShortcutRegistrar {
	return &fakeShortcutRegistrar{keys: map[string]func(){}, blocked: map[string]bool{}}
}

func (r *fakeShortcutRegistrar) Register(key string, fn func()) error {
	if r.blocked[key] || r.keys[key] != nil {
		return errors.New("key is taken")
	}
	r.keys[key] = fn
	return nil
}

func (r *fakeShortcutRegistrar) Unregister(key string) {
	delete(r.keys, key)
	r.released = append(r.released, key)
}

func testShortcutManager() (*shortcutManager, *fakeShortcutRegistrar) {
	cfg := completeShortcuts(nil)
	cfg.Enabled = true
	registrar := newFakeShortcutRegistrar()
	manager := newShortcutManager(cfg, registrar, func(shortcutAction) {})
	manager.retry()
	return manager, registrar
}

func saveShortcutsForTest(shortcutConfig) error { return nil }

// Equivalent accelerators conflict even when modifiers have another order or case.
func TestShortcutNormalizationAndConflicts(t *testing.T) {
	for input, want := range map[string]string{
		" alt + control + shift + p ": "Ctrl+Alt+Shift+P",
		"Alt+left":                    "Alt+Left", "ctrl+F12": "Ctrl+F12", "Ctrl+,": "Ctrl+,",
	} {
		got, err := normalizeShortcut(input)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"P", "Shift+P", "Win+P", "Ctrl+Alt", "Ctrl+Escape", "Ctrl+F99", "Ctrl++"} {
		if _, err := normalizeShortcut(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	cfg := completeShortcuts(nil)
	binding := cfg.Bindings[shortcutMute]
	binding.Key, binding.Enabled = "alt+ctrl+p", false
	cfg.Bindings[shortcutMute] = binding
	if _, err := validateShortcuts(cfg); err == nil {
		t.Fatal("accepted a duplicate stored on a disabled action")
	}
	if _, err := shortcutFromInput(ui.KeyP, ui.Ctrl|ui.Super); err == nil {
		t.Fatal("accepted the Windows modifier")
	}
}

// Adding missing actions preserves both the master choice and explicit per-action false.
func TestShortcutMigrationPreservesChoices(t *testing.T) {
	var saved settings
	if err := json.Unmarshal([]byte(`{"globalShortcuts":{"enabled":true,"bindings":{"playPause":{"key":"Alt+P","enabled":false}}}}`), &saved); err != nil {
		t.Fatal(err)
	}
	cfg := completeShortcuts(saved.Shortcuts)
	if !cfg.Enabled || cfg.Bindings[shortcutPlay].Enabled || cfg.Bindings[shortcutPlay].Key != "Alt+P" {
		t.Fatalf("lost explicit choices: %+v", cfg)
	}
	registrar := newFakeShortcutRegistrar()
	manager := newShortcutManager(cfg, registrar, func(shortcutAction) {})
	manager.retry()
	if registrar.keys["Alt+P"] != nil || registrar.keys["Ctrl+Alt+Right"] == nil {
		t.Fatal("migration changed which actions register")
	}
	legacy := newShortcutManager(completeShortcuts(nil), newFakeShortcutRegistrar(), func(shortcutAction) {})
	legacy.retry()
	if len(legacy.active) != 0 {
		t.Fatal("upgrading unexpectedly claimed global keys")
	}
}

// A failed remap or save keeps the old working key and rejects staged callbacks.
func TestShortcutRemapRollback(t *testing.T) {
	for _, failure := range []string{"register", "save"} {
		t.Run(failure, func(t *testing.T) {
			manager, registrar := testShortcutManager()
			fired := 0
			manager.dispatch = func(shortcutAction) { fired++ }
			old := registrar.keys["Ctrl+Alt+P"]
			cfg := manager.config.clone()
			cfg.Bindings[shortcutPlay] = shortcutBinding{"Alt+Q", true}
			save := saveShortcutsForTest
			if failure == "register" {
				registrar.blocked["Alt+Q"] = true
			} else {
				save = func(shortcutConfig) error { return errors.New("disk is full") }
			}
			if err := manager.apply(cfg, false, save); err == nil {
				t.Fatal("accepted failed change")
			}
			if manager.config.Bindings[shortcutPlay].Key != "Ctrl+Alt+P" || registrar.keys["Ctrl+Alt+P"] == nil || registrar.keys["Alt+Q"] != nil {
				t.Fatal("failed remap lost old configuration or registration")
			}
			old()
			if fired != 1 {
				t.Fatal("old callback stopped working")
			}
		})
	}
}

// Successful remapping retires the old callback even if the native event was already queued.
func TestShortcutRemapRetiresCallback(t *testing.T) {
	manager, registrar := testShortcutManager()
	fired := 0
	manager.dispatch = func(shortcutAction) { fired++ }
	old := registrar.keys["Ctrl+Alt+P"]
	cfg := manager.config.clone()
	cfg.Bindings[shortcutPlay] = shortcutBinding{"Alt+Q", true}
	if err := manager.apply(cfg, false, saveShortcutsForTest); err != nil {
		t.Fatal(err)
	}
	old()
	if fired != 0 {
		t.Fatal("dispatched a retired callback")
	}
	registrar.keys["Alt+Q"]()
	if fired != 1 {
		t.Fatal("new binding did not dispatch")
	}
}

// An unrelated unavailable action does not prevent editing another working binding.
func TestShortcutRemapWithOtherFailure(t *testing.T) {
	manager, registrar := testShortcutManager()
	manager.pause()
	registrar.blocked["Ctrl+Alt+M"] = true
	manager.resume()
	cfg := manager.config.clone()
	cfg.Bindings[shortcutPlay] = shortcutBinding{"Alt+Q", true}
	if err := manager.apply(cfg, false, saveShortcutsForTest); err != nil {
		t.Fatal(err)
	}
	if manager.failures[shortcutMute] == nil || registrar.keys["Alt+Q"] == nil {
		t.Fatal("unrelated failure was lost or blocked remapping")
	}
}

// Enabling can succeed partially, retry only claims unavailable keys, and disabling releases them.
func TestShortcutPartialEnableRetryAndDisable(t *testing.T) {
	registrar := newFakeShortcutRegistrar()
	registrar.blocked["Ctrl+Alt+P"] = true
	manager := newShortcutManager(completeShortcuts(nil), registrar, func(shortcutAction) {})
	cfg := manager.config.clone()
	cfg.Enabled = true
	if err := manager.apply(cfg, true, saveShortcutsForTest); err != nil {
		t.Fatal(err)
	}
	if !manager.config.Enabled || len(manager.active) != 7 || manager.failures[shortcutPlay] == nil {
		t.Fatal("did not keep partial success and enabled intent")
	}
	delete(registrar.blocked, "Ctrl+Alt+P")
	manager.retry()
	if len(manager.active) != 8 || len(manager.failures) != 0 {
		t.Fatal("retry did not complete")
	}
	cfg = manager.config.clone()
	binding := cfg.Bindings[shortcutPlay]
	binding.Enabled = false
	cfg.Bindings[shortcutPlay] = binding
	if err := manager.apply(cfg, true, saveShortcutsForTest); err != nil {
		t.Fatal(err)
	}
	if registrar.keys[binding.Key] != nil || manager.config.Bindings[shortcutPlay].Key != binding.Key {
		t.Fatal("disabling lost the key or failed to release it")
	}
}

// Pausing rejects queued callbacks; a resume that loses a key records failure without changing intent.
func TestShortcutRecordingPauseResumeAndQuit(t *testing.T) {
	manager, registrar := testShortcutManager()
	fired := 0
	manager.dispatch = func(shortcutAction) { fired++ }
	old := registrar.keys["Ctrl+Alt+P"]
	manager.pause()
	old()
	if len(registrar.keys) != 0 || fired != 0 {
		t.Fatal("recording left actions active")
	}
	registrar.blocked["Ctrl+Alt+P"] = true
	manager.resume()
	manager.resume()
	if len(registrar.keys) != 7 || manager.failures[shortcutPlay] == nil || !manager.config.Bindings[shortcutPlay].Enabled {
		t.Fatal("resume did not preserve partial success")
	}
	callback := registrar.keys["Ctrl+Alt+Right"]
	manager.stop()
	manager.resume()
	manager.retry()
	callback()
	if len(registrar.keys) != 0 || fired != 0 {
		t.Fatal("quit registered or dispatched an action")
	}
}

// Restoring a swapped set of keys is transactional, including a disk error after registration.
func TestShortcutBatchRestoreRollback(t *testing.T) {
	manager, registrar := testShortcutManager()
	cfg := manager.config.clone()
	first, second := cfg.Bindings[shortcutPlay], cfg.Bindings[shortcutMute]
	first.Key, second.Key = second.Key, first.Key
	cfg.Bindings[shortcutPlay], cfg.Bindings[shortcutMute] = first, second
	if err := manager.apply(cfg, false, func(shortcutConfig) error { return errors.New("read only") }); err == nil {
		t.Fatal("accepted a failed save")
	}
	if len(registrar.keys) != 8 || manager.active[shortcutPlay].key != "Ctrl+Alt+P" || manager.active[shortcutMute].key != "Ctrl+Alt+M" {
		t.Fatal("failed swap did not restore the complete original set")
	}
	if err := manager.apply(cfg, false, saveShortcutsForTest); err != nil {
		t.Fatal(err)
	}
	if manager.active[shortcutPlay].key != "Ctrl+Alt+M" || manager.active[shortcutMute].key != "Ctrl+Alt+P" {
		t.Fatal("valid swap was not committed")
	}
}

// Recording a new key must validate native registration before replacing saved configuration.
func TestShortcutRecordedRemapFailure(t *testing.T) {
	manager, registrar := testShortcutManager()
	manager.pause()
	registrar.blocked["Alt+Q"] = true
	cfg := manager.config.clone()
	cfg.Bindings[shortcutPlay] = shortcutBinding{"Alt+Q", true}
	if err := manager.apply(cfg, false, saveShortcutsForTest); err == nil {
		t.Fatal("saved an unavailable recorded key")
	}
	manager.resume()
	if manager.config.Bindings[shortcutPlay].Key != "Ctrl+Alt+P" || len(registrar.keys) != 8 {
		t.Fatal("recording failed to preserve original keys")
	}
}

// A failed settings replacement keeps the existing file intact and removes temporary writes.
func TestShortcutSettingsSaveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.path = path
	if err := s.save(); err == nil {
		t.Fatal("replaced a directory as though it were settings")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("left temporary settings or damaged destination: %v, %v", entries, err)
	}
}

package app

import (
	"fmt"
	"maps"
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"
)

type shortcutAction string

const (
	shortcutPlay shortcutAction = "playPause"
	shortcutPrev shortcutAction = "previous"
	shortcutNext shortcutAction = "next"
	shortcutUp   shortcutAction = "volumeUp"
	shortcutDown shortcutAction = "volumeDown"
	shortcutMute shortcutAction = "mute"
	shortcutLike shortcutAction = "like"
	shortcutShow shortcutAction = "showHide"
)

var shortcutDefaults = []struct {
	id    shortcutAction
	label string
	key   string
}{
	{shortcutPlay, N("Play / Pause"), "Ctrl+Alt+P"},
	{shortcutPrev, N("Previous"), "Ctrl+Alt+Left"},
	{shortcutNext, N("Next"), "Ctrl+Alt+Right"},
	{shortcutUp, N("Volume Up"), "Ctrl+Alt+Up"},
	{shortcutDown, N("Volume Down"), "Ctrl+Alt+Down"},
	{shortcutMute, N("Mute / Restore Volume"), "Ctrl+Alt+M"},
	{shortcutLike, N("Like / Unlike"), "Ctrl+Alt+L"},
	{shortcutShow, N("Show / Hide Main Window"), "Ctrl+Alt+H"},
}

type shortcutBinding struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

type shortcutConfig struct {
	Enabled  bool                               `json:"enabled"`
	Bindings map[shortcutAction]shortcutBinding `json:"bindings"`
}

func completeShortcuts(saved *shortcutConfig) shortcutConfig {
	cfg := shortcutConfig{Bindings: map[shortcutAction]shortcutBinding{}}
	if saved != nil {
		cfg.Enabled = saved.Enabled
	}
	for _, def := range shortcutDefaults {
		binding := shortcutBinding{Key: def.key, Enabled: true}
		// 缺失项才补默认；显式关闭与用户填写的键位必须保留。
		if saved != nil {
			if value, exists := saved.Bindings[def.id]; exists {
				binding = value
			}
		}
		cfg.Bindings[def.id] = binding
	}
	return cfg
}

func (cfg shortcutConfig) clone() shortcutConfig {
	cfg.Bindings = maps.Clone(cfg.Bindings)
	return cfg
}

var shortcutKeyNames = func() map[ui.Key]string {
	keys := map[ui.Key]string{
		ui.KeySpace: "Space", ui.KeyTab: "Tab", ui.KeyEnter: "Enter",
		ui.KeyBackspace: "Backspace", ui.KeyDelete: "Delete", ui.KeyInsert: "Insert",
		ui.KeyHome: "Home", ui.KeyEnd: "End", ui.KeyPageUp: "PageUp", ui.KeyPageDown: "PageDown",
		ui.KeyLeft: "Left", ui.KeyRight: "Right", ui.KeyUp: "Up", ui.KeyDown: "Down",
		ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyComma: ",", ui.KeyPeriod: ".",
		ui.KeySlash: "/", ui.KeySemicolon: ";", ui.KeyQuote: "'",
		ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]", ui.KeyBackslash: "\\", ui.KeyBackquote: "`",
	}
	for index := range 26 {
		keys[ui.KeyA+ui.Key(index)] = string(rune('A' + index))
	}
	for index := range 10 {
		keys[ui.Key0+ui.Key(index)] = string(rune('0' + index))
	}
	for index := range 12 {
		keys[ui.KeyF1+ui.Key(index)] = fmt.Sprintf("F%d", index+1)
	}
	return keys
}()

// shortcutPolicy 返回本平台能登记的修饰键，以及其中必须出现的一个。
// macOS 的 ui.Super 是 Command，Carbon 可以直接登记；Windows 侧策略与文案保持不变。
func shortcutPolicy() (allowed, required ui.Modifiers) {
	if runtime.GOOS == "darwin" {
		return ui.Super | ui.Ctrl | ui.Alt | ui.Shift, ui.Super | ui.Ctrl | ui.Alt
	}
	return ui.Ctrl | ui.Alt | ui.Shift, ui.Ctrl | ui.Alt
}

func shortcutInputError() error {
	if runtime.GOOS == "darwin" {
		return fmt.Errorf("%s", T("Use Command, Control or Option, optionally with Shift, and a supported key"))
	}
	return fmt.Errorf("%s", T("Use Ctrl or Alt, optionally with Shift, and a supported key"))
}

func shortcutModifier(part string) (ui.Modifiers, bool) {
	switch strings.ToLower(strings.TrimSpace(part)) {
	case "super", "cmd", "command", "meta":
		return ui.Super, true
	case "ctrl", "control":
		return ui.Ctrl, true
	case "alt":
		return ui.Alt, true
	case "option":
		return ui.Alt, runtime.GOOS == "darwin"
	case "shift":
		return ui.Shift, true
	}
	return 0, false
}

func shortcutFromInput(key ui.Key, mods ui.Modifiers) (string, error) {
	name, ok := shortcutKeyNames[key]
	allowed, required := shortcutPolicy()
	if !ok || mods&^allowed != 0 || mods&required == 0 {
		return "", shortcutInputError()
	}
	// 规范顺序与 upstream accelerator 一致，保证同一组合只有一种写法。
	var parts []string
	for _, modifier := range []struct {
		mask ui.Modifiers
		name string
	}{{ui.Super, "Super"}, {ui.Ctrl, "Ctrl"}, {ui.Alt, "Alt"}, {ui.Shift, "Shift"}} {
		if mods&modifier.mask != 0 {
			parts = append(parts, modifier.name)
		}
	}
	return strings.Join(append(parts, name), "+"), nil
}

func normalizeShortcut(text string) (string, error) {
	parts := strings.Split(text, "+")
	var mods ui.Modifiers
	for _, part := range parts[:len(parts)-1] {
		modifier, ok := shortcutModifier(part)
		if !ok {
			return "", shortcutInputError()
		}
		mods |= modifier
	}
	name := strings.TrimSpace(parts[len(parts)-1])
	for key, canonical := range shortcutKeyNames {
		if strings.EqualFold(name, canonical) {
			return shortcutFromInput(key, mods)
		}
	}
	return "", shortcutInputError()
}

var shortcutSymbols = map[string]string{
	"Super": "⌘", "Ctrl": "⌃", "Alt": "⌥", "Shift": "⇧",
	"Enter": "↩", "Tab": "⇥", "Space": "␣", "Backspace": "⌫", "Delete": "⌦",
	"Left": "←", "Right": "→", "Up": "↑", "Down": "↓",
	"Home": "↖", "End": "↘", "PageUp": "⇞", "PageDown": "⇟",
}

func shortcutHelpText() string {
	if runtime.GOOS == "darwin" {
		return T("Click a key to record. Use Command, Control or Option; Shift is optional. Esc cancels.")
	}
	return T("Click a key to record. Use Ctrl or Alt; Shift is optional. Esc cancels.")
}

// shortcutDisplay 只改变展示；存储始终是 accelerator 文本。
func shortcutDisplay(text string) string {
	if runtime.GOOS != "darwin" || text == "" {
		return text
	}
	parts := strings.Split(text, "+")
	key := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	var display []string
	// 与 macOS 菜单的惯例保持一致，Command 放在最后。
	for _, modifier := range []string{"Ctrl", "Alt", "Shift", "Super"} {
		for _, part := range mods {
			if part == modifier {
				display = append(display, shortcutSymbols[part])
				break
			}
		}
	}
	if symbol, ok := shortcutSymbols[key]; ok {
		key = symbol
	}
	return strings.Join(append(display, key), "")
}

// shortcutInputKey macOS 按物理位置登记，布局字符可能和 ANSI keycode 对不上。
func shortcutInputKey(ev ui.InputEvent) ui.Key {
	if runtime.GOOS == "darwin" && ev.PhysicalKey != ui.KeyUnknown {
		return ev.PhysicalKey
	}
	if ev.Key != ui.KeyUnknown {
		return ev.Key
	}
	return ev.PhysicalKey
}

func validateShortcuts(cfg shortcutConfig) (shortcutConfig, error) {
	cfg = cfg.clone()
	used := map[string]string{}
	for _, def := range shortcutDefaults {
		binding := cfg.Bindings[def.id]
		key, err := normalizeShortcut(binding.Key)
		if err != nil {
			return cfg, err
		}
		if label, exists := used[key]; exists {
			return cfg, fmt.Errorf("%s", Tf("Already used by %s", label))
		}
		binding.Key, used[key] = key, T(def.label)
		cfg.Bindings[def.id] = binding
	}
	return cfg, nil
}

type shortcutRegistrar interface {
	Register(string, func()) error
	Unregister(string)
}

type shortcutRegistration struct {
	key string
}

// shortcutManager 只在主线程使用；持久启用意愿与本次登记结果分别保存。
type shortcutManager struct {
	config     shortcutConfig
	registrar  shortcutRegistrar
	active     map[shortcutAction]*shortcutRegistration
	failures   map[shortcutAction]error
	paused     bool
	stopped    bool
	generation uint64
	dispatch   func(shortcutAction)
}

func newShortcutManager(cfg shortcutConfig, registrar shortcutRegistrar, dispatch func(shortcutAction)) *shortcutManager {
	return &shortcutManager{config: cfg, registrar: registrar, dispatch: dispatch,
		active: map[shortcutAction]*shortcutRegistration{}, failures: map[shortcutAction]error{}}
}

func (m *shortcutManager) register(id shortcutAction, key string) (*shortcutRegistration, error) {
	if m.registrar == nil {
		return nil, fmt.Errorf("%s", T("Global shortcuts are unavailable on this platform"))
	}
	entry := &shortcutRegistration{key: key}
	err := m.registrar.Register(key, func() {
		// 已排队的旧回调不能越过停用、录制、改键或退出边界。
		if !m.stopped && !m.paused && m.config.Enabled && m.config.Bindings[id].Enabled && m.active[id] == entry {
			m.dispatch(id)
		}
	})
	return entry, err
}

func (m *shortcutManager) release() {
	for id, entry := range m.active {
		m.registrar.Unregister(entry.key)
		delete(m.active, id)
	}
}

func (m *shortcutManager) retry() {
	if m.stopped || m.paused || !m.config.Enabled {
		return
	}
	used := map[string]bool{}
	for _, def := range shortcutDefaults {
		binding := m.config.Bindings[def.id]
		if !binding.Enabled {
			continue
		}
		key, err := normalizeShortcut(binding.Key)
		if err == nil && used[key] {
			err = fmt.Errorf("%s", T("Duplicate shortcut"))
		}
		used[key] = true
		if err == nil && m.active[def.id] != nil {
			continue
		}
		if err == nil {
			var entry *shortcutRegistration
			entry, err = m.register(def.id, key)
			if err == nil {
				m.active[def.id] = entry
			}
		}
		if err == nil {
			delete(m.failures, def.id)
		} else {
			m.failures[def.id] = err
		}
	}
}

func (m *shortcutManager) pause() {
	m.generation++
	m.paused = true
	m.release()
}

func (m *shortcutManager) resume() {
	if !m.paused {
		return
	}
	m.paused = false
	m.retry()
}

func (m *shortcutManager) stop() {
	m.generation++
	m.stopped = true
	m.release()
}

// apply 在新键登记和写盘都成功后才提交；启用时允许逐项失败，改键则整次回滚。
func (m *shortcutManager) apply(next shortcutConfig, partial bool, save func(shortcutConfig) error) error {
	if m.stopped {
		return fmt.Errorf("%s", T("The app is closing"))
	}
	next, err := validateShortcuts(next)
	if err != nil {
		return err
	}
	old := maps.Clone(m.active)
	staged := map[shortcutAction]*shortcutRegistration{}
	released := map[shortcutAction]bool{}
	failures := maps.Clone(m.failures)
	rollback := func() {
		for _, entry := range staged {
			m.registrar.Unregister(entry.key)
		}
		for id := range released {
			// 恢复时键位也可能被外部抢占；保留配置并如实标记未生效。
			entry, restoreErr := m.register(id, old[id].key)
			if restoreErr != nil {
				delete(m.active, id)
				m.failures[id] = restoreErr
			} else {
				m.active[id] = entry
			}
		}
	}
	if next.Enabled {
		// 恢复全部可能交换本应用已有键位，先释放确实挡住目标的旧绑定。
		for _, def := range shortcutDefaults {
			binding := next.Bindings[def.id]
			if !binding.Enabled {
				continue
			}
			for owner, entry := range old {
				if owner != def.id && entry.key == binding.Key {
					m.registrar.Unregister(entry.key)
					delete(m.active, owner)
					released[owner] = true
				}
			}
		}
		for _, def := range shortcutDefaults {
			binding := next.Bindings[def.id]
			if !binding.Enabled {
				continue
			}
			// 录制只试登记新键，其余项目在结束后独立恢复；不让无关失败阻止改键。
			unchanged := binding == m.config.Bindings[def.id] && next.Enabled == m.config.Enabled
			if m.paused && unchanged || !partial && !m.paused && unchanged && m.active[def.id] == nil {
				continue
			}
			if entry := m.active[def.id]; entry != nil && entry.key == binding.Key {
				continue
			}
			entry, registerErr := m.register(def.id, binding.Key)
			if registerErr != nil {
				failures[def.id] = registerErr
				if !partial {
					rollback()
					return fmt.Errorf("%s: %w", T(def.label), registerErr)
				}
			} else {
				staged[def.id] = entry
				delete(failures, def.id)
			}
		}
	}
	if err := save(next); err != nil {
		rollback()
		return fmt.Errorf("%s: %w", T("Could not save shortcuts"), err)
	}
	for id, entry := range old {
		binding := next.Bindings[id]
		if next.Enabled && binding.Enabled && !m.paused && entry.key == binding.Key && !released[id] {
			continue
		}
		if !released[id] {
			m.registrar.Unregister(entry.key)
		}
		delete(m.active, id)
	}
	if m.config.Enabled != next.Enabled || m.config.Bindings[shortcutLike] != next.Bindings[shortcutLike] {
		m.generation++
	}
	m.config, m.failures = next, failures
	for id, binding := range next.Bindings {
		if !next.Enabled || !binding.Enabled {
			delete(m.failures, id)
		}
	}
	for id, entry := range staged {
		m.active[id] = entry
	}
	return nil
}

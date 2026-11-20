package app

import (
	"github.com/egoist/mygo/ui"
)

type shortcutEditor struct {
	recording shortcutAction
	candidate string
	held      ui.Key
	handle    ui.Handle
	hadFocus  bool
	confirm   bool
	error     error
	errors    map[shortcutAction]error
}

func (a *app) startShortcutRecording(c *ui.Context, id shortcutAction) {
	a.endShortcutRecording()
	a.globalShortcuts.pause()
	a.shortcutEdit.recording = id
	a.shortcutEdit.error = nil
	delete(a.shortcutEdit.errors, id)
	a.shortcutEdit.handle.Focus(c)
}

func (a *app) endShortcutRecording() {
	if a.shortcutEdit.recording == "" {
		return
	}
	a.shortcutEdit.recording, a.shortcutEdit.candidate = "", ""
	a.shortcutEdit.held, a.shortcutEdit.hadFocus = ui.KeyUnknown, false
	if a.quitting {
		a.globalShortcuts.stop()
	} else {
		a.globalShortcuts.resume()
	}
	a.redraw()
}

func (a *app) recordShortcut(ev ui.InputEvent) bool {
	editor := &a.shortcutEdit
	if editor.recording == "" {
		return false
	}
	switch ev.Kind {
	case ui.InputKeyDown:
		if ev.Key == ui.KeyEscape {
			a.endShortcutRecording()
			return true
		}
		if ev.Repeat {
			return true
		}
		key := shortcutInputKey(ev)
		if key == ui.KeyUnknown {
			return true // 单独按修饰键继续等待，不当成一次无效提交。
		}
		candidate, err := shortcutFromInput(key, ev.Mods)
		if editor.errors == nil {
			editor.errors = map[shortcutAction]error{}
		}
		editor.errors[editor.recording] = err
		editor.candidate, editor.held = candidate, key
		return true
	case ui.InputKeyUp:
		key := shortcutInputKey(ev)
		if key != editor.held || editor.candidate == "" {
			return true
		}
		id, candidate := editor.recording, editor.candidate
		cfg := a.globalShortcuts.config.clone()
		binding := cfg.Bindings[id]
		binding.Key = candidate
		cfg.Bindings[id] = binding
		// 主键释放后才登记新键，避免录入动作同时触发音乐控制。
		a.applyShortcutConfig(cfg, false, id)
		a.endShortcutRecording()
		return true
	case ui.InputText, ui.InputCommand:
		return true
	}
	return false
}

func (a *app) shortcutSettings(c *ui.Context, p palette, row settingsRow, block settingsBlock) {
	manager := a.globalShortcuts
	row(T("Global Shortcuts"), T("Control music while another app is in front"), func() {
		on := manager.config.Enabled
		if ui.Switch(c.Key("shortcuts-enabled"), &on).Label(T("Global Shortcuts")).Disabled(a.shortcutEdit.recording != "").Changed() {
			cfg := manager.config.clone()
			cfg.Enabled = on
			a.applyShortcutConfig(cfg, true, "")
		}
	})
	block(T("Shortcut Help"), func() {
		ui.Column(c).Padding(10, 16).Gap(5).Children(func() {
			ui.Text(c, shortcutHelpText()).FontSize(12).TextColor(p.muted)
			ui.Text(c, T("While recording, all global shortcuts are paused. Hold a key to trigger only once.")).FontSize(12).TextColor(p.muted)
			if a.shortcutEdit.error != nil {
				ui.Text(c, a.shortcutEdit.error.Error()).FontSize(12).TextColor(p.accent)
			}
		})
	})
	for _, def := range shortcutDefaults {
		def := def
		block(T(def.label), func() {
			a.shortcutSettingsRow(c.Key(string(def.id)), p, def.id, T(def.label))
		})
	}
	block(T("Restore All Default Keys"), func() {
		ui.Column(c.Key("restore-all")).Padding(12, 16).Gap(8).Children(func() {
			if !a.shortcutEdit.confirm {
				ui.Button(c, T("Restore All Default Keys")).Disabled(a.shortcutEdit.recording != "").OnClick(func() {
					a.shortcutEdit.confirm = true
				})
				return
			}
			ui.Text(c, T("Restore all keys? Your enabled choices will be kept.")).FontSize(12).TextColor(p.muted)
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, T("Cancel")).OnClick(func() { a.shortcutEdit.confirm = false })
				ui.Button(c, T("Restore All")).OnClick(func() {
					cfg := manager.config.clone()
					for _, def := range shortcutDefaults {
						binding := cfg.Bindings[def.id]
						binding.Key = def.key
						cfg.Bindings[def.id] = binding
					}
					a.applyShortcutConfig(cfg, false, "")
					a.shortcutEdit.confirm = false
					if a.shortcutEdit.error == nil {
						clear(a.shortcutEdit.errors)
					}
				})
			})
		})
	})
}

func (a *app) shortcutSettingsRow(c *ui.Context, p palette, id shortcutAction, label string) {
	manager, editor := a.globalShortcuts, &a.shortcutEdit
	binding := manager.config.Bindings[id]
	recording := editor.recording == id
	status := T("Disabled")
	switch {
	case recording:
		status = T("Recording — press a combination, or Esc to cancel")
	case manager.paused:
		status = T("Paused while recording")
	case !manager.config.Enabled:
		status = T("Global shortcuts are off")
	case !binding.Enabled:
	case manager.active[id] != nil:
		status = T("Active")
	default:
		status = T("Not active")
	}
	problem := editor.errors[id]
	if problem == nil {
		problem = manager.failures[id]
	}
	ui.Column(c).Padding(10, 16).Gap(5).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).FontSize(13).FontWeight(500).Grow(1).Shrink(1).MinWidth(0)
			if recording {
				text := T("Press keys…")
				if editor.candidate != "" {
					text = shortcutDisplay(editor.candidate)
				}
				recorder := ui.ButtonBase(c.Key("recorder")).Bind(&editor.handle).Label(Tf("Record %s", label)).
					MinWidth(136).Padding(6, 9).Radius(6).Border(1, p.accent).Background(p.hover)
				recorder.Children(func() { ui.Text(c, text).FontSize(12).TextColor(p.accent) })
				recorder.HandleInput(a.recordShortcut)
				if editor.handle.Focused(c) {
					editor.hadFocus = true
				}
				ui.Button(c, T("Cancel")).OnClick(a.endShortcutRecording)
			} else {
				ui.Button(c.Key("key"), shortcutDisplay(binding.Key)).Label(Tf("Change %s shortcut", label)).MinWidth(136).
					Disabled(editor.recording != "").OnClick(func() { a.startShortcutRecording(c, id) })
				on := binding.Enabled
				if ui.Switch(c.Key("enabled"), &on).Label(Tf("Enable %s shortcut", label)).Disabled(editor.recording != "").Changed() {
					cfg := manager.config.clone()
					next := cfg.Bindings[id]
					next.Enabled = on
					cfg.Bindings[id] = next
					a.applyShortcutConfig(cfg, true, id)
				}
				iconButton(c.Key("restore"), iconRefresh, 14, p.muted, Tf("Restore %s shortcut", label)).
					Disabled(editor.recording != "").OnClick(func() {
					cfg := manager.config.clone()
					next := cfg.Bindings[id]
					for _, def := range shortcutDefaults {
						if def.id == id {
							next.Key = def.key
						}
					}
					cfg.Bindings[id] = next
					a.applyShortcutConfig(cfg, false, id)
				})
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, status).FontSize(11.5).TextColor(p.muted)
			if manager.failures[id] != nil && manager.config.Enabled && binding.Enabled && editor.recording == "" {
				ui.Button(c.Key("retry"), T("Retry")).Label(Tf("Retry %s shortcut", label)).OnClick(func() {
					delete(editor.errors, id)
					manager.retry()
					a.redraw()
				})
			}
		})
		if problem != nil {
			ui.Text(c, problem.Error()).FontSize(12).TextColor(p.accent)
		}
	})
}

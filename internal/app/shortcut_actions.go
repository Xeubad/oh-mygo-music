package app

import (
	"log"
	"math"
	"runtime"

	"github.com/egoist/mygo"

	"mygo-music/internal/music"
)

func (a *app) initGlobalShortcuts() {
	var registrar shortcutRegistrar
	if runtime.GOOS == "windows" {
		registrar = mygo.GlobalShortcut
	}
	a.globalShortcuts = newShortcutManager(completeShortcuts(a.settings.Shortcuts), registrar, a.globalShortcutAction)
	if runtime.GOOS == "windows" {
		a.globalShortcuts.retry()
	}
}

func (a *app) globalShortcutAction(id shortcutAction) {
	if a.quitting {
		return
	}
	switch id {
	case shortcutPlay:
		a.pl.TogglePlay()
	case shortcutPrev:
		a.pl.Prev()
	case shortcutNext:
		a.pl.Next()
	case shortcutUp:
		a.setPlayerVolume(a.volume + 0.05)
	case shortcutDown:
		a.setPlayerVolume(a.volume - 0.05)
	case shortcutMute:
		a.toggleMute()
	case shortcutLike:
		a.likeCurrentQuietly()
	case shortcutShow:
		if a.win != nil {
			if a.win.IsFocused() && !a.win.IsMinimized() && a.win.IsVisible() {
				a.win.Hide()
			} else {
				a.showMain()
			}
		}
	}
	a.redraw()
}

func validVolume(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value <= 1
}

func (a *app) setPlayerVolume(value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	value = min(1, max(0, value))
	// 拖动或降音量至零同样留下最近的非零值，和静音按钮保持一致。
	if value == 0 && validVolume(a.volume) {
		a.mutedFrom = a.volume
	}
	if value > 0 {
		a.mutedFrom = value
	}
	a.volume = value
	a.pl.SetVolume(value)
	if draft := a.sharing; draft != nil && draft.clipPreview != nil {
		draft.clipPreview.SetVolume(value, a.settings.Normalize)
	}
	a.settings.Volume, a.settings.MutedFrom = a.volume, a.mutedFrom
}

func (a *app) toggleMute() {
	if a.volume > 0 {
		a.mutedFrom = a.volume
		a.setPlayerVolume(0)
	} else {
		restore := a.mutedFrom
		if !validVolume(restore) {
			restore = 0.5
		}
		a.setPlayerVolume(restore)
	}
	// 静音后即刻保存历史，重启仍能恢复；退出时也会保存当前音量。
	if err := a.settings.save(); err != nil {
		log.Printf("saving volume: %v", err)
	}
}

type shortcutLikeRequest struct {
	lib        *library
	track      music.Track
	listener   string
	version    uint64
	epoch      uint64
	generation uint64
}

func (a *app) likeCurrentQuietly() {
	tr := a.pl.Current()
	if tr == nil {
		return
	}
	lib := a.libOf(tr.Source)
	// 不回退到当前浏览来源，也不从后台引导登录或打开设置。
	if lib == nil || lib.signedOut() || lib.accountUnknown() || lib.needsSignIn || lib.needsSetup || !lib.canLike(tr) {
		return
	}
	liked, known := lib.liked[tr.ID]
	if !known || lib.likesPending[tr.ID] || lib.likesFailed[tr.ID] {
		if lib.can("isLiked") {
			generation := uint64(0)
			if a.globalShortcuts != nil {
				generation = a.globalShortcuts.generation
			}
			if pending := a.shortcutLikePending; pending != nil && pending.lib == lib && pending.track.ID == tr.ID && pending.listener == lib.listener() && pending.epoch == lib.likeEpoch && pending.generation == generation {
				return
			}
			a.shortcutLikePending = &shortcutLikeRequest{lib, *tr, lib.listener(), lib.likeVersions[tr.ID], lib.likeEpoch, generation}
			if lib.likesFailed[tr.ID] {
				delete(lib.liked, tr.ID)
			}
			a.askLiked(lib, []string{tr.ID})
		}
		return
	}
	a.setLike(lib, *tr, !liked, true)
}

func (a *app) resolveShortcutLike(lib *library, id string, success bool) {
	pending := a.shortcutLikePending
	if pending == nil || pending.lib != lib || pending.track.ID != id {
		return
	}
	a.shortcutLikePending = nil
	// 暂停或停用后再恢复，也不能让边界之前的异步收藏意图重新执行。
	if manager := a.globalShortcuts; manager != nil && (manager.generation != pending.generation || manager.stopped || manager.paused || !manager.config.Enabled || !manager.config.Bindings[shortcutLike].Enabled) {
		return
	}
	current := a.pl.Current()
	if !success || a.quitting || current == nil || current.ID != id || current.Source != lib.id ||
		a.libOf(lib.id) != lib || pending.listener != lib.listener() || pending.epoch != lib.likeEpoch ||
		pending.version != lib.likeVersions[id] || lib.signedOut() || lib.accountUnknown() ||
		lib.needsSignIn || lib.needsSetup || !lib.canLike(current) {
		return
	}
	a.setLike(lib, pending.track, !lib.liked[id], true)
}

func (a *app) saveShortcutConfig(cfg shortcutConfig) error {
	next := a.settings
	next.Shortcuts = &cfg
	next.Volume, next.MutedFrom = a.volume, a.mutedFrom
	if err := next.save(); err != nil {
		return err
	}
	a.settings = next
	return nil
}

func (a *app) applyShortcutConfig(cfg shortcutConfig, partial bool, row shortcutAction) {
	err := a.globalShortcuts.apply(cfg, partial, a.saveShortcutConfig)
	if row == "" {
		a.shortcutEdit.error = err
	} else {
		if a.shortcutEdit.errors == nil {
			a.shortcutEdit.errors = map[shortcutAction]error{}
		}
		a.shortcutEdit.errors[row] = err
	}
	a.redraw()
}

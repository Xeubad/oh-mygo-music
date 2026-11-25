package app

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
	"mygo-music/internal/plugin"
)

// 普通插件错误仍可找回和重试，但界面及剪贴板都不暴露原始请求。
func TestPlaybackDetailsCopyAndRetry(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	a.pl = player.New(a.post)
	a.pl.Source = a.sourceByID
	defer func() { a.pl.RemoveQueued(a.pl.QueueEntryID(0)) }()
	requested := make(chan string, 4)
	complete := make(chan struct{})
	const secret = "secret-token-in-plugin-error"
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		requested <- id
		if id == "retry" {
			<-complete
		}
		return nil, errors.New(secret)
	}
	defer close(complete)
	var copied string
	old := copyText
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = old }()
	track := song("retry", "Retry song")
	track.Source = "fake"
	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		if ui.Button(c.Key("test-song"), "Play test song").Clicked() {
			a.pl.PlayQueue([]music.Track{track}, 0)
		}
		a.playerBar(c.Key("bar"))
	}, 920, 360)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()
	click("Play test song")
	if got := wait(t, requested, "first playback"); got != track.ID {
		t.Fatalf("requested %q", got)
	}
	complete <- struct{}{}
	until(t, ch, func() bool { return a.pl.Err() != "" })
	tt.Frame()
	if _, ok := tt.Find(secret); ok {
		t.Fatal("raw error shown in playback bar")
	}
	click("Playback details")
	click("Copy playback diagnostics")
	if copied == "" || strings.Contains(copied, secret) || !strings.Contains(copied, "category=failed") {
		t.Fatalf("unexpected diagnostic: %q", copied)
	}
	click("Playback details")
	click("Retry playback")
	if got := wait(t, requested, "manual retry"); got != track.ID {
		t.Fatalf("retried %q", got)
	}
	if _, ok := tt.Find("Retry playback"); ok || a.pl.Current().ID != track.ID || len(a.pl.Queue()) != 1 {
		t.Fatal("retry did not disable the old action or preserve the queue")
	}
	complete <- struct{}{}
	until(t, ch, func() bool { return a.pl.Err() != "" })
}

// 停用的插件仍能查看其已声明能力，且不会发起账号探测。
func TestDisabledSourceShowsCapabilitiesWithoutProbing(t *testing.T) {
	a := newTestApp()
	src := newFakeSource("Disabled example")
	src.caps = map[string]bool{"search": true, "account": true}
	src.account = func() (*music.Account, error) { t.Error("opened a disabled source account"); return nil, nil }
	lib := newLibrary(src)
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, lib, paletteFor(c.Theme())) }, 640, 520)
	tt.Frame()
	if err := tt.Click("Source capabilities"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("Search"); !ok {
		t.Fatal("declared search is unavailable in the source details")
	}
	if _, ok := tt.Find("Lyrics"); ok {
		t.Fatal("undeclared lyrics were shown as supported")
	}
}

// 停用的插件会明确显示账号状态未知，而不会把它误报成已退出。
func TestDisabledSourceShowsUnknownAccountState(t *testing.T) {
	a := newTestApp()
	src := newFakeSource("Disabled example")
	src.caps = map[string]bool{"account": true}
	lib := newLibrary(src)
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, lib, paletteFor(c.Theme())) }, 640, 520)
	tt.Frame()
	if _, ok := tt.Find("Account status not checked"); !ok {
		t.Fatal("disabled source did not show its unknown account state")
	}
}

// 设置页区分本机与远程播放模式、展现权限和运行状态，且停用来源权限可见而不触发探测。
func TestSourceSettingsModePermissionsAndHealthSummary(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	remoteSrc := newFakeSource("Remote box")
	remoteSrc.info.Playback = music.PlaybackRemote
	remoteSrc.caps["stream"] = false
	remoteLib := addFake(a, remoteSrc)
	remoteLib.account = &music.Account{ID: "remote-user", Name: "Remote User"}
	remoteLib.accountAsked = true

	guardedDisabled := &guardedSource{fakeSource: newFakeSource("Guarded off")}
	guardedDisabled.perms.Network = []string{"api.example.com"}
	guardedDisabled.perms.Process = plugin.Programs{"helper"}
	guardedDisabled.account = func() (*music.Account, error) {
		t.Error("probed disabled guarded source")
		return nil, nil
	}
	disabledLib := newLibrary(guardedDisabled)

	guardedOn := &guardedSource{fakeSource: newFakeSource("Guarded on")}
	guardedOn.perms.Process = plugin.Programs{"helper"}
	onLib := addFake(a, guardedOn)
	onLib.needsSignIn = true

	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.pluginSettings(c.Key("remote"), remoteLib, a.pal)
		a.pluginSettings(c.Key("on"), onLib, a.pal)
		a.pluginSettings(c.Key("disabled"), disabledLib, a.pal)
	}, 720, 960)
	tt.Frame()

	if _, ok := tt.Find("Asks for no network"); !ok {
		t.Fatal("missing explicit no-network scope for remote source")
	}
	if _, ok := tt.Find("Can reach api.example.com"); !ok {
		t.Fatal("disabled source network scope is hidden")
	}
	if _, ok := tt.Find("Program permission needed"); !ok {
		t.Fatal("pending program permission status is missing")
	}
	if _, ok := tt.Find("Sign in required"); !ok {
		t.Fatal("sign-in required status is missing")
	}
	if _, ok := tt.Find("Ready"); !ok {
		t.Fatal("ready status is missing for healthy remote source")
	}
	if err := tt.Click("Allow Programs"); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return guardedOn.Granted() })
	tt.Frame()
	if _, ok := tt.Find("Programs allowed"); !ok {
		t.Fatal("granting programs did not update status summary")
	}
	if err := tt.Click("Stop Allowing"); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return !guardedOn.Granted() })
	tt.Frame()
	if _, ok := tt.Find("Program permission needed"); !ok {
		t.Fatal("revoking programs did not restore permission-needed summary")
	}
	if err := tt.Click("Source capabilities"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("Plays in external app · Download unavailable"); !ok {
		t.Fatal("remote source did not distinguish external playback and unavailable download")
	}
}

// 播放栏与播放页共用安全失败反馈，提供适用恢复入口、不泄露秘密，并在切歌/停用后使旧动作失效。
func TestPlaybackFailureRecoveryActionsAndInvalidation(t *testing.T) {
	const secret = "secret-bearer-token-999"
	for _, tc := range []struct {
		name           string
		err            error
		wantTitle      string
		wantCategory   string
		clickAction    string
		checkAfter     func(t *testing.T, a *app, lib *library)
		keepSingleItem bool
	}{
		{
			name:         "sign-in",
			err:          music.ErrSignIn,
			wantTitle:    "Sign in required",
			wantCategory: "category=sign-in",
			clickAction:  "Sign In",
			checkAfter: func(t *testing.T, a *app, lib *library) {
				if !a.signIn.open || a.signIn.lib != lib {
					t.Fatalf("did not open sign-in dialog for %s", lib.id)
				}
			},
		},
		{
			name:         "setup",
			err:          music.ErrSetup,
			wantTitle:    "Setup required",
			wantCategory: "category=setup",
			clickAction:  "Open Source Settings",
			checkAfter: func(t *testing.T, a *app, _ *library) {
				if a.settingsPage != "plugins" {
					t.Fatalf("did not switch to source settings page: %q", a.settingsPage)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp()
			ch := posts(a)
			a.pl = player.New(a.post)
			a.pl.Source = a.sourceByID
			a.pl.Failed = a.sourceFailed
			t.Cleanup(func() {
				if a.settingsWin != nil {
					a.settingsWin.Close()
				}
				a.pl.RemoveQueued(a.pl.QueueEntryID(0))
			})
			var requests atomic.Int32
			fake(a).stream = func(_, _ string) (*music.Stream, error) {
				requests.Add(1)
				return nil, errors.Join(tc.err, errors.New(secret))
			}
			var copied string
			oldCopy := copyText
			copyText = func(text string) error { copied = text; return nil }
			defer func() { copyText = oldCopy }()

			first := song("first", "First song")
			second := song("second", "Second song")
			first.Source, second.Source = "fake", "fake"
			a.library.home = &music.Browse{Sections: []music.Section{{Title: "Browse", Tracks: []music.Track{second}}}}
			tt := ui.NewTester(a.view, 1080, 760)
			a.nowPlaying = true
			if tc.keepSingleItem {
				a.pl.PlayQueue([]music.Track{first, second}, 0)
			} else {
				a.pl.PlayTrack([]music.Track{first, second}, 0)
			}
			until(t, ch, func() bool { return a.pl.Err() != "" })
			settled(tt)
			if tc.name == "sign-in" && a.signIn.open {
				// 首次失败弹出的登录框关闭后，持久播放详情里仍可再次打开登录入口。
				if err := tt.Click("Cancel"); err != nil {
					t.Fatal(err)
				}
				settled(tt)
			}

			if _, ok := tt.Find(secret); ok {
				t.Fatal("raw plugin error leaked into UI")
			}
			if _, ok := tt.Find(tc.wantTitle); !ok {
				t.Fatalf("missing failure summary %q in %q", tc.wantTitle, tt.Texts())
			}
			if tc.keepSingleItem && a.pl.Current().ID != "first" {
				t.Fatalf("blocked playback skipped to %q", a.pl.Current().ID)
			}
			// 单曲失败不影响继续浏览已加载来源内容。
			if a.library.home == nil || len(a.library.home.Sections[0].Tracks) != 1 {
				t.Fatal("single track failure corrupted loaded library content")
			}

			if err := tt.Click("Playback details"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("Copy playback diagnostics"); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(copied, secret) || !strings.Contains(copied, tc.wantCategory) {
				t.Fatalf("unexpected diagnostics %q", copied)
			}

			if tc.clickAction != "" {
				if err := tt.Click("Playback details"); err != nil {
					t.Fatal(err)
				}
				if err := tt.Click(tc.clickAction); err != nil {
					t.Fatal(err)
				}
				tc.checkAfter(t, a, a.library)
			}

			// 切歌后旧错误与旧重试代次必须失效。
			staleAttempt := a.pl.Feedback().Attempt
			a.pl.Next()
			tt.Frame()
			a.pl.RetryCurrent(staleAttempt)
			if got := requests.Load(); got != 2 {
				t.Fatalf("expected 2 requests (first + next), got %d after stale retry", got)
			}
		})
	}
}

// 成功播放后播放页展示实际来源与可确认音质，音质未知时明确标为未知。
func TestNowPlayingShowsActualSourceAndUnknownQuality(t *testing.T) {
	a := newTestApp()
	a.pl = player.New(a.post)
	a.pl.Source = a.sourceByID
	a.settings.Quality = "lossless"
	tr := song("one", "Track One")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.nowPlaying = true

	var copied string
	oldCopy := copyText
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = oldCopy }()

	tt := ui.NewTester(a.view, 1100, 760)
	settled(tt)
	a.pl.SetStream(&music.Stream{URL: "https://secret.example/audio?token=xyz", Type: "mp3", Platform: "wy", Quality: "320k"})
	tt.Frame()
	if _, ok := tt.Find("320k"); !ok && func() bool {
		for _, text := range tt.Texts() {
			if strings.Contains(text, "320k") {
				return false
			}
		}
		return true
	}() {
		t.Fatalf("expected actual quality 320k in Now Playing, got %q", tt.Texts())
	}

	a.pl.SetStream(&music.Stream{URL: "https://secret.example/audio?token=xyz", Type: "mp3", Quality: ""})
	tt.Frame()
	foundUnknown := false
	for _, text := range tt.Texts() {
		if strings.Contains(text, "Quality unknown") {
			foundUnknown = true
			break
		}
	}
	if !foundUnknown {
		t.Fatalf("expected explicit Quality unknown badge, got %q", tt.Texts())
	}
	if err := tt.Click("Playback details"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Copy playback diagnostics"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(copied, "token=xyz") || !strings.Contains(copied, "requested_quality=lossless") || !strings.Contains(copied, "actual_quality=unknown") {
		t.Fatalf("unexpected diagnostics for unknown quality: %q", copied)
	}
}

// 停用、注销或移除队列位置都会令旧的失败反馈和动作立即失效。
func TestSourceInvalidationClearsPlaybackFeedbackAndActions(t *testing.T) {
	t.Run("disable-source", func(t *testing.T) {
		a := newTestApp()
		addFake(a, newFakeSource("other"))
		ch := posts(a)
		a.pl = player.New(a.post)
		a.pl.Source = a.sourceByID
		fake(a).stream = func(_, _ string) (*music.Stream, error) {
			return nil, music.ErrUnavailable
		}
		tr := song("one", "Track One")
		tr.Source = "fake"
		a.pl.PlayQueue([]music.Track{tr}, 0)
		until(t, ch, func() bool { return a.pl.Err() != "" })
		staleAttempt := a.pl.Feedback().Attempt

		a.setPluginEnabled(a.libOf("fake"), false)
		if fb := a.pl.Feedback(); fb.Failure != "" || a.pl.Err() != "" {
			t.Fatalf("disabling source kept stale failure: %+v %q", fb, a.pl.Err())
		}
		a.pl.RetryCurrent(staleAttempt)
		if a.pl.Loading() {
			t.Fatal("stale retry started loading after source was disabled")
		}
	})

	t.Run("sign-out", func(t *testing.T) {
		a := newTestApp()
		ch := posts(a)
		a.pl = player.New(a.post)
		a.pl.Source = a.sourceByID
		a.account = &music.Account{ID: "u1", Name: "Listener"}
		a.accountAsked = true
		fake(a).account = func() (*music.Account, error) { return nil, nil }
		fake(a).signOut = func() error { return nil }
		fake(a).stream = func(_, _ string) (*music.Stream, error) {
			return nil, music.ErrUnavailable
		}
		tr := song("one", "Track One")
		tr.Source = "fake"
		a.pl.PlayQueue([]music.Track{tr}, 0)
		until(t, ch, func() bool { return a.pl.Err() != "" })
		staleAttempt := a.pl.Feedback().Attempt

		a.signOut(a.library)
		until(t, ch, func() bool { return a.account == nil })
		if fb := a.pl.Feedback(); fb.Failure != "" || a.pl.Err() != "" {
			t.Fatalf("signing out kept stale failure: %+v %q", fb, a.pl.Err())
		}
		a.pl.RetryCurrent(staleAttempt)
		if a.pl.Loading() {
			t.Fatal("stale retry started loading after sign out")
		}
	})

	t.Run("remove-queue-item", func(t *testing.T) {
		a := newTestApp()
		ch := posts(a)
		a.pl = player.New(a.post)
		a.pl.Source = a.sourceByID
		fake(a).stream = func(_, _ string) (*music.Stream, error) {
			return nil, music.ErrUnavailable
		}
		tr := song("one", "Track One")
		tr.Source = "fake"
		a.pl.PlayQueue([]music.Track{tr}, 0)
		until(t, ch, func() bool { return a.pl.Err() != "" })
		staleAttempt := a.pl.Feedback().Attempt

		a.pl.RemoveQueued(a.pl.QueueEntryID(0))
		if fb := a.pl.Feedback(); fb.Failure != "" || a.pl.Err() != "" {
			t.Fatalf("removing queued item kept stale failure: %+v %q", fb, a.pl.Err())
		}
		a.pl.RetryCurrent(staleAttempt)
		if a.pl.Loading() {
			t.Fatal("stale retry started loading after queue item removal")
		}
	})
}

// 键盘用户可以通过 Tab、Enter 和 Escape 打开并关闭播放详情弹窗。
func TestPlaybackDetailsKeyboardNavigation(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	a.pl = player.New(a.post)
	a.pl.Source = a.sourceByID
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return nil, music.ErrUnavailable
	}
	tr := song("one", "Keyboard Song")
	tr.Source = "fake"
	a.pl.PlayQueue([]music.Track{tr}, 0)
	until(t, ch, func() bool { return a.pl.Err() != "" })

	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.playerBar(c.Key("bar"))
	}, 920, 360)
	settled(tt)
	opened := false
	for range tt.AccessAll() {
		tt.Key(0, ui.KeyTab)
		if info, ok := tt.Access("Playback details"); ok && info.Focused {
			tt.Key(0, ui.KeyEnter)
			settled(tt)
			opened = a.playbackDetailsOpen
			break
		}
	}
	if !opened {
		t.Fatal("could not focus and open Playback details via keyboard")
	}
	if _, ok := tt.Find("Copy playback diagnostics"); !ok {
		t.Fatal("diagnostics action missing after keyboard open")
	}
	tt.Key(0, ui.KeyEscape)
	settled(tt)
	if a.playbackDetailsOpen {
		t.Fatal("Escape did not close Playback details")
	}
	_ = time.Millisecond
}

// 设置 MM_FRAMES 后，生成音源设置、播放栏失败详情与播放页音质反馈的浅色/深色/窄窗口截图。
func TestSourceAndPlaybackFeedbackFrames(t *testing.T) {
	dir := framesDir(t)

	// 1. 音源设置（能力面板展开 + 网络/程序权限 + 多态摘要，浅色与深色）
	for _, dark := range []bool{false, true} {
		a := newTestApp()
		remote := newFakeSource("Remote Player")
		remote.info.Playback = music.PlaybackRemote
		remote.caps["stream"] = false
		remoteLib := addFake(a, remote)
		remoteLib.account = &music.Account{ID: "r1", Name: "Alex"}
		remoteLib.accountAsked = true

		guardedOn := &guardedSource{fakeSource: newFakeSource("Stream Plugin")}
		guardedOn.perms.Network = []string{"api.example.com"}
		guardedOn.perms.Process = plugin.Programs{"helper"}
		onLib := addFake(a, guardedOn)
		onLib.needsSignIn = true
		a.capabilitiesOpen = map[string]bool{onLib.id: true}

		guardedOff := &guardedSource{fakeSource: newFakeSource("Disabled Plugin")}
		guardedOff.perms.Network = []string{"cdn.example.com"}
		offLib := newLibrary(guardedOff)

		tt := ui.NewTester(func(c *ui.Context) {
			a.pal = paletteFor(c.Theme())
			a.pluginSettings(c.Key("on"), onLib, a.pal)
			a.pluginSettings(c.Key("remote"), remoteLib, a.pal)
			a.pluginSettings(c.Key("off"), offLib, a.pal)
		}, 760, 720)
		tt.SetDark(dark)
		settled(tt)
		name := "p0-source-settings-light.png"
		if dark {
			name = "p0-source-settings-dark.png"
		}
		saveFrame(t, dir, name, tt.Image())
	}

	// 2. 播放栏失败详情弹窗（浅色窄窗口 & 深色桌面窗口）
	for _, tc := range []struct {
		name string
		w, h int
		dark bool
	}{
		{"p0-playback-details-narrow-light.png", 680, 520, false},
		{"p0-playback-details-dark.png", 1080, 720, true},
	} {
		a := newTestApp()
		ch := posts(a)
		a.pl = player.New(a.post)
		a.pl.Source = a.sourceByID
		a.settings.Quality = "lossless"
		fake(a).stream = func(_, _ string) (*music.Stream, error) {
			return nil, music.ErrSignIn
		}
		tr := song("one", "Midnight City", "M83")
		tr.Source = "fake"
		a.pl.PlayTrack([]music.Track{tr}, 0)
		until(t, ch, func() bool { return a.pl.Err() != "" })
		tt := ui.NewTester(a.view, tc.w, tc.h)
		tt.SetDark(tc.dark)
		settled(tt)
		_ = tt.Click("Playback details")
		settled(tt)
		saveFrame(t, dir, tc.name, tt.Image())
	}

	// 3. 播放页实际来源与未知音质反馈
	a := newTestApp()
	a.pl = player.New(a.post)
	a.pl.Source = a.sourceByID
	a.settings.Quality = "lossless"
	tr := song("one", "Northern Lights", "The Polar Lights")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.nowPlaying = true
	tt := ui.NewTester(a.view, 960, 700)
	tt.SetDark(true)
	a.pl.SetStream(&music.Stream{URL: "https://127.0.0.1/audio", Type: "mp3", Platform: "wy", Quality: ""})
	settled(tt)
	_ = tt.Click("Playback details")
	settled(tt)
	saveFrame(t, dir, "p0-nowplaying-quality-unknown.png", tt.Image())
}

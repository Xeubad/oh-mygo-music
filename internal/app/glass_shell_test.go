package app

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// patternedCover 生成带高光白块与几何图案的封面图，用于检验清晰封面沉浸背景下的文字对比度。
func patternedCover() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 480, 480))
	for y := range 480 {
		for x := range 480 {
			// 高对比明暗分区：右侧偏亮白光斑，左下部偏深蓝紫，模拟极端明暗封面。
			r := uint8(60 + 185*x/480)
			g := uint8(95 + 145*y/480)
			b := uint8(165 + 80*(480-y)/480)
			if x > 260 && y > 100 && y < 320 {
				r, g, b = 242, 240, 234
			}
			if (x-140)*(x-140)+(y-160)*(y-160) < 95*95 {
				r, g, b = 235, 118, 92
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img
}

// 浏览页封面氛围与正在播放页背景使用独立缓存，切歌或打开关闭播放页不能互相踩踏且静止零帧。
func TestBrowseAndNowPlayingBackdropsStayIsolatedAndIdle(t *testing.T) {
	a := newTestApp()
	first := song("1", "迷星叫", "MyGO!!!!!")
	first.Album.ImageURL = "cover-first"
	second := song("2", "春日影", "CRYCHIC")
	second.Album.ImageURL = "cover-second"
	plain := song("3", "无封面曲目", "Local")

	giveCover(a, "cover-first", coverImage(ui.RGB(42, 118, 134), ui.RGB(210, 178, 120)))
	giveCover(a, "cover-second", patternedCover())
	a.pl.Cue([]music.Track{first, second, plain})
	a.pl.Hold(10 * time.Second)

	tt := ui.NewTester(a.view, 1180, 780)
	settled(tt)
	if a.browseBackdrop.art == nil || a.backdropCache.canvas != nil {
		t.Fatalf("unexpected initial backdrop caches: browse=%v nowPlaying=%v", a.browseBackdrop.art != nil, a.backdropCache.canvas != nil)
	}
	browseFirst := a.browseBackdrop.art

	// 打开正在播放页并切换到下一首：播放页应建立独立画布。
	a.nowPlaying = true
	settled(tt)
	if a.backdropCache.canvas == nil {
		t.Fatal("now playing backdrop canvas was not initialized")
	}
	a.pl.Next()
	tt.Frame()
	if a.backdropCache.before == nil {
		t.Fatal("now playing did not fade between covers")
	}
	tt.Advance(backdropFade + 100*time.Millisecond)
	tt.Frame()
	if a.backdropCache.before != nil {
		t.Fatal("now playing fade did not settle")
	}

	// 关闭正在播放页回到浏览页：浏览页独立刷新为第二首封面并在过渡后静止。
	a.nowPlaying = false
	settled(tt)
	if a.browseBackdrop.art == nil || a.browseBackdrop.art == browseFirst {
		t.Fatal("browse backdrop did not update independently to second cover")
	}
	if a.browseBackdrop.before != nil {
		t.Fatal("browse backdrop did not finish fading")
	}
	if n := framesAt(a, tt, 10*time.Second); n > 1 {
		t.Fatalf("browse page with cover wash requested %d frames while paused/held", n)
	}

	// 切到无封面歌曲时，浏览页氛围应主动清空回纯底色，不残留上一首带封面的氛围。
	a.pl.Next()
	settled(tt)
	if a.browseBackdrop.art != nil || a.browseBackdrop.before != nil {
		t.Fatalf("expected browse backdrop to clear on coverless track, got art=%v before=%v", a.browseBackdrop.art != nil, a.browseBackdrop.before != nil)
	}
}

// 浅色主题关闭透明效果时，模糊层不能用白底覆盖歌词和律动舞台的深色背景。
func TestNowPlayingBackgroundStaysDarkWithoutTransparency(t *testing.T) {
	a, tester, _ := playingApp()
	a.nowPlaying = true
	a.pl.PauseForSleep(nil)
	tester.SetDark(false)
	tester.SetPreferences(ui.Preferences{ReduceTransparency: true})
	for _, tab := range []struct {
		name string
		pane int
	}{{"Lyrics", paneLyrics}, {"Stage", paneStage}} {
		t.Run(tab.name, func(t *testing.T) {
			a.npPane = tab.pane
			settled(tester)
			// 采样避开封面、歌词和控件，落在原先变白的背景区域。
			background := tester.Image().RGBAAt(1100, 300)
			if background.R >= 160 || background.G >= 160 || background.B >= 160 {
				t.Fatalf("background is too light behind white text: %v", background)
			}
		})
	}
}

// 关闭新背景会更新两处视图，重启时仍按保存的选择显示。
func TestCoverBackdropSwitchUpdatesViewsAndSurvivesRestart(t *testing.T) {
	a := newTestApp()
	if err := json.Unmarshal([]byte(`{"volume":0.5}`), &a.settings); err != nil {
		t.Fatal(err)
	}
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	track := song("1", "Cover song")
	track.Album.ImageURL = "cover:backdrop-setting"
	giveCover(a, track.Album.ImageURL, patternedCover())
	a.pl.Cue([]music.Track{track})

	main := ui.NewTester(a.view, 980, 680)
	main.SetPreferences(ui.Preferences{ReduceMotion: true})
	toggleNowPlaying := func(open bool) {
		a.nowPlaying = open
		settled(main)
	}
	toggleNowPlaying(false)
	if a.browseBackdrop.art == nil {
		t.Fatal("an older settings file lost the browsing cover glow")
	}
	toggleNowPlaying(true)
	if !a.backdropCache.hasBmp {
		t.Fatal("an older settings file lost the full cover background")
	}

	prefs := ui.NewTester(a.settingsView, 900, 900)
	settled(prefs)
	clickSettingRole(t, prefs, ui.RoleSwitch, T("Cover Backdrop"))
	prefs.Frame()
	toggleNowPlaying(true)
	if a.backdropCache.hasBmp {
		t.Fatal("Now Playing kept the full cover after switching it off")
	}
	toggleNowPlaying(false)
	if a.browseBackdrop.art != nil || a.browseBackdrop.before != nil {
		t.Fatal("browsing kept a cover glow after switching it off")
	}
	saved, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CoverBackdrop {
		t.Fatal("the saved setting turned the cover background on again")
	}

	clickSettingRole(t, prefs, ui.RoleSwitch, T("Cover Backdrop"))
	prefs.Frame()
	toggleNowPlaying(false)
	if a.browseBackdrop.art == nil {
		t.Fatal("browsing did not restore the cover glow")
	}
	toggleNowPlaying(true)
	if !a.backdropCache.hasBmp {
		t.Fatal("Now Playing did not restore the full cover")
	}
}

// 预设和滑杆应立即更新封面背景，重新读取设置时保留自定义值。
func TestCoverBackdropStylePreviewsAndSaves(t *testing.T) {
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	track := song("1", "Cover song")
	track.Album.ImageURL = "cover:tune-backdrop"
	giveCover(a, track.Album.ImageURL, patternedCover())
	a.pl.Cue([]music.Track{track})
	a.nowPlaying = true

	main := ui.NewTester(a.view, 900, 620)
	main.SetPreferences(ui.Preferences{ReduceMotion: true, ReduceTransparency: true})
	prefs := ui.NewTester(a.settingsView, 780, 800)
	settled(prefs)
	sample := func() color.RGBA {
		settled(main)
		return main.Image().RGBAAt(840, 210)
	}
	initial := sample()
	clickSettingRole(t, prefs, ui.RoleButton, T("Dark"))
	if darker := sample(); int(darker.R)+int(darker.G)+int(darker.B) >= int(initial.R)+int(initial.G)+int(initial.B) {
		t.Fatalf("dark preset did not dim the cover: %v → %v", initial, darker)
	}

	blurSlider, ok := prefs.AccessByRole(ui.RoleSlider, T("Backdrop Blur"))
	if !ok {
		t.Fatal("blur slider is unavailable")
	}
	beforeBlur := sample()
	bounds := blurSlider.Bounds
	prefs.Press(bounds.X+30, bounds.Y+bounds.H/2)
	prefs.Move(bounds.X+8, bounds.Y+bounds.H/2)
	prefs.Frame()
	if a.settings.CoverBackdropStyle.Blur >= 70 || sample() == beforeBlur {
		t.Fatal("dragging blur did not update the playing page")
	}
	prefs.Release(bounds.X+8, bounds.Y+bounds.H/2)
	prefs.Frame()
	saved, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restored := defaultSettings()
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CoverBackdropStyle != a.settings.CoverBackdropStyle {
		t.Fatalf("custom style was not saved: got %+v, want %+v", restored.CoverBackdropStyle, a.settings.CoverBackdropStyle)
	}

	clickSettingRole(t, prefs, ui.RoleButton, T("Reset"))
	if sample() != initial {
		t.Fatal("reset did not restore the original background")
	}
}

// 顶部悬浮玻璃导航胶囊可跨页面返回/前进，且列表滚动到底部时末行不被悬浮播放胶囊永久遮挡。
func TestFloatingNavPillAndBottomScrollClearance(t *testing.T) {
	a := newTestApp()
	tracks := make([]music.Track, 40)
	for i := range tracks {
		tracks[i] = song(fmt.Sprint(i+1), fmt.Sprintf("曲目-%02d", i+1), "The Polar Lights")
	}
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "长歌单", Tracks: tracks}

	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tt)
	if _, ok := tt.Find("Back"); ok {
		t.Fatal("back button should not appear without navigation history")
	}

	a.open(route{page: pagePlaylist, src: "fake", id: "42"})
	settled(tt)
	backBtn, ok := tt.Find("Back")
	if !ok {
		t.Fatal("floating nav capsule back button missing after navigation")
	}
	if backBtn.X < sidebarWidth {
		t.Fatalf("floating nav capsule should sit in content area right of sidebar, got x=%v", backBtn.X)
	}

	// 滚动到底部，确认最后一首能露出在悬浮播放条上方（Y+H <= 780-playerBarHeight）。
	tt.Scroll(650, 420, 0, 20000)
	settled(tt)
	lastRow, ok := tt.Find("曲目-40")
	if !ok {
		t.Fatal("last track row not visible after scrolling to bottom")
	}
	if lastRow.Y+lastRow.H > 780-playerBarHeight {
		t.Fatalf("last row covered by floating player capsule: row=[%v,%v], limit=%d", lastRow.Y, lastRow.Y+lastRow.H, 780-playerBarHeight)
	}

	if err := tt.Click("Back"); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	if a.route.page != pageHome {
		t.Fatalf("clicking floating Back capsule navigated to %+v, want Home", a.route)
	}
}

// 设有 MM_FRAMES 时，生成带极端高亮图案封面的浅/深色浏览页（含悬浮返回胶囊）、沉浸歌词页与律动页截图供目视审查。
func TestGlassShellAndImmersiveContrastFrames(t *testing.T) {
	dir := framesDir(t)
	prevLang := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	t.Cleanup(func() { i18n.SetLanguage(prevLang) })

	a := newTestApp()
	a.account = &music.Account{ID: "1", Name: "Alex"}
	tracks := framesTracks(a)
	giveCover(a, "cover:bright", patternedCover())
	tracks[0].Album.ImageURL = "cover:bright"
	a.playlists["10"] = &music.Playlist{ID: "10", Name: "高对比测试歌单", Owner: "Alex", ImageURL: "cover:bright", Mine: true, Tracks: tracks}
	a.pl.Cue(tracks)
	a.pl.SetLyrics(lyrics.Build(
		"[00:04.00]晴光穿透玻璃边缘\n[00:09.00]即便亮白背景也保持可读\n[00:14.00]渐进模糊与暗色护底\n[00:19.00]让每行歌词自然浮现",
		"",
	))
	a.pl.Hold(10 * time.Second)

	tt := ui.NewTester(a.view, 1200, 800)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	a.open(route{page: pagePlaylist, src: "fake", id: "10"})

	tt.SetDark(false)
	settled(tt)
	saveFrame(t, dir, "glass-browse-nav-light.png", tt.Image())

	tt.SetDark(true)
	settled(tt)
	saveFrame(t, dir, "glass-browse-nav-dark.png", tt.Image())

	a.nowPlaying, a.npPane = true, paneLyrics
	tt.Move(600, 200)
	settled(tt)
	saveFrame(t, dir, "glass-immersive-bright-cover.png", tt.Image())

	a.npPane = paneStage
	settled(tt)
	saveFrame(t, dir, "glass-stage-pills.png", tt.Image())
}

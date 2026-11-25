package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 歌词显示胶囊与律动胶囊同样有悬停与按压反馈，且结束后不会持续请求重绘。
func TestLyricDisplayGlassPillFeedbackSettles(t *testing.T) {
	a, tester, _ := playingApp()
	a.pl.SetLyrics(lyrics.Build("[00:01]one line", "[00:01]一行译文"))
	a.nowPlaying, a.npPane = true, paneLyrics
	settled(tester)
	hoverChangesGlass(t, tester, "Lyrics Display")
	pressThenSettle(t, tester, "Lyrics Display")
	if count := framesAt(a, tester, 0); count > 1 {
		t.Fatalf("idle Lyrics Display requested %d frames per second after hover/press", count)
	}
}

// 从播放页修改显示选择后，重建应用仍按保存值显示，原始歌词不被覆盖。
func TestLyricDisplayPreferencesSurviveRestart(t *testing.T) {
	makePlayer := func(a *app) *ui.Tester {
		a.pl.Cue([]music.Track{{ID: "display", Source: "fake", Name: "A song"}})
		a.pl.SetLyrics(lyrics.Build("[00:01]風景\n[00:09]國際風景", "[00:09]a view"))
		a.pl.Hold(2 * time.Second)
		a.nowPlaying, a.npPane = true, paneLyrics
		tester := ui.NewTester(a.view, 1200, 800)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		settled(tester)
		return tester
	}
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tester := makePlayer(a)
	for _, option := range []string{"Show Translation", "Simplified Chinese", "Treat Lyrics as Chinese"} {
		if err := tester.Click("Lyrics Display"); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem(option); err != nil {
			t.Fatal(err)
		}
		tester.Frame()
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newTestApp()
	if err := json.Unmarshal(data, &restarted.settings); err != nil {
		t.Fatal(err)
	}
	shown := makePlayer(restarted)
	if shown.HasText("a view") || !shown.HasText("国际风景") {
		t.Fatalf("restart lost the display choices: %q", shown.Texts())
	}
	if source, _ := restarted.pl.Lyrics(); source[1].Text != "國際風景" {
		t.Fatal("source lyrics were overwritten")
	}
}

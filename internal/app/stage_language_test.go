package app

import (
	"encoding/json"
	"image"

	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 律动可独立选择译文并跨重启保留，不改原词、播放进度或其他页面的翻译选择。
func TestStageLanguageUsesTranslationAndSurvivesRestart(t *testing.T) {
	makeView := func(a *app) *ui.Tester {
		a.pl.Cue([]music.Track{{ID: "stage-language", Source: "fake", Name: "Language choices"}})
		a.pl.SetLyrics(lyrics.Build("[00:01]風が通り過ぎる\n[00:09]次の景色", "[00:01]讓風慢慢經過\n[00:09]我們一起出發"))
		a.pl.Hold(2 * time.Second)
		a.nowPlaying, a.npPane = true, paneStage
		tester := ui.NewTester(a.view, 1200, 800)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		tester.Move(600, 400)
		settled(tester)
		return tester
	}
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.settings.LyricsScript = "simplified"
	a.settings.HidePlaybackTranslation, a.settings.HideCardTranslation = true, true
	tester := makeView(a)
	source, _ := a.pl.Lyrics()
	expected := lyrics.Build("[00:01]風が通り過ぎる\n[00:09]次の景色", "[00:01]讓風慢慢經過\n[00:09]我們一起出發")
	if err := tester.Click("Stage Language"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Translated Lyrics"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	visible := strings.Join(tester.Texts(), "")
	if !strings.Contains(visible, "让") || !strings.Contains(visible, "经") || strings.Contains(visible, "が") || !tester.HasText("我们一起出发") {
		t.Fatalf("translated mode did not replace the primary lyrics: %q", tester.Texts())
	}
	if err := tester.Click("Lyrics Display"); err == nil {
		t.Fatal("stage still opens the general lyric display settings")
	}
	if !a.settings.HidePlaybackTranslation || !a.settings.HideCardTranslation {
		t.Fatal("stage language changed another page's translation choice")
	}
	if !reflect.DeepEqual(source, expected) || a.pl.Position() != 2*time.Second || a.pl.Current().ID != "stage-language" {
		t.Fatal("changing stage language modified the source or playback")
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newTestApp()
	if err := json.Unmarshal(data, &restarted.settings); err != nil {
		t.Fatal(err)
	}
	shown := makeView(restarted)
	visible = strings.Join(shown.Texts(), "")
	if !strings.Contains(visible, "让") || !strings.Contains(visible, "经") || strings.Contains(visible, "が") || !shown.HasText("我们一起出发") {
		t.Fatalf("restart lost the stage language: %q", shown.Texts())
	}
	for _, choice := range []string{"Original Lyrics", "Bilingual"} {
		if err := shown.Click("Stage Language"); err != nil {
			t.Fatal(err)
		}
		if err := shown.ChooseMenuItem(choice); err != nil {
			t.Fatal(err)
		}
		shown.Frame()
		visible = strings.Join(shown.Texts(), "")
		if !strings.Contains(visible, "が") || shown.HasText("让风慢慢经过") != (choice == "Bilingual") {
			t.Fatalf("language %s did not update primary and translated lyrics: %q", choice, shown.Texts())
		}
	}
	if err := shown.Click("Lyrics"); err != nil {
		t.Fatal(err)
	}
	shown.Frame()
	if err := shown.Click("Lyrics Display"); err != nil {
		t.Fatal("normal lyrics lost their own display settings")
	}
	shown.CloseMenu()
	if shown.HasText("让风慢慢经过") {
		t.Fatal("stage choice changed the normal lyrics translation preference")
	}
}

// 译文仅借用原行时间生成显示动效，缺译行回退原词，所有模式都不覆盖来源的逐字时间。
func TestStageLanguageKeepsSourceTimingAndFallsBack(t *testing.T) {
	a := newTestApp()
	track := &music.Track{ID: "timing", Source: "fake"}
	source := []lyrics.Line{
		{At: time.Second, End: 5 * time.Second, Text: "hello world", Translation: "我们一起出发",
			Words: []lyrics.Word{{Text: "hello ", Start: time.Second, End: 1200 * time.Millisecond}, {Text: "world", Start: 1200 * time.Millisecond, End: 5 * time.Second}}},
		{At: 10 * time.Second, End: 13 * time.Second, Text: "original fallback", Translation: " \t ",
			Words: []lyrics.Word{{Text: "original fallback", Start: 10 * time.Second, End: 13 * time.Second}}},
	}
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	a.settings.HidePlaybackTranslation = true
	a.settings.StageLanguage = "translation"
	shown := a.stageLyrics(track, source)
	if shown[0].Text != "我们一起出发" || shown[0].Translation != "" || shown[0].At != time.Second || shown[0].End != 5*time.Second {
		t.Fatalf("translation changed its line interval: %+v", shown[0])
	}
	var text strings.Builder
	for _, word := range shown[0].Words {
		text.WriteString(word.Text)
		if word.Start < time.Second || word.End > 5*time.Second || word.End < word.Start {
			t.Fatalf("translated animation escaped the source line: %+v", word)
		}
	}
	if text.String() != "我们一起出发" || len(shown[0].Words) == 0 || shown[0].Words[0].Start != time.Second {
		t.Fatalf("translation inherited the original word text or timing: %+v", shown[0].Words)
	}
	if shown[1].Text != source[1].Text || shown[1].Translation != "" || !reflect.DeepEqual(shown[1].Words, source[1].Words) {
		t.Fatalf("missing translation did not fall back: %+v", shown[1])
	}
	a.settings.StageLanguage = "original"
	shown = a.stageLyrics(track, source)
	if shown[0].Text != source[0].Text || shown[0].Translation != "" || !reflect.DeepEqual(shown[0].Words, source[0].Words) {
		t.Fatalf("original mode did not restore the source words: %+v", shown[0])
	}
	a.settings.StageLanguage = ""
	shown = a.stageLyrics(track, source)
	if shown[0].Text != source[0].Text || shown[0].Translation != source[0].Translation {
		t.Fatal("bilingual mode followed the normal page's hidden translation")
	}
	after, err := json.Marshal(source)
	if err != nil || string(before) != string(after) {
		t.Fatal("language selection mutated the source lyrics")
	}
}

// 导出语言选择和各律动样式的译文帧，人工检查排版而不固定像素快照。
func TestStageLanguageFrames(t *testing.T) {
	directory := framesDir(t)
	a, tester, _ := playingApp()
	a.pl.Current().Name = "春の風 · Spring Breeze"
	a.pl.SetLyrics(lyrics.Build("[00:01]風が通り過ぎる\n[00:09]次の景色", "[00:01]让风慢慢经过\n[00:09]我们一起出发"))
	a.pl.Hold(4 * time.Second)
	a.nowPlaying, a.npPane = true, paneStage
	tester.SetDark(true)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Move(600, 400)
	settled(tester)
	for _, language := range []string{"original", "translation", ""} {
		a.settings.StageLanguage = language
		tester.Frame()
		name := language
		if name == "" {
			name = "bilingual"
		}
		saveFrame(t, directory, "stage-language-"+name+".png", tester.Image())
	}
	a.settings.StageLanguage = "translation"
	for _, style := range []string{"partita", "tilt", "spotlight", "wave"} {
		a.settings.StageStyle = style
		tester.Frame()
		saveFrame(t, directory, "stage-translation-"+style+".png", tester.Image())
	}
	a.settings.StageStyle = "spotlight"
	compact := ui.NewTester(a.view, 900, 640)
	compact.SetDark(true)
	compact.SetPreferences(ui.Preferences{ReduceMotion: true})
	compact.Move(450, 320)
	settled(compact)
	saveFrame(t, directory, "stage-language-compact.png", compact.Image())
}

func cloneRGBA(source *image.RGBA) *image.RGBA {
	copy := image.NewRGBA(source.Bounds())
	copy.Pix = append(copy.Pix[:0], source.Pix...)
	return copy
}

func imagesDifferWithin(before, after *image.RGBA, box ui.Rect) bool {
	return changedPixels(before, after, box) != 0
}

func changedPixels(before, after *image.RGBA, box ui.Rect) int {
	if before.Bounds() != after.Bounds() {
		return -1
	}
	region := image.Rect(int(box.X), int(box.Y), int(box.X+box.W), int(box.Y+box.H)).Intersect(before.Bounds())
	changed := 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if before.RGBAAt(x, y) != after.RGBAAt(x, y) {
				changed++
			}
		}
	}
	return changed
}

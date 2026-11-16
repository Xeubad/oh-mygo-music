package lyrics_test

import (
	"reflect"
	"testing"
	"time"

	"mygo-music/internal/lyrics"
)

// 简体显示同时转换整句与逐字内容，保留真实时间和来源文本。
func TestDisplayPreservesSourceAndTiming(t *testing.T) {
	source := []lyrics.Line{{At: time.Second, End: 5 * time.Second, Text: "我們一起聽見風", Translation: "讓我們一起聽見風",
		Words: []lyrics.Word{{Text: "我們", Start: time.Second, End: 2 * time.Second}, {Text: "一起聽見風", Start: 3 * time.Second, End: 5 * time.Second}}}}
	shown := lyrics.Display(source, lyrics.DisplayOptions{Simplify: true})
	if shown[0].Text != "我们一起听见风" || shown[0].Translation != "让我们一起听见风" || shown[0].Words[1].Text != "一起听见风" {
		t.Fatalf("display: %+v", shown[0])
	}
	if shown[0].Words[1].Start != 3*time.Second || shown[0].Words[1].End != 5*time.Second || shown[0].At != time.Second {
		t.Fatal("display changed the timing")
	}
	if source[0].Text != "我們一起聽見風" || source[0].Words[1].Text != "一起聽見風" {
		t.Fatal("display changed the source")
	}
	if restored := lyrics.Display(source, lyrics.DisplayOptions{}); !reflect.DeepEqual(restored, source) {
		t.Fatal("turning simplification off did not restore the source")
	}
}

// 简体显示应转换来源译文中的繁体词组，即使该短句本身没有原词判据。
func TestDisplayConvertsTraditionalTranslation(t *testing.T) {
	source := []lyrics.Line{{At: time.Second, End: 2 * time.Second, Text: "Hello", Translation: "臺北的夜"}}
	shown := lyrics.Display(source, lyrics.DisplayOptions{Simplify: true})
	if shown[0].Translation != "台北的夜" {
		t.Fatalf("translation %q, want 台北的夜", shown[0].Translation)
	}
}

// 一首歌由可靠中文词组识别后，其余繁体歌词行也应统一简体显示。
func TestDisplayUsesSongLanguageEvidence(t *testing.T) {
	source := []lyrics.Line{
		{Text: "我們一起聽見風"},
		{Text: "風景照亮遠方"},
		{Text: "風景照亮遠方", Translation: "臺北的夜"},
	}
	shown := lyrics.Display(source, lyrics.DisplayOptions{Simplify: true})
	if shown[1].Text != "风景照亮远方" || shown[2].Translation != "台北的夜" {
		t.Fatalf("song language evidence was not shared across lines: %+v", shown)
	}
}

// 语言不明和日文原词保留来源文本，日文歌的中文译文仍可单独简化。
func TestDisplayPreservesUnknownAndJapaneseLyrics(t *testing.T) {
	unknown := []lyrics.Line{{Text: "風景照亮遠方"}}
	if shown := lyrics.Display(unknown, lyrics.DisplayOptions{Simplify: true}); shown[0].Text != unknown[0].Text {
		t.Fatalf("ambiguous Han text changed to %q", shown[0].Text)
	}
	japanese := []lyrics.Line{{Text: "風景の中", Translation: "讓我看見遠方"}, {Text: "光が揺れる"}}
	shown := lyrics.Display(japanese, lyrics.DisplayOptions{Simplify: true})
	if shown[0].Text != japanese[0].Text || shown[0].Translation != "让我看见远方" {
		t.Fatalf("Japanese lyric and Chinese translation: %+v", shown[0])
	}
}

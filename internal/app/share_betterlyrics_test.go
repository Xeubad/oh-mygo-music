package app

import (
	"path/filepath"
	"strings"
	"testing"

	"mygo-music/internal/betterlyrics"
	"mygo-music/internal/music"
)

// 分享卡片应沿用 BetterLyrics 覆盖歌词，而不是重新展示来源返回的原始歌词。
func TestShareCardUsesBetterLyricsOverride(t *testing.T) {
	a := newTestApp()
	var sourceCalls int
	fake(a).lyrics = func(string) (*music.Lyrics, error) {
		sourceCalls++
		return &music.Lyrics{LRC: "[00:01.00]Original lyrics"}, nil
	}
	a.betterLyricsOverrides, _ = betterlyrics.NewOverrideStore(filepath.Join(t.TempDir(), "lyrics.json"))
	track := music.Track{ID: "shared", Source: "fake", Name: "Blue Hour", Duration: 12000}
	if err := a.betterLyricsOverrides.Put(a.betterLyricsSongKey(track), music.Lyrics{LRC: "[00:01.00]BetterLyrics override"}); err != nil {
		t.Fatal(err)
	}

	posted := posts(a)
	a.openShare(track)
	until(t, posted, func() bool { return !a.sharing.loading })

	if got := strings.TrimSpace(a.sharing.lines[0].Text); got != "BetterLyrics override" {
		t.Fatalf("share lyrics = %q, want BetterLyrics override", got)
	}
	if sourceCalls != 0 {
		t.Fatalf("source lyrics was requested %d times, want no request when an override exists", sourceCalls)
	}
}

// 已保存的 BetterLyrics 覆盖即使来源不提供歌词能力，也应进入分享卡片。
func TestShareCardUsesBetterLyricsOverrideWithoutSourceLyrics(t *testing.T) {
	a := newTestApp()
	without(a, "lyrics")
	a.betterLyricsOverrides, _ = betterlyrics.NewOverrideStore(filepath.Join(t.TempDir(), "lyrics.json"))
	track := music.Track{ID: "shared", Source: "fake", Name: "Blue Hour", Duration: 12000}
	if err := a.betterLyricsOverrides.Put(a.betterLyricsSongKey(track), music.Lyrics{LRC: "[00:01.00]BetterLyrics override"}); err != nil {
		t.Fatal(err)
	}

	posted := posts(a)
	a.openShare(track)
	until(t, posted, func() bool { return !a.sharing.loading })

	if len(a.sharing.lines) == 0 || strings.TrimSpace(a.sharing.lines[0].Text) != "BetterLyrics override" {
		t.Fatalf("share lyrics = %v, want BetterLyrics override", a.sharing.lines)
	}
}

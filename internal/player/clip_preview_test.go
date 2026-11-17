package player_test

import (
	"path/filepath"
	"testing"
	"time"

	"mygo-music/internal/player"
)

// 试听使用真实解码器并限制在选定区间，不需要播放声音或创建窗口。
func TestClipPreviewUsesOnlyTheRequestedDuration(t *testing.T) {
	path, err := filepath.Abs("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := player.OpenClipPreview(path, 500*time.Millisecond, 1250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	if preview.Duration() != 750*time.Millisecond {
		t.Fatalf("preview duration %v", preview.Duration())
	}
	if invalid, err := player.OpenClipPreview(path, time.Second, time.Second); err == nil {
		invalid.Close()
		t.Fatal("accepted an empty audition range")
	}
}

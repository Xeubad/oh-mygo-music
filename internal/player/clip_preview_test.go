package player

import (
	"io"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// 试听使用真实解码器并限制在选定区间，不需要播放声音或创建窗口。
func TestClipPreviewUsesOnlyTheRequestedDuration(t *testing.T) {
	path, err := filepath.Abs("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := OpenClipPreview(path, 500*time.Millisecond, 1250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	if preview.Duration() != 750*time.Millisecond {
		t.Fatalf("preview duration %v", preview.Duration())
	}
	if invalid, err := OpenClipPreview(path, time.Second, time.Second); err == nil {
		invalid.Close()
		t.Fatal("accepted an empty audition range")
	}
}

// 调整边界后解码器必须按新终点截断，且能从原终点之后继续读取。
func TestClipDecoderMovesItsEndpoint(t *testing.T) {
	download, err := openFile("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := openStream(download, 0)
	if err != nil {
		download.Close()
		t.Fatal(err)
	}
	defer decoded.Close()
	clip := &clipDecoder{stream: decoded}
	start := clipFrame(500*time.Millisecond, clip.SampleRate())
	if err := clip.SetPosition(start); err != nil {
		t.Fatal(err)
	}
	buffer := make([]float32, 64*clip.Channels())
	for _, length := range []int64{16, 32} {
		clip.limit.Store(clip.position + length)
		count, err := clip.Read(buffer)
		if err != nil || count != int(length)*clip.Channels() {
			t.Fatalf("read %d samples, want %d frames: %v", count, length, err)
		}
		if count, err := clip.Read(buffer); count != 0 || err != io.EOF {
			t.Fatalf("read past the clip end: %d samples, %v", count, err)
		}
	}
}

// 主播放器正在淡出时，试听必须立即静音并暂停它，试听音量沿用主播放器的增益曲线。
func TestClipPreviewTakesOverPlaybackVolume(t *testing.T) {
	mainOutput := &transitionTestOutput{playing: true}
	main := &Player{out: mainOutput, playing: true, volume: .4, gain: .9, Normalize: func() bool { return true }}
	main.fade.attach(mainOutput, 1)
	main.applyGain()
	preview, err := OpenClipPreview("testdata/seek.flac", 500*time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	preview.gain = main.gain
	preview.SetVolume(main.volume, true)
	if math.Abs(preview.output.Volume()-mainOutput.Volume()) > 1e-9 {
		t.Fatalf("preview gain %v, main gain %v", preview.output.Volume(), mainOutput.Volume())
	}
	main.PauseForPreview()
	if main.Playing() || mainOutput.Playing() || mainOutput.Volume() != 0 {
		t.Fatal("main output was not paused immediately")
	}
	preview.SetVolume(0, true)
	if preview.output.Volume() != 0 {
		t.Fatal("preview did not follow mute")
	}
}

package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFFmpegArgsCarryTheClipAndTheSandbox(t *testing.T) {
	transcoder := NewFFmpeg("/usr/bin/ffmpeg")
	args := transcoder.args(AudioInput{Path: "/tmp/in.m4a", Demuxer: "mov"}, &Clip{StartMs: 425, EndMs: 2125}, "/tmp/out.mp3", "libmp3lame")

	for _, want := range [][]string{
		{"-protocol_whitelist", "file,pipe"},
		{"-f", "mov"},
		{"-ss", "0.425"},
		{"-i", "/tmp/in.m4a"},
		{"-t", "1.700"},
		{"-map", "0:a:0"},
		{"-map_metadata", "-1"},
		{"-c:a", "libmp3lame"},
		{"-f", "mp3"},
	} {
		if !slices.ContainsFunc(windowsOf(args, len(want)), func(window []string) bool { return slices.Equal(window, want) }) {
			t.Errorf("args %v are missing %v", args, want)
		}
	}
	if last := args[len(args)-1]; last != "/tmp/out.mp3" {
		t.Errorf("last arg = %q, want the destination", last)
	}
}

func TestFFmpegFullSongArgsHaveNoSeek(t *testing.T) {
	transcoder := NewFFmpeg("/usr/bin/ffmpeg")
	args := transcoder.args(AudioInput{Path: "/tmp/in.mp3", Demuxer: "mp3"}, nil, "/tmp/out.mp3", "mp3")
	if slices.Contains(args, "-ss") {
		t.Errorf("full-song args contain a seek: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "-t 7200") {
		t.Errorf("full-song args lack the duration cap: %v", args)
	}
}

func windowsOf(values []string, size int) [][]string {
	if size <= 0 || len(values) < size {
		return nil
	}
	out := make([][]string, 0, len(values)-size+1)
	for i := 0; i+size <= len(values); i++ {
		out = append(out, values[i:i+size])
	}
	return out
}

func TestFFmpegRejectsUnknownContainers(t *testing.T) {
	transcoder := NewFFmpeg("/usr/bin/ffmpeg")
	for _, demuxer := range []string{"", "concat", "hls", "image2", "MP3", "matroska"} {
		err := transcoder.Transcode(context.Background(), AudioInput{Path: "/tmp/in", Demuxer: demuxer}, nil, "/tmp/out.mp3")
		if err == nil {
			t.Errorf("demuxer %q was accepted", demuxer)
		}
	}
}

func TestFFmpegWithoutBinaryFailsClosed(t *testing.T) {
	transcoder := NewFFmpeg("")
	if transcoder.Available() {
		t.Fatal("an empty path reports itself available")
	}
	if err := transcoder.Transcode(context.Background(), AudioInput{Path: "/tmp/in.mp3", Demuxer: "mp3"}, nil, "/tmp/out.mp3"); !errors.Is(err, ErrFFmpegUnavailable) {
		t.Errorf("err = %v, want ErrFFmpegUnavailable", err)
	}
}

func TestVerifyMP3AcceptsOnlyMP3Headers(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{"id3", "ID3\x04\x00\x00\x00\x00\x00\x00", true},
		{"frame sync", "\xff\xfb\x90\x00rest", true},
		{"empty", "", false},
		{"wav", "RIFF\x00\x00\x00\x00WAVE", false},
		{"short", "ID", false},
	}
	for _, testCase := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(testCase.name, " ", "-"))
		if err := os.WriteFile(path, []byte(testCase.content), 0o600); err != nil {
			t.Fatal(err)
		}
		err := verifyMP3(path)
		if testCase.wantOK && err != nil {
			t.Errorf("%s: %v", testCase.name, err)
		}
		if !testCase.wantOK && err == nil {
			t.Errorf("%s: accepted as MP3", testCase.name)
		}
	}
	if err := verifyMP3(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestLimitedBufferStopsAtTheLimit(t *testing.T) {
	buffer := &limitedBuffer{max: 4}
	if n, err := buffer.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("Write = (%d, %v), want (6, nil)", n, err)
	}
	if buffer.buf.String() != "abcd" {
		t.Errorf("buffer = %q, want %q", buffer.buf.String(), "abcd")
	}
}

func TestFFmpegTranscodesRealAudio(t *testing.T) {
	binaryPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "tone.wav")
	writeTestWAV(t, input, 1500*time.Millisecond)

	transcoder := NewFFmpeg(binaryPath)
	transcoder.Timeout = 30 * time.Second
	full := filepath.Join(dir, "full.mp3")
	if err := transcoder.Transcode(context.Background(), AudioInput{Path: input, Demuxer: "wav"}, nil, full); err != nil {
		t.Fatalf("full transcode: %v", err)
	}
	if info, err := os.Stat(full); err != nil || info.Size() == 0 {
		t.Fatalf("full output = %v, %v", info, err)
	}

	clip := filepath.Join(dir, "clip.mp3")
	if err := transcoder.Transcode(context.Background(), AudioInput{Path: input, Demuxer: "wav"}, &Clip{StartMs: 200, EndMs: 600}, clip); err != nil {
		t.Fatalf("clip transcode: %v", err)
	}
	clipInfo, err := os.Stat(clip)
	if err != nil || clipInfo.Size() == 0 {
		t.Fatalf("clip output = %v, %v", clipInfo, err)
	}
	if fullInfo, err := os.Stat(full); err == nil && clipInfo.Size() >= fullInfo.Size() {
		t.Errorf("clip is %d bytes, full is %d: the cut was not applied", clipInfo.Size(), fullInfo.Size())
	}
}

func writeTestWAV(t *testing.T, path string, duration time.Duration) {
	t.Helper()
	const sampleRate = 44100
	samples := int(sampleRate * duration.Seconds())
	var buffer bytes.Buffer
	buffer.WriteString("RIFF")
	writeLE(t, &buffer, uint32(36+samples*2))
	buffer.WriteString("WAVEfmt ")
	writeLE(t, &buffer, uint32(16))
	writeLE(t, &buffer, uint16(1))
	writeLE(t, &buffer, uint16(1))
	writeLE(t, &buffer, uint32(sampleRate))
	writeLE(t, &buffer, uint32(sampleRate*2))
	writeLE(t, &buffer, uint16(2))
	writeLE(t, &buffer, uint16(16))
	buffer.WriteString("data")
	writeLE(t, &buffer, uint32(samples*2))
	for i := 0; i < samples; i++ {
		value := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
		writeLE(t, &buffer, value)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLE(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := binary.Write(writer, binary.LittleEndian, value); err != nil {
		t.Fatal(err)
	}
}

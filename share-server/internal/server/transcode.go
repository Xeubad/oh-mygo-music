package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrFFmpegUnavailable is returned when the service cannot transcode because no
// ffmpeg binary was found. Uploads fail closed instead of publishing the source
// file untouched.
var ErrFFmpegUnavailable = errors.New("ffmpeg is not available")

const (
	defaultTranscodeTimeout = 5 * time.Minute
	maxTranscodeSeconds     = 7200
	maxStderrBytes          = 4096
)

// Clip is an absolute range in the original track.
type Clip struct {
	StartMs int64
	EndMs   int64
}

// AudioInput is an uploaded file plus the demuxer chosen from its declared
// media type. The demuxer is validated again here so the service never hands
// ffmpeg an arbitrary format.
type AudioInput struct {
	Path    string
	Demuxer string
}

// Transcoder converts an uploaded audio file to a web-playable MP3, cutting the
// clip when one is requested.
type Transcoder interface {
	Transcode(ctx context.Context, input AudioInput, clip *Clip, dst string) error
}

// FFmpeg runs the ffmpeg binary with a restricted protocol whitelist and a hard
// timeout.
type FFmpeg struct {
	Path    string
	Timeout time.Duration
}

func NewFFmpeg(path string) *FFmpeg {
	return &FFmpeg{Path: path}
}

// Available reports whether a transcoder can actually run.
func (f *FFmpeg) Available() bool {
	return f != nil && strings.TrimSpace(f.Path) != ""
}

func (f *FFmpeg) Transcode(ctx context.Context, input AudioInput, clip *Clip, dst string) error {
	if !f.Available() {
		return ErrFFmpegUnavailable
	}
	if !allowedDemuxer(input.Demuxer) {
		return fmt.Errorf("unsupported input format %q", input.Demuxer)
	}
	if strings.TrimSpace(input.Path) == "" || strings.TrimSpace(dst) == "" {
		return errors.New("missing audio path")
	}
	if _, err := os.Stat(input.Path); err != nil {
		return fmt.Errorf("read input audio: %w", err)
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = defaultTranscodeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// libmp3lame is the normal encoder; a stripped ffmpeg build may only ship
	// the built-in mp3 encoder. Nothing else is accepted.
	encoders := []string{"libmp3lame", "mp3"}
	var lastErr error
	for _, encoder := range encoders {
		_ = os.Remove(dst)
		stderr, err := f.run(ctx, input, clip, dst, encoder)
		if err == nil {
			if verifyErr := verifyMP3(dst); verifyErr == nil {
				return nil
			} else {
				lastErr = verifyErr
			}
		} else {
			lastErr = fmt.Errorf("ffmpeg: %w: %s", err, summarizeStderr(stderr))
		}
		_ = os.Remove(dst)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if encoder == "libmp3lame" && encoderUnavailable(stderr) {
			continue
		}
		return lastErr
	}
	return lastErr
}

func (f *FFmpeg) run(ctx context.Context, input AudioInput, clip *Clip, dst, encoder string) (string, error) {
	command := exec.CommandContext(ctx, f.Path, f.args(input, clip, dst, encoder)...)
	var stderr limitedBuffer
	stderr.max = maxStderrBytes
	command.Stderr = &stderr
	command.Stdout = io.Discard
	command.WaitDelay = 5 * time.Second
	// Do not inherit the server environment (which may contain its token).
	command.Env = []string{}
	command.Dir = os.TempDir()
	err := command.Run()
	return stderr.buf.String(), err
}

func (f *FFmpeg) args(input AudioInput, clip *Clip, dst, encoder string) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-protocol_whitelist", "file,pipe",
		"-abort_on", "empty_output",
		"-f", input.Demuxer,
	}
	if input.Demuxer == "mov" {
		// Self-contained M4A only: do not follow external data references.
		args = append(args, "-enable_drefs", "0", "-use_absolute_path", "0")
	}
	durationMs := int64(0)
	if clip != nil {
		start := clip.StartMs
		if start < 0 {
			start = 0
		}
		args = append(args, "-ss", seconds(start))
		durationMs = clip.EndMs - start
	}
	args = append(args, "-i", input.Path)
	if durationMs > 0 {
		args = append(args, "-t", seconds(durationMs))
	} else {
		args = append(args, "-t", strconv.Itoa(maxTranscodeSeconds))
	}
	args = append(args,
		"-map", "0:a:0",
		"-vn", "-sn", "-dn",
		"-map_metadata", "-1", "-map_chapters", "-1",
		"-c:a", encoder,
		"-b:a", "192k",
		"-ar", "44100",
		"-ac", "2",
		"-f", "mp3",
		dst,
	)
	return args
}

func allowedDemuxer(name string) bool {
	switch name {
	case "mp3", "mov", "aac", "flac", "ogg", "wav":
		return true
	default:
		return false
	}
}

func seconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

func encoderUnavailable(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "unknown encoder") || strings.Contains(lower, "encoder not found")
}

func summarizeStderr(stderr string) string {
	text := strings.TrimSpace(stderr)
	text = strings.ReplaceAll(text, "\n", "; ")
	if len(text) > 500 {
		text = text[:500]
	}
	if text == "" {
		return "no diagnostics"
	}
	return text
}

func verifyMP3(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read transcoded audio: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("read transcoded audio: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("ffmpeg produced an empty file")
	}
	header := make([]byte, 3)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read transcoded audio: %w", err)
	}
	if string(header) == "ID3" {
		return nil
	}
	if header[0] == 0xFF && header[1]&0xE0 == 0xE0 {
		return nil
	}
	return errors.New("ffmpeg did not produce MP3 audio")
}

// limitedBuffer keeps at most max bytes of ffmpeg diagnostics.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.max - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

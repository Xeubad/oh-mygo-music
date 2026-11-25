package player

import (
	"errors"
	"io"
	"math"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/audio"
)

type ClipPreview struct {
	output *audio.Player
	clip   *clipDecoder
	start  time.Duration
	end    time.Duration
	gain   float64
}

func OpenClipPreview(path string, start, end time.Duration) (*ClipPreview, error) {
	if start < 0 || end <= start {
		return nil, errors.New("invalid preview range")
	}
	download, err := openFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := openStream(download, 0)
	if err != nil {
		download.Close()
		return nil, err
	}
	first := clipFrame(start, decoded.SampleRate())
	if err := decoded.SetPosition(first); err != nil {
		decoded.Close()
		return nil, err
	}
	clip := &clipDecoder{stream: decoded, position: first, closed: make(chan struct{})}
	clip.limit.Store(clipFrame(end, decoded.SampleRate()))
	preview := &ClipPreview{output: audio.NewPlayer(clip), clip: clip, start: start, end: end, gain: 1}
	preview.Seek(start)
	return preview, nil
}

func (song *Song) OpenClipPreview(path string, start, end time.Duration) (*ClipPreview, error) {
	preview, err := OpenClipPreview(path, start, end)
	if err == nil {
		preview.gain = normGain(song.st)
	}
	return preview, err
}

func (preview *ClipPreview) SetRange(start, end, position time.Duration) {
	preview.start, preview.end = start, end
	preview.clip.limit.Store(clipFrame(end, preview.clip.SampleRate()))
	// 重定位会丢弃旧边界下预解码的样本，也能在延长终点后继续读取。
	preview.Seek(position)
}

func (preview *ClipPreview) Seek(position time.Duration) {
	last := max(preview.start, preview.end-time.Millisecond)
	preview.output.Seek(min(max(position, preview.start), last))
}

func (preview *ClipPreview) SetVolume(volume float64, normalize bool) {
	gain := volume * volume
	if normalize {
		gain *= preview.gain
	}
	preview.output.SetVolume(gain)
}

func (preview *ClipPreview) Play() { preview.output.Play() }

func (preview *ClipPreview) Pause() { preview.output.Pause() }

func (preview *ClipPreview) Playing() bool { return preview.output.Playing() }

func (preview *ClipPreview) Position() time.Duration { return preview.output.Position() }

func (preview *ClipPreview) Duration() time.Duration { return preview.end - preview.start }

func (preview *ClipPreview) Close() {
	preview.output.Close()
	// 解码线程退出并释放文件后，调用方才能删除 Windows 上的试听文件。
	<-preview.clip.closed
}

func clipFrame(at time.Duration, rate int) int64 {
	return int64(math.Round(at.Seconds() * float64(rate)))
}

type clipDecoder struct {
	stream
	limit    atomic.Int64
	position int64
	closed   chan struct{}
}

// 输出沿用整曲时间轴，实际终点由 Read 的可变边界决定。
func (clip *clipDecoder) Len() int64 { return -1 }

func (clip *clipDecoder) SetPosition(frame int64) error {
	if frame == clip.position {
		return nil
	}
	if err := clip.stream.SetPosition(frame); err != nil {
		return err
	}
	clip.position = frame
	return nil
}

func (clip *clipDecoder) Read(samples []float32) (int, error) {
	remaining := (clip.limit.Load() - clip.position) * int64(clip.Channels())
	if remaining <= 0 {
		return 0, io.EOF
	}
	count, err := clip.stream.Read(samples[:min(int64(len(samples)), remaining)])
	clip.position += int64(count / clip.Channels())
	return count, err
}

func (clip *clipDecoder) Close() error {
	err := clip.stream.Close()
	close(clip.closed)
	return err
}

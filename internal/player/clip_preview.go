package player

import (
	"errors"
	"io"
	"time"

	"github.com/egoist/mygo/audio"
)

type ClipPreview struct {
	output *audio.Player
	stream stream
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
	first := int64(start) * int64(decoded.SampleRate()) / int64(time.Second)
	last := int64(end) * int64(decoded.SampleRate()) / int64(time.Second)
	if err := decoded.SetPosition(first); err != nil {
		decoded.Close()
		return nil, err
	}
	bounded := &clipDecoder{Decoder: decoded, start: first, length: last - first}
	return &ClipPreview{output: audio.NewPlayer(bounded), stream: decoded}, nil
}

func (preview *ClipPreview) Play() { preview.output.Play() }

func (preview *ClipPreview) Duration() time.Duration { return preview.output.Duration() }

func (preview *ClipPreview) Close() {
	preview.output.Close()
	preview.stream.Close()
}

type clipDecoder struct {
	audio.Decoder
	start    int64
	length   int64
	position int64
}

func (clip *clipDecoder) Len() int64 { return clip.length }

func (clip *clipDecoder) SetPosition(frame int64) error {
	clip.position = min(max(frame, 0), clip.length)
	return clip.Decoder.SetPosition(clip.start + clip.position)
}

func (clip *clipDecoder) Read(samples []float32) (int, error) {
	remaining := (clip.length - clip.position) * int64(clip.Channels())
	if remaining <= 0 {
		return 0, io.EOF
	}
	count, err := clip.Decoder.Read(samples[:min(int64(len(samples)), remaining)])
	clip.position += int64(count / clip.Channels())
	return count, err
}

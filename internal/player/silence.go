package player

import (
	"errors"
	"io"
	"math"
	"time"

	"github.com/egoist/mygo/audio"
)

const (
	trimSilenceDB         = -50.0
	trimSilenceWindow     = 10 * time.Millisecond
	trimSilenceMinimumRun = 120 * time.Millisecond
)

// findSilenceTrimBounds scans a complete decoder and restores it to its start.
func findSilenceTrimBounds(decoder audio.Decoder) (int64, int64, int64, error) {
	rate, channels := decoder.SampleRate(), decoder.Channels()
	if rate <= 0 || channels <= 0 {
		return 0, 0, 0, errors.New("player: invalid audio format")
	}
	windowFrames := max(int64(rate)*int64(trimSilenceWindow)/int64(time.Second), 1)
	trimFrames := int64(rate) * int64(trimSilenceMinimumRun) / int64(time.Second)
	threshold := math.Pow(10, trimSilenceDB/20)
	keptChannels := min(channels, 2)
	buffer := make([]float32, 2048*channels)
	var (
		totalFrames, windowStart, windowFramesRead int64
		windowEnergy                               float64
		windowFirstLoud, windowLastLoud            int64
		firstActive, lastActive                    int64 = -1, -1
	)
	finishWindow := func() {
		if windowFramesRead == 0 {
			return
		}
		rms := math.Sqrt(windowEnergy / float64(windowFramesRead*int64(keptChannels)))
		if rms >= threshold && windowFirstLoud >= 0 {
			if firstActive < 0 {
				firstActive = windowStart + windowFirstLoud
			}
			lastActive = windowStart + windowLastLoud + 1
		}
		windowStart += windowFramesRead
		windowFramesRead, windowEnergy = 0, 0
		windowFirstLoud, windowLastLoud = -1, -1
	}
	var scanErr error
	for {
		count, err := decoder.Read(buffer)
		count -= count % channels
		for offset := 0; offset < count; offset += channels {
			loud := false
			for channel := 0; channel < keptChannels; channel++ {
				sample := float64(buffer[offset+channel])
				windowEnergy += sample * sample
				if math.Abs(sample) >= threshold {
					loud = true
				}
			}
			if loud {
				if windowFirstLoud < 0 {
					windowFirstLoud = windowFramesRead
				}
				windowLastLoud = windowFramesRead
			}
			windowFramesRead++
			totalFrames++
			if windowFramesRead == windowFrames {
				finishWindow()
			}
		}
		if err == io.EOF {
			finishWindow()
			break
		}
		if err != nil {
			scanErr = err
			break
		}
		if count == 0 {
			scanErr = io.ErrNoProgress
			break
		}
	}
	resetErr := decoder.SetPosition(0)
	if scanErr != nil {
		return 0, 0, totalFrames, scanErr
	}
	if resetErr != nil {
		return 0, 0, totalFrames, resetErr
	}
	if firstActive < 0 || lastActive < 0 {
		return 0, totalFrames, totalFrames, nil
	}
	start := int64(0)
	if firstActive >= trimFrames {
		start = firstActive
	}
	end := totalFrames
	if totalFrames-lastActive >= trimFrames {
		end = lastActive
	}
	if end <= start {
		return 0, totalFrames, totalFrames, nil
	}
	return start, end, totalFrames, nil
}

// trimSilenceStream only scans completed downloads, so boundary detection cannot stall a live song.
func trimSilenceStream(source stream) (stream, error) {
	download, _ := source.source()
	if download == nil {
		return source, nil
	}
	download.mu.Lock()
	complete := download.done && download.err == nil
	download.mu.Unlock()
	if !complete {
		return source, nil
	}
	start, end, total, err := findSilenceTrimBounds(source)
	if err != nil {
		if resetErr := source.SetPosition(0); resetErr != nil {
			return nil, errors.Join(err, resetErr)
		}
		return source, nil
	}
	if start == 0 && end == total {
		resetStreamSpectrum(source)
		return source, nil
	}
	if err := source.SetPosition(start); err != nil {
		if resetErr := source.SetPosition(0); resetErr != nil {
			return nil, errors.Join(err, resetErr)
		}
		return source, nil
	}
	resetStreamSpectrum(source)
	return &silenceTrimStream{stream: source, start: start, end: end}, nil
}

func resetStreamSpectrum(source stream) {
	switch decoded := source.(type) {
	case *decodedStream:
		decoded.spec = newSpectrum(decoded.SampleRate())
	case *wavStream:
		decoded.spec = newSpectrum(decoded.SampleRate())
	case *mp3Stream:
		decoded.spec = newSpectrum(decoded.SampleRate())
	case *oggStream:
		decoded.spec = newSpectrum(decoded.SampleRate())
	case *adtsStream:
		decoded.spec = newSpectrum(decoded.SampleRate())
	}
}

type silenceTrimStream struct {
	stream
	start, end int64
	position   int64
}

func (source *silenceTrimStream) Len() int64 { return source.end - source.start }
func (source *silenceTrimStream) Read(dst []float32) (int, error) {
	channels := source.Channels()
	remaining := source.Len() - source.position
	if remaining <= 0 {
		return 0, io.EOF
	}
	limit := min(len(dst)-len(dst)%channels, int(min(remaining*int64(channels), int64(len(dst)))))
	count, err := source.stream.Read(dst[:limit])
	count -= count % channels
	source.position += int64(count / channels)
	if source.position >= source.Len() {
		return count, io.EOF
	}
	return count, err
}
func (source *silenceTrimStream) SetPosition(frame int64) error {
	frame = min(max(frame, 0), source.Len())
	if err := source.stream.SetPosition(source.start + frame); err != nil {
		return err
	}
	source.position = frame
	return nil
}

func silenceTrimOffset(source stream) time.Duration {
	switch current := source.(type) {
	case *silenceTrimStream:
		return time.Duration(current.start) * time.Second / time.Duration(current.SampleRate())
	case *preparedStream:
		return silenceTrimOffset(current.stream)
	default:
		return 0
	}
}

package player

import (
	"io"
	"math"

	"github.com/egoist/mygo/audio"
)

// 重采样放在准备/解码线程；切歌的帧号始终属于 48 kHz 输出，不能混用来源帧。
type transitionDecoder struct {
	decoder  audio.Decoder
	frames   [][2]float32
	base     int64
	position int64
	ended    bool
	failure  error
	length   int64
	origin   int64
}

func newTransitionDecoder(decoder audio.Decoder) *transitionDecoder {
	length := decoder.Len()
	// MP3/部分流媒体的 Len 来自标注或码率估算，不能据此提前截歌。
	switch source := decoder.(type) {
	case *watched:
		length = trustedTransitionLength(source.stream)
	case stream:
		length = trustedTransitionLength(source)
	}
	if length >= 0 {
		length = (length*48000 + int64(decoder.SampleRate()) - 1) / int64(decoder.SampleRate())
	}
	return &transitionDecoder{decoder: decoder, length: length}
}

func trustedTransitionLength(source stream) int64 {
	switch original := source.(type) {
	case *preparedStream:
		return trustedTransitionLength(original.stream)
	case *mp3Stream, *adtsStream:
		return -1
	case *oggStream:
		if !original.seekable {
			return -1
		}
		return original.Len()
	default:
		return source.Len()
	}
}

func (decoder *transitionDecoder) SampleRate() int { return 48000 }
func (decoder *transitionDecoder) Channels() int   { return 2 }
func (decoder *transitionDecoder) Len() int64 {
	if decoder.length < 0 {
		return -1
	}
	return max(decoder.length-decoder.origin, 0)
}
func (decoder *transitionDecoder) Close() error {
	if closer, ok := decoder.decoder.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (decoder *transitionDecoder) fill(until int64) {
	for !decoder.ended && decoder.base+int64(len(decoder.frames)) <= until {
		channels := decoder.decoder.Channels()
		buffer := make([]float32, 1024*channels)
		count, failure := decoder.decoder.Read(buffer)
		for offset := 0; offset+channels <= count; offset += channels {
			frame := [2]float32{buffer[offset], buffer[offset]}
			if channels > 1 {
				frame[1] = buffer[offset+1]
			}
			decoder.frames = append(decoder.frames, frame)
		}
		if failure != nil || count == 0 {
			decoder.ended = true
			decoder.failure = failure
			if failure == nil {
				decoder.failure = io.ErrNoProgress
			}
		}
	}
}

func (decoder *transitionDecoder) Read(dst []float32) (int, error) {
	count := 0
	rate := float64(decoder.decoder.SampleRate()) / 48000
	for count+1 < len(dst) {
		sourcePosition := float64(decoder.position) * rate
		center := int64(sourcePosition)
		reach := int64(16)
		if rate == 1 {
			reach = 0
		}
		decoder.fill(center + reach)
		if center >= decoder.base+int64(len(decoder.frames)) {
			return count, decoder.failure
		}
		var frame [2]float64
		if rate == 1 {
			sample := decoder.frames[center-decoder.base]
			frame = [2]float64{float64(sample[0]), float64(sample[1])}
		} else {
			cutoff := math.Min(1, 1/rate)
			total := 0.0
			for index := center - reach + 1; index <= center+reach; index++ {
				if index < 0 || index < decoder.base || index >= decoder.base+int64(len(decoder.frames)) {
					continue
				}
				distance := float64(index) - sourcePosition
				weight := cutoff
				if distance != 0 {
					weight = math.Sin(math.Pi*distance*cutoff) / (math.Pi * distance)
				}
				weight *= .5 + .5*math.Cos(math.Pi*distance/float64(reach))
				sample := decoder.frames[index-decoder.base]
				frame[0] += float64(sample[0]) * weight
				frame[1] += float64(sample[1]) * weight
				total += weight
			}
			if total != 0 {
				frame[0] /= total
				frame[1] /= total
			}
		}
		dst[count], dst[count+1] = float32(frame[0]), float32(frame[1])
		count += 2
		decoder.position++
		keep := max(center-reach, 0)
		if keep-decoder.base > 4096 {
			removed := min(keep-decoder.base, int64(len(decoder.frames)))
			copy(decoder.frames, decoder.frames[removed:])
			decoder.frames = decoder.frames[:int64(len(decoder.frames))-removed]
			decoder.base += removed
		}
	}
	return count, nil
}

func (decoder *transitionDecoder) SetPosition(frame int64) error {
	sourceFrame := frame * int64(decoder.decoder.SampleRate()) / 48000
	if err := decoder.decoder.SetPosition(sourceFrame); err != nil {
		return err
	}
	decoder.frames = nil
	decoder.base = sourceFrame
	decoder.position = frame
	decoder.ended = false
	decoder.failure = nil
	return nil
}

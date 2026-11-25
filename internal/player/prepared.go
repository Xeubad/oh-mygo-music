package player

import (
	"context"
	"errors"
	"io"
	"time"
)

// preparedStream 保留定位后的首帧；音频层确认起点前不读取，避免预解码吞掉续播片段。
type preparedStream struct {
	stream
	frame  int64
	first  []float32
	active bool
}

func prepareStream(ctx context.Context, s stream, from time.Duration) (stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frame := max(int64(from)*int64(s.SampleRate())/int64(time.Second), 0)
	if from > 0 {
		if length := s.Len(); length >= 0 && frame >= length {
			return nil, errors.New("player: interruption position is outside this recording")
		}
		if mp3, ok := s.(*mp3Stream); ok && !mp3.dl.onDisk {
			// 流式 MP3 的字节比例不是时间证据；逐样本跳过才能证明没有越过中断点。
			if err := s.SetPosition(0); err != nil {
				return nil, err
			}
			buf := make([]float32, 2048*s.Channels())
			remaining := frame * int64(s.Channels())
			for remaining > 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				n, err := s.Read(buf[:min(int64(len(buf)), remaining)])
				remaining -= int64(n)
				if err != nil {
					return nil, err
				}
				if n <= 0 {
					return nil, io.ErrNoProgress
				}
			}
		} else if err := s.SetPosition(frame); err != nil {
			return nil, err
		}
	}
	// SetPosition 可能只登记跳过帧数，必须实际读到该位置的音频才算定位完成。
	first := make([]float32, 2048*s.Channels())
	n, err := s.Read(first)
	if failure := ctx.Err(); failure != nil {
		return nil, failure
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n <= 0 {
		return nil, io.ErrNoProgress
	}
	return &preparedStream{stream: s, frame: frame, first: first[:n], active: from <= 0}, nil
}

func (s *preparedStream) Read(dst []float32) (int, error) {
	if !s.active {
		// audio.NewPlayer 会先解码；等待显式 Seek 建立高层位置后再消费已准备样本。
		return 0, io.EOF
	}
	if len(s.first) > 0 {
		n := copy(dst, s.first)
		s.first = s.first[n:]
		return n, nil
	}
	return s.stream.Read(dst)
}

func (s *preparedStream) SetPosition(frame int64) error {
	if !s.active && frame == s.frame {
		s.active = true
		return nil
	}
	s.active = true
	s.first = nil
	return s.stream.SetPosition(frame)
}

package player

import (
	"io"
	"sync"

	"github.com/egoist/mygo/audio"
)

type transitionChunk struct {
	samples []float32
	err     error
}

// 每曲独立预解码；尾轨下载停滞时丢弃其未就绪尾音，不能阻塞已经开播的新曲。
type transitionBuffer struct {
	decoder audio.Decoder
	chunks  chan transitionChunk
	done    chan struct{}
	stop    func()
	mu      sync.Mutex
	current []float32
	failure error
	closed  bool
	once    sync.Once
	worker  sync.WaitGroup
	length  int64
	started bool
}

func newTransitionBuffer(decoder audio.Decoder) *transitionBuffer {
	buffer := &transitionBuffer{decoder: decoder, length: decoder.Len()}
	buffer.chunks = make(chan transitionChunk, 8)
	buffer.done = make(chan struct{})
	buffer.stop = sync.OnceFunc(func() { close(buffer.done) })
	return buffer
}
func (buffer *transitionBuffer) start() {
	buffer.chunks = make(chan transitionChunk, 8)
	buffer.done = make(chan struct{})
	chunks, done := buffer.chunks, buffer.done
	// 定位与关闭可能先后取消同一轮；失败退出也必须保留可安全重复清理的状态。
	buffer.stop = sync.OnceFunc(func() { close(done) })
	buffer.worker.Add(1)
	buffer.started = true
	go func() {
		defer buffer.worker.Done()
		for {
			samples := make([]float32, 2048)
			count, failure := buffer.decoder.Read(samples)
			if count == 0 && failure == nil {
				failure = io.ErrNoProgress
			}
			select {
			case chunks <- transitionChunk{samples: samples[:count], err: failure}:
			case <-done:
				return
			}
			if failure != nil {
				return
			}
		}
	}()
}
func (buffer *transitionBuffer) SampleRate() int { return 48000 }
func (buffer *transitionBuffer) Channels() int   { return 2 }
func (buffer *transitionBuffer) Len() int64      { return buffer.length }
func (buffer *transitionBuffer) Read(dst []float32) (int, error) {
	buffer.mu.Lock()
	if !buffer.started && !buffer.closed {
		buffer.start()
	}
	for len(buffer.current) == 0 && buffer.failure == nil && !buffer.closed {
		chunks, done := buffer.chunks, buffer.done
		buffer.mu.Unlock()
		var chunk transitionChunk
		select {
		case chunk = <-chunks:
		case <-done:
			return 0, io.ErrClosedPipe
		}
		buffer.mu.Lock()
		buffer.current, buffer.failure = chunk.samples, chunk.err
	}
	defer buffer.mu.Unlock()
	if buffer.closed {
		return 0, io.ErrClosedPipe
	}
	count := copy(dst, buffer.current)
	buffer.current = buffer.current[count:]
	if len(buffer.current) == 0 {
		return count, buffer.failure
	}
	return count, nil
}
func (buffer *transitionBuffer) ready() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if !buffer.started && !buffer.closed {
		buffer.start()
	}
	return len(buffer.current) > 0 || len(buffer.chunks) > 0 || buffer.failure != nil
}
func (buffer *transitionBuffer) SetPosition(frame int64) error {
	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return io.ErrClosedPipe
	}
	if !buffer.started {
		buffer.mu.Unlock()
		return buffer.decoder.SetPosition(frame)
	}
	buffer.stop()
	buffer.mu.Unlock()
	buffer.worker.Wait()
	if failure := buffer.decoder.SetPosition(frame); failure != nil {
		return failure
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.current = nil
	buffer.failure = nil
	if buffer.closed {
		return io.ErrClosedPipe
	}
	buffer.start()
	return nil
}
func (buffer *transitionBuffer) Close() error {
	buffer.once.Do(func() {
		buffer.mu.Lock()
		buffer.closed = true
		buffer.stop()
		buffer.mu.Unlock()
		if closer, ok := buffer.decoder.(io.Closer); ok {
			closer.Close()
		}
	})
	return nil
}

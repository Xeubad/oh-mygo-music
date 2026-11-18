package player

import (
	"io"
	"sync"

	"github.com/egoist/mygo/audio"

	"mygo-music/internal/automix"
)

type transitionChunk struct {
	samples []float32
	err     error
}

// 每曲独立预解码；尾轨下载停滞时丢弃其未就绪尾音，不能阻塞已经开播的新曲。
type transitionBuffer struct {
	decoder     audio.Decoder
	chunks      chan transitionChunk
	done        chan struct{}
	stop        func()
	mu          sync.Mutex
	current     []float32
	failure     error
	closed      bool
	once        sync.Once
	worker      sync.WaitGroup
	length      int64
	started     bool
	capacity    int
	analyzer    *automix.Analyzer
	prefix      []float32
	prefixLimit int
	produced    int64
	consumed    int64
	actualEnd   int64
	intro       []float32
	introAt     int
	discard     int
	mapping     *automix.Mapping
}

func newTransitionBuffer(decoder audio.Decoder) *transitionBuffer {
	buffer := &transitionBuffer{decoder: decoder, length: decoder.Len(), capacity: 8, actualEnd: -1}
	buffer.chunks = make(chan transitionChunk, 8)
	buffer.done = make(chan struct{})
	buffer.stop = sync.OnceFunc(func() { close(buffer.done) })
	return buffer
}

func newAnalyzedTransitionBuffer(decoder audio.Decoder, incoming bool) *transitionBuffer {
	buffer := newTransitionBuffer(decoder)
	// 最长十二秒重叠还需要两秒规划提前量；只预读十二秒会让长方案生成时已经过期。
	const seconds = 16
	if incoming {
		buffer.prefixLimit = 16 * automix.SampleRate * 2
	}
	buffer.capacity = (seconds*automix.SampleRate + 1023) / 1024
	buffer.analyzer = automix.NewAnalyzer(0)
	return buffer
}

func (buffer *transitionBuffer) start() {
	buffer.chunks = make(chan transitionChunk, buffer.capacity)
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
			if buffer.analyzer != nil {
				buffer.analyzer.Feed(samples[:count])
			}
			select {
			case chunks <- transitionChunk{samples: samples[:count], err: failure}:
				buffer.mu.Lock()
				if buffer.prefixLimit > len(buffer.prefix) {
					kept := min(count, buffer.prefixLimit-len(buffer.prefix))
					buffer.prefix = append(buffer.prefix, samples[:kept]...)
				}
				buffer.produced += int64(count / 2)
				if failure == io.EOF {
					buffer.actualEnd = buffer.produced
				}
				buffer.mu.Unlock()
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
	// 变速前段已离线准备；原始前段仍在同一回放队列，只在交接消费时跳过一次。
	for buffer.discard > 0 && !buffer.closed {
		if len(buffer.current) == 0 {
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
		count := min(buffer.discard, len(buffer.current))
		buffer.current = buffer.current[count:]
		buffer.discard -= count
		if count == 0 && buffer.failure != nil {
			failure := buffer.failure
			buffer.mu.Unlock()
			return 0, failure
		}
	}
	if buffer.introAt < len(buffer.intro) && !buffer.closed {
		count := copy(dst, buffer.intro[buffer.introAt:])
		buffer.introAt += count
		buffer.consumed += int64(count / 2)
		if buffer.introAt == len(buffer.intro) {
			buffer.intro, buffer.introAt = nil, 0
		}
		buffer.mu.Unlock()
		return count, nil
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
	buffer.consumed += int64(count / 2)
	if len(buffer.current) == 0 {
		return count, buffer.failure
	}
	return count, nil
}

func (buffer *transitionBuffer) activate() {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.prefix, buffer.prefixLimit = nil, 0
}

func (buffer *transitionBuffer) analysis() (automix.Features, int64, []float32) {
	if buffer.analyzer == nil {
		return automix.Features{}, -1, nil
	}
	snapshot := buffer.analyzer.Snapshot()
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	end := buffer.length
	if buffer.actualEnd >= 0 {
		end = buffer.actualEnd
	}
	return snapshot, end, append([]float32(nil), buffer.prefix...)
}

func (buffer *transitionBuffer) installIntro(samples []float32, mapping automix.Mapping, consumed int) bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.closed || buffer.consumed != 0 || consumed > len(buffer.prefix) || consumed%2 != 0 {
		return false
	}
	buffer.intro, buffer.introAt, buffer.discard = samples, 0, consumed
	buffer.mapping = &mapping
	return true
}

func (buffer *transitionBuffer) sourceFrame(frame int64) int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.mapping != nil {
		return buffer.mapping.SourceFrame(frame)
	}
	return frame
}

func (buffer *transitionBuffer) outputFrame(frame int64) int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.mapping != nil {
		return buffer.mapping.OutputFrame(frame)
	}
	return frame
}

func (buffer *transitionBuffer) lengths() (int64, int64) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	source := buffer.length
	if buffer.analyzer != nil && buffer.actualEnd >= 0 {
		source = buffer.actualEnd
	}
	output := source
	if source >= 0 && buffer.mapping != nil {
		output = buffer.mapping.OutputFrame(source)
	}
	return source, output
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
	buffer.produced, buffer.consumed, buffer.actualEnd = 0, 0, -1
	buffer.prefix, buffer.intro, buffer.mapping = nil, nil, nil
	buffer.introAt, buffer.discard = 0, 0
	if buffer.analyzer != nil {
		buffer.analyzer = automix.NewAnalyzer(0)
	}
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

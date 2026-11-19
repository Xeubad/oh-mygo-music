package player

import (
	"io"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
)

// TransitionSettings 是下一次过渡的快照；已开始的交叉不受设置修改影响。
type TransitionSettings struct {
	Crossfade         bool          `json:"crossfadeEnabled"`
	CrossfadeDuration time.Duration `json:"-"`
	PlaybackFade      bool          `json:"playbackFadeEnabled"`
	PauseFadeOut      time.Duration `json:"-"`
	PlayFadeIn        time.Duration `json:"-"`
}

func DefaultTransitionSettings() TransitionSettings {
	return TransitionSettings{Crossfade: true, CrossfadeDuration: 5 * time.Second, PlaybackFade: true, PauseFadeOut: fadeOut, PlayFadeIn: fadeIn}
}

type transitionEvent struct {
	id        uint64
	frame     int64
	failed    error
	ended     bool
	crossfade bool
}

type transitionVoice struct {
	decoder     audio.Decoder
	buffer      []float32
	offset      int
	err         error
	pos         int64
	length      int64
	gain        float64
	id          uint64
	closeOnce   sync.Once
	closed      bool
	admit       func() error
	reject      func(error)
	admitted    bool
	retireFrame int64
}

func (voice *transitionVoice) readFrame() ([2]float32, bool, error) {
	if voice.offset >= len(voice.buffer) {
		if voice.err != nil {
			return [2]float32{}, false, voice.err
		}
		voice.buffer = make([]float32, 1024*2)
		count, failure := voice.decoder.Read(voice.buffer)
		voice.buffer, voice.offset, voice.err = voice.buffer[:count-count%2], 0, failure
		if len(voice.buffer) == 0 {
			if failure == nil {
				failure = io.ErrNoProgress
			}
			return [2]float32{}, false, failure
		}
	}
	frame := [2]float32{voice.buffer[voice.offset], voice.buffer[voice.offset+1]}
	voice.offset += 2
	voice.pos++
	return frame, true, nil
}

func (voice *transitionVoice) close() {
	voice.closeOnce.Do(func() {
		voice.closed = true
		if closer, ok := voice.decoder.(io.Closer); ok {
			closer.Close()
		}
	})
}

// 单一 Decoder 在同一个 PCM 时间轴接续两首，避免靠两个 Play 调用的时序实现无缝。
type transitionOutput struct {
	mu                  sync.Mutex
	current, next, tail *transitionVoice
	settings            TransitionSettings
	frame               int64
	overlap, progress   int64
	events              []transitionEvent
	closed              bool
	allowNext           bool
	resume              chan struct{}
	readMu              sync.Mutex
	started             bool
	retired             []*transitionVoice
	segments            []transitionEvent
	voices              map[uint64]*transitionVoice
	terminal            *transitionEvent
}

func newTransitionOutput(first audio.Decoder) *transitionOutput {
	voice := &transitionVoice{decoder: first, length: first.Len(), gain: 1}
	return &transitionOutput{current: voice, allowNext: true, segments: []transitionEvent{{id: 0, frame: 0}}, voices: map[uint64]*transitionVoice{0: voice}}
}

func (output *transitionOutput) SampleRate() int { return 48000 }
func (output *transitionOutput) Channels() int   { return 2 }
func (output *transitionOutput) Len() int64      { return -1 }

func (output *transitionOutput) queue(next audio.Decoder, settings TransitionSettings, id uint64) {
	output.queueConfigured(next, settings, id, 1, nil, nil)
}

func (output *transitionOutput) queueConfigured(next audio.Decoder, settings TransitionSettings, id uint64, gain float64, admit func() error, reject func(error)) {
	if buffered, ok := next.(*transitionBuffer); ok {
		buffered.ready()
	}
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.closed {
		if closer, ok := next.(io.Closer); ok {
			closer.Close()
		}
		return
	}
	if output.next != nil {
		output.next.close()
		delete(output.voices, output.next.id)
	}
	output.next = &transitionVoice{decoder: next, length: next.Len(), gain: gain, id: id, admit: admit, reject: reject}
	output.voices[id] = output.next
	output.settings = settings
}

func (output *transitionOutput) eventsAt(frame int64) []transitionEvent {
	output.mu.Lock()
	defer output.mu.Unlock()
	var events []transitionEvent
	// 首帧必须已被消费；失败没有新样本，曲尾同帧就要交付，避免挡住后面的结束事件。
	for len(output.events) > 0 && (output.events[0].frame < frame || (output.events[0].ended || output.events[0].failed != nil) && output.events[0].frame <= frame) {
		events = append(events, output.events[0])
		output.events = output.events[1:]
	}
	kept := output.retired[:0]
	for _, voice := range output.retired {
		if voice.retireFrame <= frame {
			voice.close()
			delete(output.voices, voice.id)
		} else {
			kept = append(kept, voice)
		}
	}
	output.retired = kept
	return events
}

func (output *transitionOutput) Read(dst []float32) (int, error) {
	output.readMu.Lock()
	defer output.readMu.Unlock()
	output.mu.Lock()
	defer output.mu.Unlock()
	count := 0
	var refreshed *transitionVoice
	var fadeEnd int64
	for count+1 < len(dst) {
		if output.closed || output.current == nil {
			return count, io.EOF
		}
		current := output.current
		if current != refreshed {
			if buffered, ok := current.decoder.(*transitionBuffer); ok {
				_, current.length = buffered.lengths()
			}
			fadeEnd = current.length
			if output.settings.Crossfade {
				if buffered, ok := current.decoder.(*transitionBuffer); ok {
					if audibleEnd := buffered.crossfadeEnd(fadeEnd); audibleEnd >= current.pos {
						fadeEnd = audibleEnd
					}
				}
				if output.next != nil {
					if buffered, ok := output.next.decoder.(*transitionBuffer); ok {
						buffered.skipLeadingSilence()
						_, output.next.length = buffered.lengths()
					}
				}
			}
			refreshed = current
		}
		if count > 0 && current.offset >= len(current.buffer) && current.err == nil {
			if buffered, ok := current.decoder.(*transitionBuffer); ok && !buffered.ready() {
				// 先交付已混好的首帧，不能因等待后续下载把交接和故障恢复一起卡住。
				return count, nil
			}
		}
		// Read 只发生在解码线程；等网络时释放状态锁，音量/暂停/取消仍能立即处理。
		output.mu.Unlock()
		frame, valid, failure := current.readFrame()
		output.mu.Lock()
		if output.closed {
			return count, io.EOF
		}
		if !valid {
			if output.tail != nil {
				output.tail.retireFrame = output.frame
				output.retired = append(output.retired, output.tail)
				output.tail = nil
				output.overlap, output.progress = 0, 0
			}
			if failure != io.EOF {
				return output.finish(count, failure)
			}
			if !output.allowNext {
				// 暂停不是永久 EOF；先交付旧曲末尾，后续解码等待恢复或关闭，保留已准备的 B。
				if count > 0 {
					return count, nil
				}
				resume := output.resume
				output.mu.Unlock()
				<-resume
				output.mu.Lock()
				continue
			}
			if output.next == nil || !output.settings.Crossfade {
				return output.finish(count, io.EOF)
			}
			if buffered, ok := output.next.decoder.(*transitionBuffer); ok && !buffered.ready() {
				return output.finish(count, io.EOF)
			}
			if err := output.begin(0); err != nil {
				if output.next == nil {
					return output.finish(count, io.EOF)
				}
				continue
			}
			continue
		}
		if !output.started {
			output.started = true
			output.events = append(output.events, transitionEvent{id: current.id, frame: output.frame})
		}
		mixed := false
		if output.tail == nil && output.next != nil && output.allowNext && output.settings.Crossfade && current.length >= 0 {
			if buffered, ok := output.next.decoder.(*transitionBuffer); ok && !buffered.ready() {
				frame[0] *= float32(current.gain)
				frame[1] *= float32(current.gain)
				dst[count], dst[count+1] = frame[0], frame[1]
				count += 2
				output.frame++
				continue
			}
			end := fadeEnd
			remaining := end - (current.pos - 1)
			duration := int64(output.settings.CrossfadeDuration) * 48000 / int64(time.Second)
			start := end - duration
			if output.next.length >= 0 {
				duration = min(duration, output.next.length)
			}
			if remaining > 0 && remaining <= duration && current.pos-1 >= start {
				if err := output.begin(remaining); err == nil {
					output.mu.Unlock()
					incoming, ok, err := output.current.readFrame()
					output.mu.Lock()
					if !ok {
						return count, err
					}
					frame = output.mix(frame, incoming)
					mixed = true
				}
			}
		} else if output.tail != nil {
			if buffered, ok := output.tail.decoder.(*transitionBuffer); ok && output.tail.offset >= len(output.tail.buffer) && !buffered.ready() {
				output.tail.retireFrame = output.frame
				output.retired = append(output.retired, output.tail)
				output.tail = nil
				output.overlap, output.progress = 0, 0
				frame[0] *= float32(output.current.gain)
				frame[1] *= float32(output.current.gain)
				dst[count], dst[count+1] = frame[0], frame[1]
				count += 2
				output.frame++
				continue
			}
			output.mu.Unlock()
			tail, ok, _ := output.tail.readFrame()
			output.mu.Lock()
			if !ok {
				tail = [2]float32{}
			}
			frame = output.mix(tail, frame)
			mixed = true
		}
		if !mixed {
			frame[0] *= float32(output.current.gain)
			frame[1] *= float32(output.current.gain)
		}
		dst[count], dst[count+1] = frame[0], frame[1]
		count += 2
		output.frame++
	}
	// 最后一批恰好填满时也传播 EOF，下一次读取仍会再次看到它。
	if output.allowNext && output.current != nil && output.current.offset == len(output.current.buffer) && output.current.err == io.EOF && output.next == nil {
		return output.finish(count, io.EOF)
	}
	return count, nil
}

// 解码完成并不等于听众已听到结尾；结束事件和首帧交接都带同一个输出帧号。
func (output *transitionOutput) finish(count int, failure error) (int, error) {
	if output.terminal == nil && output.current != nil {
		if failure == io.EOF {
			failure = nil
		}
		event := transitionEvent{id: output.current.id, frame: output.frame, failed: failure, ended: true}
		output.terminal = &event
		output.events = append(output.events, event)
	}
	if failure == nil {
		failure = io.EOF
	}
	return count, failure
}

func (output *transitionOutput) begin(overlap int64) error {
	next := output.next
	if overlap > 0 && output.settings.Crossfade {
		if buffered, ok := next.decoder.(*transitionBuffer); ok {
			buffered.skipLeadingSilence()
			_, next.length = buffered.lengths()
		}
	}
	// 先证明下一首确有样本；空文件/开头读错绝不能使正常旧曲进入淡出。
	output.mu.Unlock()
	_, ok, failure := next.readFrame()
	output.mu.Lock()
	if output.closed || output.next != next {
		return io.ErrClosedPipe
	}
	if ok && next.admit != nil && !next.admitted {
		output.mu.Unlock()
		failure = next.admit()
		output.mu.Lock()
		ok = failure == nil
	}
	if output.closed || output.next != next {
		return io.ErrClosedPipe
	}
	if !ok {
		if next.reject != nil {
			// 协作候选失败由原候选循环重试；不能同时提交整曲失败而取消后备候选。
			next.reject(failure)
		} else {
			output.events = append(output.events, transitionEvent{id: next.id, frame: output.frame, failed: failure})
		}
		next.close()
		output.next = nil
		return failure
	}
	next.admitted = true
	if !output.allowNext {
		// 准入已一次性提交时保留首帧缓冲，暂停只延后输出，不能重验已消费上下文。
		next.offset -= 2
		next.pos--
		return io.ErrClosedPipe
	}
	next.offset -= 2
	next.pos--
	if overlap > 0 {
		output.tail = output.current
	} else {
		output.current.retireFrame = output.frame
		output.retired = append(output.retired, output.current)
	}
	output.current, output.next = next, nil
	output.overlap, output.progress = overlap, 0
	output.events = append(output.events, transitionEvent{
		id: next.id, frame: output.frame, crossfade: overlap > 0,
	})
	output.segments = append(output.segments, transitionEvent{id: next.id, frame: output.frame})
	return nil
}

func (output *transitionOutput) mix(tail, incoming [2]float32) [2]float32 {
	progress := float64(output.progress) / float64(max(output.overlap-1, 1))
	if output.overlap == 1 {
		progress = 1
	}
	oldGain, newGain := math.Cos(progress*math.Pi/2), math.Sin(progress*math.Pi/2)
	var mixed [2]float32
	if output.tail != nil {
		oldGain *= output.tail.gain
	}
	newGain *= output.current.gain
	for channel := range mixed {
		value := float64(tail[channel])*oldGain + float64(incoming[channel])*newGain
		// 只在和超过满幅时共同限峰，避免相关双轨触发引擎最后一层硬削波。
		mixed[channel] = float32(value)
	}
	peak := max(math.Abs(float64(mixed[0])), math.Abs(float64(mixed[1])))
	if peak > 1 {
		mixed[0] /= float32(peak)
		mixed[1] /= float32(peak)
	}
	output.progress++
	if output.progress >= output.overlap && output.tail != nil {
		output.tail.retireFrame = output.frame + 1
		output.retired = append(output.retired, output.tail)
		output.tail = nil
	}
	return mixed
}

func (output *transitionOutput) SetPosition(frame int64) error {
	output.readMu.Lock()
	defer output.readMu.Unlock()
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.closed || output.current == nil {
		return io.ErrClosedPipe
	}
	if output.tail != nil {
		output.tail.close()
		output.tail = nil
	}
	if output.next != nil {
		output.next.close()
		delete(output.voices, output.next.id)
		output.next = nil
	}
	output.current.buffer = nil
	output.current.offset = 0
	output.current.err = nil
	output.events = nil
	output.terminal = nil
	output.frame = max(frame, 0)
	start := int64(0)
	for _, segment := range output.segments {
		if segment.id == output.current.id {
			start = segment.frame
		}
	}
	output.current.pos = max(output.frame-start, 0)
	output.segments = []transitionEvent{{id: output.current.id, frame: start}}
	return output.current.decoder.SetPosition(output.current.pos)
}

func (output *transitionOutput) Close() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.closed {
		return nil
	}
	output.closed = true
	if output.resume != nil {
		close(output.resume)
		output.resume = nil
	}
	for _, voice := range []*transitionVoice{output.current, output.next, output.tail} {
		if voice != nil {
			voice.close()
		}
	}
	for _, voice := range output.retired {
		voice.close()
	}
	output.retired = nil
	return nil
}

func (output *transitionOutput) configureNext(id uint64, gain float64, admit func() error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.next != nil && output.next.id == id {
		output.next.gain, output.next.admit = gain, admit
	}
}

func (output *transitionOutput) cancelNext() {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.next != nil {
		output.next.close()
		delete(output.voices, output.next.id)
		output.next = nil
	}
}

func (output *transitionOutput) setAllowed(allowed bool) {
	output.mu.Lock()
	if !allowed && output.resume == nil && !output.closed {
		output.resume = make(chan struct{})
	} else if allowed && output.resume != nil {
		close(output.resume)
		output.resume = nil
	}
	output.allowNext = allowed
	output.mu.Unlock()
}

func (output *transitionOutput) setGain(id uint64, gain float64) {
	output.mu.Lock()
	defer output.mu.Unlock()
	// 主线程持有可听身份，解码 current 可能已领先到下一首。
	if voice := output.voices[id]; voice != nil {
		voice.gain = gain
	}
}

var _ audio.Decoder = (*transitionOutput)(nil)

// 解码可能领先可听游标一秒；取消/定位先选回听众当前听到的轨，再清除未听到的混音。
func (output *transitionOutput) selectCurrent(id uint64) bool {
	// 先中断其他轨等待中的 Read，避免主线程等待 readMu 时被网络永久卡住。
	output.mu.Lock()
	for voiceID, voice := range output.voices {
		if voiceID != id {
			voice.close()
		}
	}
	output.mu.Unlock()
	output.readMu.Lock()
	defer output.readMu.Unlock()
	output.mu.Lock()
	defer output.mu.Unlock()
	voice := output.voices[id]
	if voice == nil {
		return false
	}
	if output.current == voice {
		return false
	}
	changed := output.current != voice
	if changed {
		output.current = voice
	}
	output.tail = nil
	output.overlap, output.progress = 0, 0
	kept := output.retired[:0]
	for _, retired := range output.retired {
		if retired != voice {
			kept = append(kept, retired)
		}
	}
	output.retired = kept
	for voiceID, other := range output.voices {
		if voiceID != id {
			other.close()
			delete(output.voices, voiceID)
		}
	}
	output.next = nil
	output.events = nil
	output.terminal = nil
	return changed
}

func (output *transitionOutput) startedID(id uint64) bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.current != nil && output.current.id == id
}

func (output *transitionOutput) lengthOf(id uint64) int64 {
	output.mu.Lock()
	defer output.mu.Unlock()
	voice := output.voices[id]
	if voice == nil {
		return -1
	}
	if buffered, ok := voice.decoder.(*transitionBuffer); ok {
		source, _ := buffered.lengths()
		return source
	}
	return voice.length
}

func (output *transitionOutput) sourceFrameOf(id uint64, frame int64) int64 {
	output.mu.Lock()
	defer output.mu.Unlock()
	if voice := output.voices[id]; voice != nil {
		if buffered, ok := voice.decoder.(*transitionBuffer); ok {
			return buffered.sourceFrame(frame)
		}
	}
	return frame
}

func (output *transitionOutput) hasTail() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.tail != nil
}

func (output *transitionOutput) failureAudible(id uint64, position time.Duration) bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.terminal != nil && output.terminal.id == id && output.terminal.failed != nil && int64(math.Round(position.Seconds()*48000)) >= output.terminal.frame
}

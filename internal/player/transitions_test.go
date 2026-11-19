package player

import (
	"context"
	"errors"
	"io"
	"math"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/egoist/mygo/audio"
)

type transitionSamples struct {
	samples []float32
	pos     int
	rate    int
	unknown bool
}

type transitionBroken struct{ transitionSamples }

func (*transitionBroken) Len() int64 { return 48000 }

func (*transitionBroken) Read([]float32) (int, error) { return 0, errors.New("stream failed") }

type transitionReadError struct{ transitionSamples }

func (samples *transitionReadError) Read(dst []float32) (int, error) {
	count, _ := samples.transitionSamples.Read(dst)
	return count, io.ErrUnexpectedEOF
}

type transitionSeekFailure struct {
	transitionSamples
	closes int
}

func (*transitionSeekFailure) SetPosition(int64) error { return io.ErrUnexpectedEOF }
func (decoder *transitionSeekFailure) Close() error {
	decoder.closes++
	return nil
}

// 定位失败后关闭播放器仍须幂等，不能重复关闭预解码取消通道而崩溃。
func TestTransitionSeekFailureStillClosesStream(t *testing.T) {
	decoder := &transitionSeekFailure{transitionSamples: transitionSamples{samples: []float32{.2, .2}, rate: 48000}}
	output := newTransitionOutput(newTransitionBuffer(decoder))
	defer output.Close()
	if count, _ := output.Read(make([]float32, 2)); count != 2 {
		t.Fatal("首帧没有输出")
	}
	if err := output.SetPosition(0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("定位错误未保留: %v", err)
	}
	output.Close()
	output.Close()
	if decoder.closes != 1 {
		t.Fatalf("底层流关闭 %d 次", decoder.closes)
	}
}

// 首帧已经输出后遇到读取错误，交接不能回滚，且错误必须留给有限恢复路径。
func TestTransitionIncomingReadErrorKeepsCommittedIdentity(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2}, rate: 48000})
	defer output.Close()
	output.queue(&transitionReadError{transitionSamples{samples: []float32{.3, .3}, rate: 48000}}, TransitionSettings{Crossfade: true}, 9)
	count, failure := output.Read(make([]float32, 8))
	if count != 4 || !errors.Is(failure, io.ErrUnexpectedEOF) {
		t.Fatalf("新曲错误被吞掉: count=%d err=%v", count, failure)
	}
	events := output.eventsAt(2)
	if len(events) != 3 || events[1].id != 9 || events[1].failed != nil || !events[2].ended || events[2].failed == nil {
		t.Fatalf("已开播交接被回滚: %+v", events)
	}
}

// 暂停意图出现后，尚未交接的下一曲不能被提前解码的窗口启动。
func TestTransitionPausePreventsPendingHandoff(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2, .2, .2}, rate: 48000})
	defer output.Close()
	output.queue(&transitionSamples{samples: []float32{.3, .3}, rate: 48000}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 8)
	output.setAllowed(false)
	actual := make([]float32, 8)
	count, _ := output.Read(actual)
	if count != 4 {
		t.Fatalf("暂停后新曲仍开播: %v", actual)
	}
	for _, event := range output.eventsAt(8) {
		if event.id == 8 {
			t.Fatal("暂停后仍发生交接")
		}
	}
}

// 最终准入期间暂停不能吞掉 B 首帧，也不能恢复时再次使用已经结束的准入上下文。
func TestTransitionPauseDuringAdmissionPreservesFirstFrame(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2, .2, .2}, rate: 48000})
	defer output.Close()
	admissions := 0
	output.queueConfigured(&transitionSamples{samples: []float32{.3, .3, .4, .4, .5, .5}, rate: 48000}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 8, 1, func() error {
		admissions++
		if admissions > 1 {
			return io.ErrClosedPipe
		}
		output.setAllowed(false)
		return nil
	}, nil)
	paused := make([]float32, 2)
	output.Read(paused)
	if !reflect.DeepEqual(paused, []float32{.2, .2}) {
		t.Fatalf("暂停期间旧曲丢帧: %v", paused)
	}
	for _, event := range output.eventsAt(1) {
		if event.id == 8 {
			t.Fatal("暂停时仍发生交接")
		}
	}
	output.setAllowed(true)
	actual := make([]float32, 6)
	count, _ := output.Read(actual)
	if admissions != 1 || count != len(actual) || math.Abs(float64(actual[0])-.3) > .00001 {
		t.Fatalf("暂停恢复丢失准入或首帧: admissions=%d count=%d samples=%v", admissions, count, actual)
	}
}

// 曲尾准入期间暂停不产生永久结束，恢复继续使用同一首帧和一次准入。
func TestTransitionPauseAtEndDuringAdmissionResumesPreparedSong(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2}, rate: 48000})
	defer output.Close()
	admissions := 0
	output.queueConfigured(&transitionSamples{samples: []float32{.3, .3, .4, .4}, rate: 48000}, TransitionSettings{Crossfade: true}, 8, 1, func() error {
		admissions++
		output.setAllowed(false)
		return nil
	}, nil)
	if count, failure := output.Read(make([]float32, 8)); count != 2 || failure != nil {
		t.Fatalf("暂停被当成永久 EOF: count=%d err=%v", count, failure)
	}
	for _, event := range output.eventsAt(1) {
		if event.ended {
			t.Fatal("暂停生成了旧曲的永久终止事件")
		}
	}
	output.setAllowed(true)
	actual := make([]float32, 4)
	count, _ := output.Read(actual)
	if admissions != 1 || count != 4 || !reflect.DeepEqual(actual, []float32{.3, .3, .4, .4}) {
		t.Fatalf("恢复重播或丢首帧: admissions=%d count=%d samples=%v", admissions, count, actual)
	}
}

// 不同采样率按同一输出时钟接续，重采样保持常量与正确帧数。
func TestTransitionResamplesBeforeHandoff(t *testing.T) {
	for _, rate := range []int{24000, 44100, 96000} {
		samples := make([]float32, rate/100*2)
		for index := range samples {
			samples[index] = .25
		}
		decoder := newTransitionDecoder(&transitionSamples{samples: samples, rate: rate})
		actual := make([]float32, 1200)
		count, failure := decoder.Read(actual)
		if count != 960 || failure != io.EOF {
			t.Fatalf("rate=%d count=%d err=%v", rate, count, failure)
		}
		for _, sample := range actual[:count] {
			if math.Abs(float64(sample)-.25) > .0001 {
				t.Fatalf("rate=%d sample=%v", rate, sample)
			}
		}
	}
}

// 新曲首帧失败时旧曲的每帧保持原音量，且只能报告失败而不能报告交接。
func TestTransitionFailureBeforeFirstFrameKeepsOldSong(t *testing.T) {
	first := &transitionSamples{samples: []float32{.4, .4, .4, .4, .4, .4, .4, .4}, rate: 48000}
	output := newTransitionOutput(first)
	output.started = true
	defer output.Close()
	output.queue(&transitionBroken{}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 7)
	actual := make([]float32, 8)
	count, _ := output.Read(actual)
	if count != 8 || !reflect.DeepEqual(actual, first.samples) {
		t.Fatalf("旧曲被打断或静音: %v", actual)
	}
	events := output.eventsAt(8)
	if len(events) != 2 || events[0].failed == nil || !events[1].ended || events[1].id != 0 {
		t.Fatalf("错误交接: %+v", events)
	}
}

// 曲尾才发现待接曲不可读时，失败不能挡住同帧的结束事件，播放器仍需进入有限回退。
func TestTransitionFailureAtEndStillCompletes(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2, .2, .2}, rate: 48000})
	defer output.Close()
	output.queue(&transitionBroken{}, TransitionSettings{Crossfade: true}, 1)
	if count, failure := output.Read(make([]float32, 8)); count != 4 || failure != io.EOF {
		t.Fatalf("旧曲没有正常结束: count=%d err=%v", count, failure)
	}
	events := output.eventsAt(2)
	if len(events) != 3 || events[1].failed == nil || !events[2].ended || events[2].id != 0 {
		t.Fatalf("曲尾失败挡住了结束事件: %+v", events)
	}
}

// 两轨等功率交叉在首尾准确，相关满幅输入经过共同峰值保护后不硬削波。
func TestTransitionCrossfadeUsesSharedOutputFrames(t *testing.T) {
	first := &transitionSamples{samples: []float32{.4, .4, .4, .4, .4, .4}, rate: 48000}
	second := &transitionSamples{samples: []float32{.4, .4, .4, .4, .4, .4}, rate: 48000}
	output := newTransitionOutput(first)
	output.started = true
	defer output.Close()
	output.queue(second, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 1)
	actual := make([]float32, 6)
	output.Read(actual)
	if math.Abs(float64(actual[0])-.4) > .00001 || math.Abs(float64(actual[2])-.5656854) > .00001 || math.Abs(float64(actual[4])-.4) > .00001 {
		t.Fatalf("交叉曲线: %v", actual)
	}
	if events := output.eventsAt(1); len(events) != 1 || !events[0].crossfade {
		t.Fatalf("交叉淡化未随首帧报告实际方式: %+v", events)
	}
}

// 五秒混音必须放在有效音频上，不能把首尾静音算进交叉时长。
func TestCrossfadeSkipsBoundarySilence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const rate = 48000
		oldSamples := make([]float32, 16*rate*2)
		newSamples := make([]float32, 18*rate*2)
		for frame := range 12 * rate {
			oldSamples[frame*2] = .4
		}
		clear(oldSamples[3*rate*2 : (3*rate+rate/3)*2])
		for frame := 2 * rate; frame < 18*rate; frame++ {
			newSamples[frame*2+1] = .4
		}
		old := newTransitionBuffer(&transitionSamples{samples: oldSamples, rate: rate})
		incoming := newTransitionBuffer(&transitionSamples{samples: newSamples, rate: rate})
		output := newTransitionOutput(old)
		defer output.Close()
		output.queue(incoming, DefaultTransitionSettings(), 1)
		actual := make([]float32, 14*rate*2)
		read := 0
		for read < len(actual) {
			synctest.Wait()
			count, err := output.Read(actual[read:min(len(actual), read+2048)])
			read += count
			if err != nil {
				t.Fatal(err)
			}
		}
		var handoff transitionEvent
		for _, event := range output.eventsAt(14 * rate) {
			if event.id == 1 && !event.ended {
				handoff = event
			}
		}
		if handoff.frame != 7*rate || !handoff.crossfade {
			t.Fatalf("淡化应从有效尾音结束前五秒开始: %+v", handoff)
		}
		if frame := (3*rate + rate/10) * 2; actual[frame] != 0 || actual[frame+1] != 0 {
			t.Fatal("歌曲中间的停顿被改写")
		}
		middle := (9*rate + rate/2) * 2
		if math.Abs(float64(actual[middle])-.4/math.Sqrt2) > .001 || math.Abs(float64(actual[middle+1])-.4/math.Sqrt2) > .001 {
			t.Fatalf("淡化中点应同时听到两首: %v", actual[middle:middle+2])
		}
		if got := output.sourceFrameOf(1, 3*rate); got != 5*rate {
			t.Fatalf("跳过首静音后丢失了原曲时间: %d", got)
		}
	})
}

// 同相满幅混音共同衰减左右声道，不能逐声道削波改变声像；反相信号保留自然抵消。
func TestTransitionPeakProtectionKeepsStereoBalance(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		incoming []float32
		middle   [2]float32
	}{
		{"correlated", []float32{1, .5, 1, .5, 1, .5}, [2]float32{1, .5}},
		{"opposed", []float32{-1, -.5, -1, -.5, -1, -.5}, [2]float32{0, 0}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			output := newTransitionOutput(&transitionSamples{samples: []float32{1, .5, 1, .5, 1, .5}, rate: 48000})
			defer output.Close()
			output.queue(&transitionSamples{samples: scenario.incoming, rate: 48000}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 1)
			actual := make([]float32, 6)
			if count, _ := output.Read(actual); count != len(actual) {
				t.Fatalf("混音丢帧: %d", count)
			}
			for channel, expected := range scenario.middle {
				if math.Abs(float64(actual[2+channel]-expected)) > .00001 {
					t.Fatalf("中点声像改变: %v", actual)
				}
			}
			for _, sample := range actual {
				if math.Abs(float64(sample)) > 1 {
					t.Fatalf("混音超过满幅: %v", actual)
				}
			}
		})
	}
}

// 待接曲短于配置过渡时，必须等它能覆盖整个剩余窗口再交接。
func TestTransitionShortIncomingCommitsOnlyAtItsWindow(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2, .2, .2}, rate: 48000})
	output.started = true
	defer output.Close()
	next := &transitionSamples{samples: []float32{.3, .3}, rate: 48000}
	output.queue(next, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 2)
	output.Read(make([]float32, 2))
	events := output.eventsAt(0)
	if len(events) != 0 {
		t.Fatalf("短曲窗口应在最后一帧才开始: %+v", events)
	}
	output.Read(make([]float32, 2))
	events = output.eventsAt(2)
	if len(events) != 2 || events[0].id != 2 || events[0].failed != nil || !events[1].ended {
		t.Fatalf("新曲未交接: %+v", events)
	}
}

// 交叉结束后歌曲响度增益继续作用，不能随着尾轨释放被跳过。
func TestTransitionGainContinuesAfterOverlap(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.4, .4, .4, .4}, rate: 48000})
	defer output.Close()
	output.queue(&transitionSamples{samples: []float32{.4, .4, .4, .4, .4, .4, .4, .4}, rate: 48000}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 3)
	output.configureNext(3, .5, nil)
	actual := make([]float32, 8)
	output.Read(actual)
	if math.Abs(float64(actual[6])-.2) > .00001 {
		t.Fatalf("尾轨释放后丢失响度增益: %v", actual)
	}
}

// 预混已经进入 B、可听身份仍为 A 时调整主音量，不能把 A 的响度增益写给 B。
func TestTransitionVolumeBeforeAudibleHandoffKeepsIncomingGain(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.4, .4, .4, .4}, rate: 48000})
	defer output.Close()
	output.queueConfigured(&transitionSamples{samples: []float32{.4, .4, .4, .4, .4, .4, .4, .4}, rate: 48000}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 3, .5, nil, nil)
	output.Read(make([]float32, 2))
	current := New(func(func()) {})
	current.transition = output
	current.gain = 1
	current.Normalize = func() bool { return true }
	current.SetVolume(.5)
	actual := make([]float32, 6)
	output.Read(actual)
	if math.Abs(float64(actual[4])-.2) > .00001 {
		t.Fatalf("调主音量污染新曲响度: %v", actual)
	}
}

// 未知长度的新曲提前结束时，旧尾曲不能继续冒充当前曲或混入第三首。
func TestTransitionShortUnknownIncomingReleasesTail(t *testing.T) {
	output := newTransitionOutput(&transitionSamples{samples: []float32{.2, .2, .2, .2, .2, .2, .2, .2}, rate: 48000})
	defer output.Close()
	output.queue(&transitionSamples{samples: []float32{.3, .3}, rate: 48000, unknown: true}, TransitionSettings{Crossfade: true, CrossfadeDuration: time.Second}, 2)
	output.Read(make([]float32, 2))
	output.queue(&transitionSamples{samples: []float32{.4, .4, .4, .4}, rate: 48000}, TransitionSettings{Crossfade: true}, 3)
	actual := make([]float32, 4)
	output.Read(actual)
	if !reflect.DeepEqual(actual, []float32{.4, .4, .4, .4}) {
		t.Fatalf("第三曲混入旧尾曲: %v", actual)
	}
}

func (samples *transitionSamples) SampleRate() int { return samples.rate }
func (samples *transitionSamples) Channels() int   { return 2 }
func (samples *transitionSamples) Len() int64 {
	if samples.unknown {
		return -1
	}
	return int64(len(samples.samples) / 2)
}
func (samples *transitionSamples) SetPosition(frame int64) error {
	samples.pos = min(int(frame)*2, len(samples.samples))
	return nil
}
func (samples *transitionSamples) Read(dst []float32) (int, error) {
	count := copy(dst, samples.samples[samples.pos:])
	samples.pos += count
	if samples.pos == len(samples.samples) {
		return count, io.EOF
	}
	return count, nil
}

// 自然接歌跨越同一次读取时，末尾样本与 EOF 不能吞掉下一首或额外补静音。
func TestTransitionZeroOverlapKeepsEveryFrame(t *testing.T) {
	first := &transitionSamples{samples: []float32{.1, .1, .2, .2}, rate: 48000}
	second := &transitionSamples{samples: []float32{.3, .3, .4, .4}, rate: 48000}
	output := newTransitionOutput(first)
	output.started = true
	defer output.Close()
	output.queue(second, TransitionSettings{Crossfade: true}, 1)
	var decoder audio.Decoder = output
	actual := make([]float32, 8)
	count, failure := decoder.Read(actual)
	if count != 8 || failure != io.EOF || !reflect.DeepEqual(actual, []float32{.1, .1, .2, .2, .3, .3, .4, .4}) {
		t.Fatalf("frames=%v count=%d failure=%v", actual, count, failure)
	}
	if events := output.eventsAt(1); len(events) != 0 {
		t.Fatalf("交接过早: %v", events)
	}
	events := output.eventsAt(3)
	if len(events) != 1 || events[0].id != 1 || events[0].frame != 2 {
		t.Fatalf("交接边界: %+v", events)
	}
}

// 曲边静音裁剪保留 A 内部 300ms 的停顿和所有有效音频帧。
func TestTransitionTrimmedEdgesKeepMiddlePause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		makeSamples := func(leading, active, pause, resumed, trailing int, value float32) []float32 {
			samples := make([]float32, leading*2)
			for range active {
				samples = append(samples, value, value)
			}
			for range pause {
				samples = append(samples, 0, 0)
			}
			for range resumed {
				samples = append(samples, value, value)
			}
			for range trailing {
				samples = append(samples, 0, 0)
			}
			return samples
		}
		openTrimmed := func(samples []float32) audio.Decoder {
			decoded := &transitionTestStream{
				transitionSamples: transitionSamples{samples: samples, rate: 1000},
				dl:                downloaded(nil),
			}
			trimmed, err := trimSilenceStream(decoded)
			if err != nil {
				t.Fatal(err)
			}
			return newTransitionDecoder(trimmed)
		}
		firstSamples := makeSamples(200, 120, 300, 150, 250, .2)
		secondSamples := makeSamples(150, 250, 0, 0, 200, .3)
		readAll := func(decoder audio.Decoder) []float32 {
			t.Helper()
			var samples []float32
			buffer := make([]float32, 4096)
			for {
				count, err := decoder.Read(buffer)
				samples = append(samples, buffer[:count]...)
				if err == io.EOF {
					return samples
				}
				if err != nil || count == 0 {
					t.Fatalf("参考音频解码停止: count=%d err=%v", count, err)
				}
			}
		}
		firstExpected := readAll(openTrimmed(firstSamples))
		secondExpected := readAll(openTrimmed(secondSamples))
		expected := append(firstExpected, secondExpected...)
		first := openTrimmed(firstSamples)
		second := openTrimmed(secondSamples)
		output := newTransitionOutput(newTransitionBuffer(first))
		output.queue(newTransitionBuffer(second), TransitionSettings{Crossfade: true}, 1)
		defer output.Close()
		synctest.Wait()
		var actual []float32
		buffer := make([]float32, 256)
		for {
			count, err := output.Read(buffer)
			actual = append(actual, buffer[:count]...)
			if err == io.EOF {
				break
			}
			if err != nil || count == 0 {
				t.Fatalf("混音停止: count=%d err=%v", count, err)
			}
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("曲边静音未裁或中间停顿被裁: actualFrames=%d expectedFrames=%d", len(actual)/2, len(expected)/2)
		}
		t.Log("首尾静音已裁；中间停顿 300 帧保留；交接新增空帧 0")
	})
}

// 只裁掉达到阈值的歌曲首尾近静音，歌曲中间同样长度的停顿必须逐样本保留。
func TestTransitionTrimsOnlyLeadingAndTrailingSilence(t *testing.T) {
	appendFrames := func(samples []float32, count int, value float32) []float32 {
		for range count {
			samples = append(samples, value, value)
		}
		return samples
	}
	samples := make([]float32, 0)
	samples = appendFrames(samples, 200, 0)
	samples = appendFrames(samples, 120, .2)
	samples = appendFrames(samples, 300, 0)
	samples = appendFrames(samples, 150, .2)
	samples = appendFrames(samples, 250, 0)
	decoder := &transitionSamples{samples: samples, rate: 1000}
	start, end, total, err := findSilenceTrimBounds(decoder)
	if err != nil {
		t.Fatal(err)
	}
	if start != 200 || end != 770 || total != 1020 {
		t.Fatalf("静音边界 start=%d end=%d total=%d，期望 200/770/1020", start, end, total)
	}
	if err = decoder.SetPosition(start); err != nil {
		t.Fatal(err)
	}
	actual := make([]float32, (end-start)*2)
	count, err := decoder.Read(actual)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if count != len(actual) {
		t.Fatalf("定位后读取 %d 个样本，期望 %d", count, len(actual))
	}
	if !reflect.DeepEqual(actual[240:840], appendFrames(make([]float32, 0), 300, 0)) {
		t.Fatal("歌曲中间的 300ms 停顿被误裁")
	}
}

// 未完整下载的音源不能为了探测尾静音而读到 EOF，避免阻塞当前播放。
func TestTransitionDoesNotScanIncompleteDownload(t *testing.T) {
	samples := &transitionSamples{samples: []float32{0, 0, .2, .2}, rate: 48000}
	source := &transitionTestStream{transitionSamples: *samples, dl: &download{done: false}}
	trimmed, err := trimSilenceStream(source)
	if err != nil {
		t.Fatal(err)
	}
	if trimmed != source || samples.pos != 0 {
		t.Fatal("未完成下载被扫描或改变了播放起点")
	}
}

// 全程静音或不足 120ms 的边缘静音不能把短音效、短歌曲裁成空内容。
func TestTransitionKeepsSilentAndShortEdgeContent(t *testing.T) {
	for _, samples := range [][]float32{
		make([]float32, 48000*2),
		{0, 0, 0, 0, .2, .2, .2, .2, 0, 0, 0, 0},
	} {
		source := &transitionTestStream{
			transitionSamples: transitionSamples{samples: samples, rate: 48000},
			dl:                downloaded(nil),
		}
		trimmed, err := trimSilenceStream(source)
		if err != nil {
			t.Fatal(err)
		}
		if trimmed != source || source.pos != 0 {
			t.Fatalf("不可裁内容发生变化: trimmed=%T position=%d samples=%d", trimmed, source.pos, len(samples))
		}
	}
}

type transitionTestStream struct {
	transitionSamples
	dl *download
}

func (stream *transitionTestStream) Close() error                   { return nil }
func (stream *transitionTestStream) source() (*download, *spectrum) { return stream.dl, nil }

// 实际 FLAC 接 AAC 的预解码、重采样和曲尾衔接必须保留每个样本，不能新增空帧或重播首帧。
func TestTransitionDecodedFormatsKeepEverySample(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		open := func(name string) audio.Decoder {
			decoded, err := openStream(downloaded(readFixture(t, name)), 0)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := prepareCooperativeStream(context.Background(), decoded, 0)
			if err != nil {
				decoded.Close()
				t.Fatal(err)
			}
			return newTransitionDecoder(prepared)
		}
		read := func(decoder audio.Decoder) []float32 {
			if closer, ok := decoder.(io.Closer); ok {
				defer closer.Close()
			}
			var samples []float32
			buffer := make([]float32, 4096)
			for {
				count, err := decoder.Read(buffer)
				samples = append(samples, buffer[:count]...)
				if err == io.EOF {
					return samples
				}
				if err != nil || count == 0 {
					t.Fatalf("解码停止: count=%d err=%v", count, err)
				}
			}
		}
		first := read(open("seek.flac"))
		second := read(open("seek-aac.m4a"))
		output := newTransitionOutput(newTransitionBuffer(open("seek.flac")))
		output.queue(newTransitionBuffer(open("seek-aac.m4a")), TransitionSettings{Crossfade: true}, 1)
		synctest.Wait()
		actual := read(output)
		expected := append(first, second...)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("无缝输出与原音频直接拼接不同: actual=%d expected=%d", len(actual), len(expected))
		}
		found := false
		for _, event := range output.eventsAt(int64(len(actual) / 2)) {
			if event.id == 1 && !event.ended && event.failed == nil {
				found = true
				if event.frame != int64(len(first)/2) {
					t.Fatalf("交接帧不是旧曲的结束边界: frame=%d end=%d", event.frame, len(first)/2)
				}
			}
		}
		if !found {
			t.Fatal("缺少新曲首帧交接")
		}
		t.Logf("FLAC 结束帧 E=%d；AAC 起始帧 N=%d；总帧 %d；额外间隙=0 帧", len(first)/2, len(first)/2, len(actual)/2)
	})
}

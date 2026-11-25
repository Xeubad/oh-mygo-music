package player

import (
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type transitionGatedSamples struct {
	transitionSamples
	gate chan struct{}
}

func (decoder *transitionGatedSamples) Read(dst []float32) (int, error) {
	if decoder.pos >= 2048 {
		<-decoder.gate
	}
	return decoder.transitionSamples.Read(dst)
}

// 新曲首批音频曾被后续下载阻塞，设备收不到首帧，导致淡化卡住且无法交接当前曲。
func TestCrossfadePublishesSamplesBeforeIncomingStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const frames = 4800
		first := &transitionSamples{samples: make([]float32, frames*2), rate: 48000}
		incoming := &transitionGatedSamples{
			transitionSamples: transitionSamples{samples: make([]float32, frames*2), rate: 48000},
			gate:              make(chan struct{}),
		}
		release := sync.OnceFunc(func() { close(incoming.gate) })
		defer release()
		output := newTransitionOutput(first)
		defer output.Close()
		output.queue(newTransitionBuffer(incoming), TransitionSettings{Crossfade: true, CrossfadeDuration: 100 * time.Millisecond}, 1)
		synctest.Wait()
		type result struct {
			count int
			err   error
		}
		completed := make(chan result, 1)
		go func() {
			count, err := output.Read(make([]float32, 2048*2))
			completed <- result{count, err}
		}()
		synctest.Wait()
		select {
		case actual := <-completed:
			if actual.count != 2048 || actual.err != nil {
				t.Errorf("新曲首批音频丢失: count=%d error=%v", actual.count, actual.err)
			}
			if events := output.eventsAt(1024); len(events) != 2 || events[1].id != 1 {
				t.Errorf("首帧已输出却未交接: %+v", events)
			}
			release()
		default:
			t.Error("新曲下载停滞阻塞了已混好的音频，设备收不到首帧，无法交接当前曲")
			release()
			synctest.Wait()
			<-completed
			return
		}
		remaining := 0
		buffer := make([]float32, 4096)
		for {
			count, err := output.Read(buffer)
			remaining += count / 2
			if err == io.EOF {
				break
			}
			if err != nil || count == 0 {
				t.Fatalf("下载恢复后未接续音频: count=%d error=%v", count, err)
			}
		}
		if remaining != frames-1024 {
			t.Errorf("下载恢复后丢失或重复音频: remaining=%d", remaining)
		}
		events := output.eventsAt(frames)
		if len(events) != 1 || events[0].id != 1 || !events[0].ended || events[0].failed != nil {
			t.Fatalf("恢复后未正常播完新曲: %+v", events)
		}
	})
}

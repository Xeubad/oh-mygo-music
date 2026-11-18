package player

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/automix"
	"mygo-music/internal/music"
)

// 真实歌曲从正式 Player 进入独立引擎，再从无声设备输出，不能用离线试听替代产品链路。
func TestAdaptiveRealPlayerHandoff(t *testing.T) {
	configPath := os.Getenv("MM_ADAPTIVE_REAL")
	if configPath == "" {
		t.Skip("set MM_ADAPTIVE_REAL to a local engine validation JSON")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Current string
		Next    string
		Engine  string
		Python  string
		Cache   string
		Output  string
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	current, posted, _ := transitionPlayerFixture(t)
	source := &autoMixTestSource{files: map[string]string{"a": config.Current, "b": config.Next}}
	current.Source = func(string) music.Source { return source }
	current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
	current.AutoMixEngine = &automix.LocalEngine{Root: config.Engine, Cache: config.Cache, Python: config.Python}
	current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
	deadline := time.Now().Add(235 * time.Second)
	for time.Now().Before(deadline) {
		pumpTransitionPosts(posted)
		if current.AutoMixStatus() == "local" || current.AutoMixStatus() == "fallback" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current.AutoMixStatus() != "local" {
		t.Fatalf("正式 Player 未安装本机引擎: %s", current.AutoMixStatus())
	}
	if records := source.records(); len(records) != 0 {
		t.Fatalf("未输出首帧就提前记播放: %v", records)
	}
	current.transition.mu.Lock()
	plan := *current.transition.autoMixPlan
	current.transition.mu.Unlock()
	output := current.out.(*transitionTestOutput)
	for current.Current().ID != "b" && output.Position() < 1200*time.Second {
		output.advance(4800)
		pumpTransitionPosts(posted)
		current.commitTransitions()
		if current.out != output {
			t.Fatal("自适应接歌重建了声卡输出")
		}
	}
	if current.Current().ID != "b" {
		t.Fatal("正式 Player 没有交接")
	}
	for step := 0; step < 140; step++ {
		output.advance(4800)
		current.commitTransitions()
	}
	if position := current.Position(); position <= 0 {
		t.Fatal("源时间映射丢失")
	}
	recordDeadline := time.Now().Add(time.Second)
	for len(source.records()) < 2 && time.Now().Before(recordDeadline) {
		time.Sleep(time.Millisecond)
	}
	if records := source.records(); !reflect.DeepEqual(records, []string{"a", "b"}) {
		t.Fatalf("真实歌曲播放记录不正确: %v", records)
	}
	first := max(0, plan.Start-6*automix.SampleRate)
	last := min(int64(len(output.captured)/2), plan.End+8*automix.SampleRate)
	if config.Output != "" {
		if err := os.MkdirAll(config.Output, 0700); err != nil {
			t.Fatal(err)
		}
		writeAutoMixWave(t, filepath.Join(config.Output, "player-output.wav"), output.captured[first*2:last*2])
	}
	t.Logf("正式输出交接: start=%.3fs end=%.3fs entry=%.3fs current=%s records=%v",
		float64(plan.Start)/automix.SampleRate, float64(plan.End)/automix.SampleRate,
		float64(plan.Entry)/automix.SampleRate, current.Current().ID, source.records())
	current.stop()
	time.Sleep(150 * time.Millisecond)
	pumpTransitionPosts(posted)
}

type blockingAdaptiveEngine struct {
	called   chan struct{}
	canceled chan struct{}
}

func (engine blockingAdaptiveEngine) Render(ctx context.Context, job automix.AdaptiveJob) (*automix.AdaptiveResult, error) {
	close(engine.called)
	<-ctx.Done()
	close(engine.canceled)
	return nil, ctx.Err()
}

// 插队使引擎任务失效，旧任务的结果不能接替新的待播歌曲。
func TestAdaptiveQueueChangeCancelsOldRender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{"a": path, "b": path, "c": path}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		engine := blockingAdaptiveEngine{called: make(chan struct{}), canceled: make(chan struct{})}
		current.AutoMixEngine = engine
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		entered := false
		for step := 0; step < 30 && !entered; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			current.prepareAutoMix()
			select {
			case <-engine.called:
				entered = true
			default:
			}
		}
		if !entered {
			t.Fatal("没有进入异步本机引擎")
		}
		current.PlayNext(music.Track{ID: "c", Source: "test"})
		synctest.Wait()
		select {
		case <-engine.canceled:
		default:
			t.Fatal("插队没有取消旧渲染")
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 原曲自然结束而渲染还没完成时，应终止过期分析，下一首仍正常播放并只记录一次。
func TestAdaptiveNaturalHandoffCancelsLateRender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{"a": path, "b": path}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		engine := blockingAdaptiveEngine{called: make(chan struct{}), canceled: make(chan struct{})}
		current.AutoMixEngine = engine
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 1700 && current.Current().ID != "b"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		synctest.Wait()
		if current.Current().ID != "b" {
			t.Fatal("延迟渲染挡住了自然接续")
		}
		select {
		case <-engine.canceled:
		default:
			t.Fatal("过期引擎没有停止")
		}
		if records := source.records(); !reflect.DeepEqual(records, []string{"a", "b"}) {
			t.Fatalf("延迟分析改变播放记录: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

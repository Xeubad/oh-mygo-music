package player

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/automix"
	"mygo-music/internal/music"
)

type adaptiveTestEngine struct {
	fail   bool
	called int
}

func (engine *adaptiveTestEngine) Render(ctx context.Context, job automix.AdaptiveJob) (*automix.AdaptiveResult, error) {
	engine.called++
	if engine.fail {
		return nil, errors.New("synthetic quality rejection")
	}
	for _, path := range []string{job.Current, job.Next} {
		if info, err := os.Stat(path); err != nil || info.Size() < 44 {
			return nil, errors.New("missing locally decoded audio")
		}
	}
	checks := map[string]bool{}
	for _, name := range []string{
		"finite_audio", "no_50ms_silent_windows", "old_voice_gain_at_last_vocal",
		"new_voice_gain_at_first_vocal", "vocal_handoff", "old_music_retires_at_new_voice",
		"old_mid_gain_at_new_voice", "outgoing_beat_grid", "incoming_beat_grid",
		"relative_beat_phase", "same_source_prefix",
	} {
		checks[name] = true
	}
	samples := make([]float32, 14*automix.SampleRate*2)
	for index := range samples {
		samples[index] = .123
	}
	return &automix.AdaptiveResult{
		Schema: "adaptive-1", Status: "accepted", Checks: checks,
		Start: 8 * automix.SampleRate, End: 12 * automix.SampleRate, Entry: automix.SampleRate,
		Beats: 8, Ratio: 1, CurrentFrames: 16 * automix.SampleRate, NextFrames: 16 * automix.SampleRate,
		Consumed: 15 * automix.SampleRate, Samples: samples,
		Anchors: []automix.Anchor{{Output: 0, Source: automix.SampleRate},
			{Output: 14 * automix.SampleRate, Source: 15 * automix.SampleRate}},
	}, nil
}

// 过渡终点早于原曲 EOF 时，应在选定接点交接，且只在首帧可听后记新曲。
func TestAdaptivePlayerCommitsAtRenderedStartAndPreservesRecords(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{"a": path, "b": path}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		engine := &adaptiveTestEngine{}
		current.AutoMixEngine = engine
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for range 30 {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		for step := 0; step < 700 && current.Current().ID != "b"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "a" {
			t.Fatal("选点前提前推进了当前曲")
		}
		synctest.Wait()
		if records := source.records(); !reflect.DeepEqual(records, []string{"a"}) {
			t.Fatalf("准备音频提前写播放记录: %v", records)
		}
		for step := 0; step < 300 && current.Current().ID != "b"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" || engine.called != 1 {
			t.Fatalf("自适应接点未执行: current=%v engine=%d status=%s", current.Current(), engine.called, current.AutoMixStatus())
		}
		if current.out.Position() >= 10*time.Second {
			t.Fatalf("仍在等原曲末尾才衔接: %v", current.out.Position())
		}
		synctest.Wait()
		if records := source.records(); !reflect.DeepEqual(records, []string{"a", "b"}) {
			t.Fatalf("交接记录不正确: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 质量拒绝只退回原曲接续，不应提前切歌、关闭旧流或重复请求来源。
func TestAdaptiveRejectionRetainsOriginalPlaybackUntilEOF(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{"a": path, "b": path}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		current.AutoMixEngine = &adaptiveTestEngine{fail: true}
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 1200; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "a" || current.AutoMixStatus() != "fallback" {
			t.Fatalf("失败方案改变旧曲: current=%v status=%s", current.Current(), current.AutoMixStatus())
		}
		for step := 0; step < 600 && current.Current().ID != "b"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" {
			t.Fatal("拒绝后没有按原曲接续")
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 独立分析解码不得消费或关闭正在播放的下载，包括内存缓存来源。
func TestAdaptiveSnapshotKeepsPlaybackSourceReadable(t *testing.T) {
	ctx := context.Background()
	sourcePath := autoMixSongFixture(t, 120, .1)
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	download := downloaded(data)
	path, err := snapshotAnalysisWAV(ctx, download, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".wav" || !analysisDownloadReady(download) {
		t.Fatal("分析关闭了原下载")
	}
	decoded, err := openStream(download, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	count, err := decoded.Read(make([]float32, 2048))
	if count == 0 || err != nil && err != io.EOF {
		t.Fatalf("分析后原曲不能读取: %d %v", count, err)
	}
}

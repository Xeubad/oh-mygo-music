package player

import (
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/automix"
	"mygo-music/internal/music"
)

// 慢拍强节奏需要十余秒展开时，真实预读必须留够规划提前量，不能迫使长方案退成短淡化。
func TestAutoMixLookaheadKeepsLongBeatHandoffExecutable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		source := &autoMixTestSource{files: map[string]string{
			"a": autoMixSongFixture(t, 84, .1),
			"b": autoMixSongFixture(t, 82, .2),
		}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for range 20 {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		output := current.out
		var overlap int64
		for step := 0; step < 1600 && current.Current().ID != "b"; step++ {
			if current.transition != nil {
				current.transition.mu.Lock()
				if plan := current.transition.autoMixPlan; plan != nil {
					overlap = plan.End - plan.Start
				}
				current.transition.mu.Unlock()
			}
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" || current.out != output || overlap < 9*automix.SampleRate {
			t.Fatalf("预读没有给长过渡留出提前量: current=%+v overlap=%.3fs sameOutput=%v status=%s",
				current.Current(), float64(overlap)/automix.SampleRate, current.out == output, current.AutoMixStatus())
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

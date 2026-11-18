package automix

import (
	"math"
	"testing"
)

func acceptedAdaptiveResult() *AdaptiveResult {
	checks := map[string]bool{}
	for _, name := range []string{
		"finite_audio", "no_50ms_silent_windows", "old_voice_gain_at_last_vocal",
		"new_voice_gain_at_first_vocal", "vocal_handoff", "old_music_retires_at_new_voice",
		"old_mid_gain_at_new_voice", "outgoing_beat_grid", "incoming_beat_grid",
		"relative_beat_phase", "same_source_prefix",
	} {
		checks[name] = true
	}
	samples := make([]float32, 14*SampleRate*2)
	for index := range samples {
		samples[index] = .1
	}
	return &AdaptiveResult{
		Schema: "adaptive-1", Status: "accepted", Checks: checks, Start: 100 * SampleRate,
		End: 104 * SampleRate, Entry: 2 * SampleRate, Beats: 8, Ratio: 1,
		CurrentFrames: 110 * SampleRate, NextFrames: 120 * SampleRate,
		Consumed: 16 * SampleRate, Anchors: []Anchor{{0, 2 * SampleRate}, {14 * SampleRate, 16 * SampleRate}},
		Samples: samples,
	}
}

// 外部引擎不能只写 accepted 绕过质量拦截，所有必需检查均须存在且通过。
func TestAdaptiveResultRequiresEveryQualityCheck(t *testing.T) {
	result := acceptedAdaptiveResult()
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	delete(result.Checks, "incoming_beat_grid")
	if result.Validate() == nil {
		t.Fatal("漏报鼓点检查仍获准进入播放器")
	}
}

// 非有限样本和倒退时间映射必须在切换当前歌曲前拒绝。
func TestAdaptiveResultRejectsInvalidPCMAndMapping(t *testing.T) {
	result := acceptedAdaptiveResult()
	result.Samples[10] = float32(math.NaN())
	if result.Validate() == nil {
		t.Fatal("非有限音频进入播放")
	}
	result = acceptedAdaptiveResult()
	result.Anchors = []Anchor{{0, 2 * SampleRate}, {SampleRate, SampleRate}, {14 * SampleRate, 16 * SampleRate}}
	if result.Validate() == nil {
		t.Fatal("倒退源时间映射进入播放")
	}
}

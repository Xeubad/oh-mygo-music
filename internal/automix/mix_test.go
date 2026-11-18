package automix

import (
	"math"
	"testing"
)

// 锁拍过渡先引入高频、集中交接低频，弱节奏仍保持保守等功率曲线。
func TestLayeredGainsTradeBandsBeforeWholeMix(t *testing.T) {
	for _, scenario := range []struct {
		profile  string
		progress float64
		layered  bool
	}{
		{ProfileMatched, .2, true},
		{ProfileMatched, .5, true},
		{ProfileOriginal, .5, false},
	} {
		old, next := LayeredGains(scenario.profile, scenario.progress)
		if math.Abs(old[bandLow]*old[bandLow]+next[bandLow]*next[bandLow]-1) > .02 ||
			math.Abs(old[bandMid]*old[bandMid]+next[bandMid]*next[bandMid]-1) > .02 ||
			math.Abs(old[bandHigh]*old[bandHigh]+next[bandHigh]*next[bandHigh]-1) > .02 {
			t.Fatalf("%s progress=%.2f 频带能量不守恒: old=%v next=%v", scenario.profile, scenario.progress, old, next)
		}
		if scenario.layered {
			if !(old[bandHigh] < old[bandMid]) || !(next[bandHigh] > next[bandMid]) {
				t.Fatalf("%s progress=%.2f 新曲高频没有先行进入: old=%v next=%v", scenario.profile, scenario.progress, old, next)
			}
			if scenario.progress < .42 && next[bandLow] != 0 {
				t.Fatalf("%s progress=%.2f 旧曲底鼓还在主导时新曲低频提前叠加: %v", scenario.profile, scenario.progress, next)
			}
		} else if math.Abs(old[0]-next[0]) > .000001 || math.Abs(old[1]-next[1]) > .000001 || math.Abs(old[2]-next[2]) > .000001 {
			t.Fatalf("%s progress=%.2f 弱节奏曲线不对称: old=%v next=%v", scenario.profile, scenario.progress, old, next)
		}
	}
}

// 实际帧混音先交接高频节奏，底鼓换手前抑制新曲低频；滤波状态跨帧保留。
func TestMixEngineLayersLowAndHighFrequencies(t *testing.T) {
	energy := func(profile string, frequency float64) float64 {
		engine := &MixEngine{}
		var total float64
		for frame := 0; frame < 9600; frame++ {
			value := float32(.1 * math.Sin(2*math.Pi*frequency*float64(frame)/SampleRate))
			mixed := engine.Mix(profile, .2, [2]float32{}, [2]float32{value, value})
			if frame >= 4800 {
				total += float64(mixed[0]) * float64(mixed[0])
			}
		}
		return total
	}
	if matched, original := energy(ProfileMatched, 80), energy(ProfileOriginal, 80); matched > original*.2 {
		t.Fatalf("底鼓换手前低频提前叠加: matched=%.5f original=%.5f", matched, original)
	}
	if matched, original := energy(ProfileMatched, 8000), energy(ProfileOriginal, 8000); matched < original*1.2 {
		t.Fatalf("高频节奏没有先行进入: matched=%.5f original=%.5f", matched, original)
	}
}

// 分层混音应保留左右声道差异，不能把立体声折叠成单声道。
func TestMixEnginePreservesStereoImage(t *testing.T) {
	engine := &MixEngine{}
	for frame := 0; frame < 12; frame++ {
		engine.Mix(ProfileMatched, .5, [2]float32{.6, -.2}, [2]float32{.3, .1})
	}
	actual := engine.Mix(ProfileMatched, .5, [2]float32{.6, -.2}, [2]float32{.3, .1})
	if math.Abs(float64(actual[0]-actual[1])) < .05 {
		t.Fatalf("左右声道被折叠: %v", actual)
	}
}

// 过渡两端必须精确接回各自原声，响度增益只作用于所属声部。
func TestMixEngineEndpointsApplyIndependentVoiceGains(t *testing.T) {
	engine := &MixEngine{}
	old, next := [2]float32{.8, -.2}, [2]float32{.4, .1}
	for _, scenario := range []struct {
		progress float64
		expected [2]float32
	}{
		{0, [2]float32{.4, -.1}},
		{1, [2]float32{.1, .025}},
	} {
		actual := engine.MixWithGains(ProfileMatched, scenario.progress, old, next, .5, .25)
		for channel := range actual {
			if math.Abs(float64(actual[channel]-scenario.expected[channel])) > .000001 {
				t.Fatalf("边界或声部增益错误: progress=%v actual=%v expected=%v", scenario.progress, actual, scenario.expected)
			}
		}
	}
}

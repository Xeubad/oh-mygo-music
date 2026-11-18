package automix

import (
	"math"
	"testing"
)

// 过渡区变速必须保留音高与左右声道关系，随后回到原速，源帧映射不能倒退。
func TestStretchIntroPreservesPitchAndSourceClock(t *testing.T) {
	for _, ratio := range []float64{.95, 1, 1.05} {
		samples := make([]float32, 8*SampleRate*2)
		for frame := range len(samples) / 2 {
			value := float32(.3 * math.Sin(2*math.Pi*440*float64(frame)/SampleRate))
			samples[frame*2], samples[frame*2+1] = value, -value*.5
		}
		output, mapping, err := StretchIntro(samples, ratio, 3*SampleRate)
		if err != nil {
			t.Fatal(err)
		}
		if ratio > 1 && len(output) >= len(samples) || ratio < 1 && len(output) <= len(samples) {
			t.Fatalf("速度没有改变输出时长: ratio=%v frames=%d", ratio, len(output)/2)
		}
		crossings := 0
		for frame := SampleRate; frame < 2*SampleRate; frame++ {
			if output[frame*2] >= 0 && output[(frame-1)*2] < 0 {
				crossings++
			}
			if math.Abs(float64(output[frame*2+1]+output[frame*2]*.5)) > .00001 {
				t.Fatal("立体声通道被分别对齐")
			}
		}
		if math.Abs(float64(crossings)-440) > 2 {
			t.Fatalf("变速导致音高改变: ratio=%v frequency=%d", ratio, crossings)
		}
		previous := int64(-1)
		for frame := int64(0); frame <= int64(len(output)/2); frame++ {
			source := mapping.SourceFrame(frame)
			if source < previous {
				t.Fatal("播放时钟映射倒退")
			}
			previous = source
		}
		if previous != int64(len(samples)/2) {
			t.Fatalf("源尾部映射丢失: %d", previous)
		}
		end := len(output) / 2
		for offset := range 480 {
			if output[(end-480+offset)*2] != samples[len(samples)-960+offset*2] {
				t.Fatal("回到原速时未保留原始末段")
			}
		}
	}
}

// 非法速度或不完整的声道帧不能进入音频处理器。
func TestStretchIntroRejectsInvalidInput(t *testing.T) {
	for _, ratio := range []float64{0, .8, 1.2, math.NaN(), math.Inf(1)} {
		if _, _, err := StretchIntro(make([]float32, SampleRate*4), ratio, SampleRate); err == nil {
			t.Fatalf("接受非法速度 %v", ratio)
		}
	}
}

// 保调变速后的真实起音应持续落在目标拍点附近，不能只让时间映射看似正确。
func TestStretchIntroKeepsPercussiveBeatsOnTargetGrid(t *testing.T) {
	for _, ratio := range []float64{.96, 1.04} {
		samples := syncopatedFixture(16, 120)
		processed, _, err := StretchIntro(samples, ratio, 8*SampleRate)
		if err != nil {
			t.Fatal(err)
		}
		analyzer := NewAnalyzer(0)
		analyzer.Feed(processed)
		features := analyzer.Snapshot()
		for beat := 2; beat < 14; beat++ {
			target := (.13 + float64(beat)*.5) / ratio
			bestFrame, bestStrength := int64(0), 0.
			for _, point := range features.Points {
				if math.Abs(float64(point.Frame)/SampleRate-target) < .09 && point.Onset > bestStrength {
					bestStrength = point.Onset
					bestFrame = point.Frame
				}
			}
			if bestStrength == 0 || math.Abs(float64(bestFrame)/SampleRate-target) > .04 {
				t.Fatalf("变速后鼓点漂移: ratio=%v beat=%d error=%.1fms", ratio, beat, (float64(bestFrame)/SampleRate-target)*1000)
			}
		}
	}
}

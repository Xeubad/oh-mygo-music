package automix

import (
	"math"
	"testing"
)

// 高频切分主导全频起音时，实际底鼓仍须对齐，不能仅对齐两条看似相同的全频网格。
func TestPlanAlignsBassPulseWhenUpperPercussionDominates(t *testing.T) {
	fixture := func(bassPhase int) Features {
		points := make([]Point, 1600)
		for index := range points {
			points[index] = Point{Frame: int64(index * FeatureHop), Energy: .15}
			if index%50 == 0 {
				points[index].Onset = .08
				points[index].Bands[2] = .01
			}
			if index%50 == bassPhase {
				points[index].Bands[0] = .08
				points[index].Energy = .4
			}
		}
		return Features{
			Start: 0, End: 16 * SampleRate, Points: points,
			Tempo: Tempo{BPM: 120, Phase: 0, Confidence: .9},
		}
	}
	request := BuildRequest(fixture(15), fixture(30), 16*SampleRate, 0)
	decision, valid := request.LocalDecision()
	if !valid {
		t.Fatal("强底鼓曲对没有产生交接计划")
	}
	candidate, err := request.Resolve(decision)
	if err != nil || decision.Profile != ProfileMatched {
		t.Fatalf("强底鼓曲对没有锁拍: %+v %v", decision, err)
	}
	nextKick := float64(30*FeatureHop-candidate.Entry) / candidate.Ratio
	errorFrames := math.Abs(math.Remainder(float64(candidate.Start)+nextKick-15*FeatureHop, SampleRate/2))
	if errorFrames > .02*SampleRate {
		t.Fatalf("底鼓错拍 %.1fms，不能仅凭全频网格认定锁拍: %+v", errorFrames/48, candidate)
	}
}

// 过渡前半程保留旧曲底鼓、后半程交给新曲，避免两套低频长期以相近响度叠加。
func TestMatchedMixKeepsOneBassLineInCharge(t *testing.T) {
	energy := func(progress float64, incoming bool) float64 {
		engine := &MixEngine{}
		var total float64
		for frame := 0; frame < SampleRate/5; frame++ {
			value := float32(.1 * math.Sin(2*math.Pi*60*float64(frame)/SampleRate))
			old, next := [2]float32{value, value}, [2]float32{}
			if incoming {
				old, next = next, old
			}
			mixed := engine.Mix(ProfileMatched, progress, old, next)
			if frame >= SampleRate/10 {
				total += float64(mixed[0]) * float64(mixed[0])
			}
		}
		return total
	}
	for _, progress := range []float64{.2, .8} {
		old, next := energy(progress, false), energy(progress, true)
		dominant, competing := old, next
		if progress > .5 {
			dominant, competing = next, old
		}
		if competing > dominant*.02 {
			t.Fatalf("两套底鼓仍长时间叠加: progress=%.1f dominant=%.5f competing=%.5f", progress, dominant, competing)
		}
	}
}

// 短前奏的三拍重音不能推翻较长前段已证实的拍速，误把三分之一速度当作主拍。
func TestIncomingTempoRejectsThirdTempoAliasFromShortOpening(t *testing.T) {
	analyzer := NewAnalyzer(0)
	analyzer.Feed(rhythmFixture(16, 60, .13))
	features := analyzer.Snapshot()
	features.Tempo = Tempo{BPM: 180, Phase: 6240, Confidence: .85}
	result := features.Until(16 * SampleRate)
	if result.Tempo.Confidence >= .55 && !metricalAlternative(180, result.Tempo.BPM) {
		t.Fatalf("短前奏把三分之一拍速当作了独立主拍: %+v", result.Tempo)
	}
}

// 实际变速输出的鼓点可能偏离预测网格，安装前必须依据输出起音修正进场时刻。
func TestRenderedAlignmentCorrectsMeasuredIntroDelay(t *testing.T) {
	current := NewAnalyzer(0)
	current.Feed(rhythmFixture(16, 120, .1))
	candidate := Candidate{Start: 8 * SampleRate, End: 16 * SampleRate, Ratio: 1, Profiles: []string{ProfileMatched}}
	rendered := rhythmFixture(12, 120, .18)
	actual := RefineRenderedAlignment(current.Snapshot(), rendered, candidate, 2*SampleRate)
	if difference := math.Abs(float64(actual.Start - (candidate.Start - 3840))); difference > .02*SampleRate {
		t.Fatalf("实际输出延后80ms却没有修正交接: before=%v after=%v difference=%.1fms", candidate.Start, actual.Start, difference/48)
	}
	if actual.Start < 2*SampleRate || actual.End != candidate.End || actual.Entry != candidate.Entry {
		t.Fatalf("起音修正破坏源曲边界: %+v", actual)
	}
}

// 静音或已经对齐的前段不能为了提高相关分数而挪动边界，提前量不足也不能前移。
func TestRenderedAlignmentKeepsSafeAndAlreadyAlignedBoundaries(t *testing.T) {
	current := NewAnalyzer(0)
	current.Feed(rhythmFixture(16, 120, .1))
	candidate := Candidate{Start: 8 * SampleRate, End: 16 * SampleRate, Ratio: 1, Profiles: []string{ProfileMatched}}
	for _, scenario := range []struct {
		name     string
		samples  []float32
		earliest int64
	}{
		{"aligned", rhythmFixture(12, 120, .1), 0},
		{"silent", make([]float32, 12*SampleRate*2), 0},
		{"too-late-to-advance", rhythmFixture(12, 120, .18), candidate.Start},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			actual := RefineRenderedAlignment(current.Snapshot(), scenario.samples, candidate, scenario.earliest)
			if actual.Start < scenario.earliest || actual.End != candidate.End || actual.Entry != candidate.Entry {
				t.Fatalf("实测校准越过安全边界: %+v", actual)
			}
			if scenario.name != "too-late-to-advance" && math.Abs(float64(actual.Start-candidate.Start)) > .02*SampleRate {
				t.Fatalf("无需修正的输入被移动: %+v", actual)
			}
		})
	}
}

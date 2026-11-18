package automix

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
)

// 候选必须来自已分析的真实尾部，并按两首音乐的拍点选择动态重叠，不能只沿用固定秒数。
func TestPlanUsesMusicAndRejectsUnknownEnd(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 120, .1))
	second.Feed(rhythmFixture(16, 124, .15))
	request := BuildRequest(first.Snapshot(), second.Snapshot(), 16*SampleRate, 0)
	if len(request.Candidates) < 2 {
		t.Fatalf("没有生成内容候选: %+v", request.Candidates)
	}
	for _, candidate := range request.Candidates {
		if candidate.Start < 4*SampleRate || candidate.End != 16*SampleRate || candidate.Ratio < .94 || candidate.Ratio > 1.06 {
			t.Fatalf("候选越过安全尾区: %+v", candidate)
		}
		nextBeat := float64(candidate.InPhase-candidate.Entry) / candidate.Ratio
		phase := math.Mod(float64(candidate.Start)+nextBeat-float64(candidate.OutPhase), SampleRate/2)
		phase = math.Min(math.Abs(phase), SampleRate/2-math.Abs(phase))
		if phase > 2 {
			t.Fatalf("双曲拍点没有对齐: %+v phase=%v", candidate, phase)
		}
	}
	if unknown := BuildRequest(first.Snapshot(), second.Snapshot(), -1, 0); len(unknown.Candidates) != 0 {
		t.Fatal("未知曲尾被当作有效截断点")
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > MaxRequestBytes {
		t.Fatalf("模型特征请求无界: size=%d err=%v", len(encoded), err)
	}
}

// 模型只能选本地候选；不能用自报置信度绕过边界、注入新候选或重复 JSON 字段。
func TestDecisionRejectsUnsafeOrInventedPlan(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 120, .1))
	second.Feed(rhythmFixture(16, 124, .15))
	request := BuildRequest(first.Snapshot(), second.Snapshot(), 16*SampleRate, 0)
	if len(request.Candidates) == 0 {
		t.Fatal("没有可供模型选择的本地候选")
	}
	validID := request.Candidates[0].ID
	for _, response := range []string{
		`{"candidate_id":"invented","profile":"original"}`,
		`{"candidate_id":"mix-1","profile":"unlimited"}`,
		`{"candidate_id":"mix-1","profile":"original","start_frame":0}`,
		`{"candidate_id":"mix-1","candidate_id":"mix-2","profile":"original"}`,
		`{"candidate_id":"mix-1","profile":"original"} extra`,
	} {
		if _, err := ParseDecision([]byte(response), request); err == nil {
			t.Fatalf("接受危险模型输出: %s", response)
		}
	}
	valid := `{"candidate_id":"` + validID + `","profile":"` + request.Candidates[0].Profiles[0] + `"}`
	if _, err := ParseDecision([]byte(valid), request); err != nil {
		t.Fatal(err)
	}
}

// 最长特征窗口也必须压缩到请求上限以内，不能退化成逐样本上传。
func TestFeatureRequestStaysBoundedAtFullHistory(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(30, 118.4, .1))
	second.Feed(rhythmFixture(16, 120, .15))
	request := BuildRequest(first.Snapshot(), second.Snapshot(), 30*SampleRate, 18*SampleRate)
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > MaxRequestBytes {
		t.Fatalf("完整历史特征超限: size=%d err=%v", len(encoded), err)
	}
	if len(request.Current.Envelope) > 48 || len(request.Next.Envelope) > 48 {
		t.Fatal("模型输入没有聚合特征时间窗")
	}
}

// 乐句证据可信时自动按小节交接，并优先选择音高轮廓相容的过渡候选。
func TestPlanAutomaticallyAlignsPhrasesAndPrefersCompatibleTonalProfile(t *testing.T) {
	current := NewAnalyzer(0)
	current.Feed(barRhythmFixture(16, 120, .1, true))
	first := NewAnalyzer(0)
	first.Feed(barRhythmFixture(16, 120, .15, true))
	compatible := first.Snapshot()
	second := NewAnalyzer(0)
	second.Feed(barRhythmFixture(16, 120, .15, true))
	incompatible := second.Snapshot()
	for index := range incompatible.Points {
		incompatible.Points[index].Chroma = [12]float64{}
		incompatible.Points[index].Chroma[6] = 1
	}
	request := BuildRequest(current.Snapshot(), compatible, 16*SampleRate, 0)
	if len(request.Candidates) == 0 {
		t.Fatal("相容歌曲对没有生成自动过渡方案")
	}
	phraseCount := 0
	for _, candidate := range request.Candidates {
		if candidate.BarAligned {
			phraseCount++
			currentTempo, nextTempo := request.Current.Tempo, request.Next.Tempo
			beatPeriod := 60 * SampleRate / currentTempo.BPM
			barPeriod := 4 * beatPeriod
			currentBar := float64(currentTempo.Phase) + float64(currentTempo.BarPhase)*beatPeriod
			nextBeatPeriod := 60 * SampleRate / nextTempo.BPM
			nextBarAtOutput := (float64(nextTempo.Phase) + float64(nextTempo.BarPhase)*nextBeatPeriod - float64(candidate.Entry)) / candidate.Ratio
			phaseError := math.Mod(float64(candidate.Start)+nextBarAtOutput-currentBar+barPeriod, barPeriod)
			phaseError = min(phaseError, barPeriod-phaseError)
			if phaseError > 3 {
				t.Fatalf("两首歌的小节起点未对齐: candidate=%+v error=%v", candidate, phaseError)
			}
		}
	}
	if phraseCount == 0 {
		t.Fatal("有明显四拍重音却未生成乐句对齐方案")
	}
	incompatibleRequest := BuildRequest(current.Snapshot(), incompatible, 16*SampleRate, 0)
	best, valid := request.LocalDecision()
	other, otherValid := incompatibleRequest.LocalDecision()
	if !valid || !otherValid {
		t.Fatalf("自动决策缺失: compatible=%v incompatible=%v", valid, otherValid)
	}
	compatibleCandidate, _ := request.Resolve(best)
	incompatibleCandidate, _ := incompatibleRequest.Resolve(other)
	if compatibleCandidate.Score >= incompatibleCandidate.Score {
		t.Fatalf("没有优先更相容的音高轮廓: compatible=%+v incompatible=%+v", compatibleCandidate, incompatibleCandidate)
	}
}

// 速度差太大时不强迫用户配歌，也不锁拍变速；若尾段自然收弱则自动退为短淡化。
func TestPlanFallsBackAutomaticallyForDistantTempos(t *testing.T) {
	current := NewAnalyzer(0)
	current.Feed(rhythmFixture(16, 120, .1))
	next := NewAnalyzer(0)
	next.Feed(rhythmFixture(16, 150, .15))
	currentFeatures, nextFeatures := current.Snapshot(), next.Snapshot()
	end := int64(16 * SampleRate)
	for index := range currentFeatures.Points {
		if currentFeatures.Points[index].Frame >= end-SampleRate {
			currentFeatures.Points[index].Energy *= .25
		}
	}
	request := BuildRequest(currentFeatures, nextFeatures, end, 0)
	if len(request.Candidates) == 0 {
		t.Fatal("尾段已经收弱，未生成自动短淡化回退")
	}
	for _, candidate := range request.Candidates {
		if candidate.Profiles[len(candidate.Profiles)-1] == ProfileMatched || math.Abs(candidate.Ratio-1) > .000001 {
			t.Fatalf("速度差太大仍强行锁拍: %+v", candidate)
		}
	}
}

// 低置信度候选先出现时，短淡化不能挤满候选池并屏蔽后续可靠锁拍方案。
func TestPlanDoesNotLetFallbackCandidatesStarveReliableBeatMix(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 120, .1))
	second.Feed(rhythmFixture(16, 124, .15))
	current, next := first.Snapshot(), second.Snapshot()
	reliable := current.Tempo.Candidates[0]
	current.Tempo.Candidates = []TempoCandidate{
		{BPM: 80, Phase: 300, Confidence: .35},
		{BPM: 90, Phase: 400, Confidence: .4},
		{BPM: 100, Phase: 500, Confidence: .4},
		reliable,
	}
	for index := range current.Points {
		if current.Points[index].Frame >= 15*SampleRate {
			current.Points[index].Energy *= .25
		}
	}
	request := BuildRequest(current, next, 16*SampleRate, 0)
	decision, valid := request.LocalDecision()
	if !valid {
		t.Fatal("可靠锁拍方案未生成")
	}
	candidate, err := request.Resolve(decision)
	if err != nil || !slices.Contains(candidate.Profiles, ProfileMatched) {
		t.Fatalf("短淡化挤掉了可靠锁拍候选: %+v %v", candidate, err)
	}
}

// 3:2 切分起音即使很强，也不能用来伪造两首速度相容。
func TestPlanRejectsTripletAliasAsIndependentBeatMatch(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 95, .1))
	second.Feed(rhythmFixture(16, 127, .15))
	current, next := first.Snapshot(), second.Snapshot()
	current.Tempo.Candidates = append([]TempoCandidate{{BPM: 127, Phase: 200, Confidence: .95}}, current.Tempo.Candidates...)
	request := BuildRequest(current, next, 16*SampleRate, 0)
	for _, candidate := range request.Candidates {
		if slices.Contains(candidate.Profiles, ProfileMatched) {
			t.Fatalf("采用了和主节奏不一致的切分速度: %+v", candidate)
		}
	}
}

// 已有可靠主拍时不能为缩短淡化而挑倍速网格，导致只有一两秒的伪锁拍。
func TestPlanPrefersPrimaryGridAndSeveralBeatsOfOverlap(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 95, .1))
	second.Feed(rhythmFixture(16, 95, .15))
	current, next := first.Snapshot(), second.Snapshot()
	for _, features := range []*Features{&current, &next} {
		primary := features.Tempo.Candidates[0]
		features.Tempo.Candidates = append(features.Tempo.Candidates, TempoCandidate{
			BPM: primary.BPM * 2, Phase: primary.Phase / 2, Confidence: .9,
		})
	}
	request := BuildRequest(current, next, 16*SampleRate, 0)
	decision, valid := request.LocalDecision()
	if !valid {
		t.Fatal("主拍方案缺失")
	}
	candidate, err := request.Resolve(decision)
	if err != nil || candidate.End-candidate.Start < 4*SampleRate || candidate.OutPhase != current.Tempo.Phase || candidate.InPhase != next.Tempo.Phase {
		t.Fatalf("主网格被短淡化倍速方案覆盖: %+v %v", candidate, err)
	}
}

// 下一首前段后半部相位改变时，开头数拍仍必须使用开头区域的节拍。
func TestIncomingTempoUsesOpeningSectionInsteadOfLaterPrefix(t *testing.T) {
	points := make([]Point, 1600)
	for index := range points {
		phase := 13
		if index >= 800 {
			phase = 29
		}
		pulse := (index-phase+50)%50 == 0
		points[index] = Point{Frame: int64(index * FeatureHop), Energy: .08}
		if pulse {
			points[index].Onset = .01
			points[index].Energy = .4
			points[index].Bands[0] = .16
		}
	}
	features := Features{Start: 0, End: 16 * SampleRate, Points: points}
	result := features.Until(16 * SampleRate)
	period := float64(SampleRate / 2)
	phaseError := math.Abs(math.Remainder(float64(result.Tempo.Phase-13*FeatureHop), period))
	if result.End != 16*SampleRate || len(result.Points) != len(points) || phaseError > .025*SampleRate {
		t.Fatalf("使用了后半部相位或丢失可播放前段: tempo=%+v error=%.1fms", result.Tempo, phaseError/48)
	}
}

// 尾段拍点已改变时，不能仅凭之前的高置信度继续输出锁拍候选。
func TestPlanRejectsStaleTempoWhenOverlapNoLongerSupportsItsPhase(t *testing.T) {
	first, second := NewAnalyzer(0), NewAnalyzer(0)
	first.Feed(rhythmFixture(16, 120, .1))
	second.Feed(rhythmFixture(16, 120, .15))
	current, next := first.Snapshot(), second.Snapshot()
	for index := range current.Points {
		current.Points[index].Onset = 0
		current.Points[index].Bands = [3]float64{.01, .01, .01}
	}
	request := BuildRequest(current, next, 16*SampleRate, 0)
	for _, candidate := range request.Candidates {
		if slices.Contains(candidate.Profiles, ProfileMatched) {
			t.Fatalf("过渡区没有拍点证据仍使用旧节拍: %+v", candidate)
		}
	}
}

package automix

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
)

type Candidate struct {
	ID         string   `json:"id"`
	Start      int64    `json:"start_frame"`
	End        int64    `json:"end_frame"`
	Entry      int64    `json:"entry_frame"`
	Ratio      float64  `json:"speed_ratio"`
	OutPhase   int64    `json:"out_phase_frame"`
	InPhase    int64    `json:"in_phase_frame"`
	Harmony    float64  `json:"harmony_score"`
	BarAligned bool     `json:"bar_aligned"`
	Score      float64  `json:"local_score"`
	Profiles   []string `json:"profiles"`
}

type Request struct {
	Candidates []Candidate `json:"candidates"`
}

type Decision struct {
	CandidateID string `json:"candidate_id"`
	Profile     string `json:"profile"`
}

func (features Features) Until(end int64) Features {
	reference := features.Tempo
	previousEnd := features.End
	features.End = min(features.End, end)
	stop := sort.Search(len(features.Points), func(index int) bool { return features.Points[index].Frame >= end })
	features.Points = features.Points[:stop]
	analysis := features.Points
	if previousEnd > end {
		reference = estimateTempo(analysis)
	}
	if features.Start == 0 {
		// 待接曲的锁拍发生在开头，不能拿预读后半段的相位回推最初几拍。
		openingEnd := sort.Search(len(analysis), func(index int) bool { return analysis[index].Frame >= 8*SampleRate })
		analysis = analysis[:openingEnd]
	}
	features.Tempo = estimateTempo(analysis)
	if features.Start == 0 && features.End >= 10*SampleRate && reference.Confidence >= .55 &&
		features.Tempo.Confidence >= .55 && !metricalAlternative(reference.BPM, features.Tempo.BPM) {
		// 短窗可能只看到每三拍的重音；用长窗排除三分之一等别名，仍保留开头实测相位。
		opening := features.Tempo
		features.Tempo = Tempo{}
		for _, candidate := range opening.Candidates {
			if candidate.Confidence >= .55 && metricalAlternative(reference.BPM, candidate.BPM) {
				features.Tempo = Tempo{
					BPM: candidate.BPM, Phase: candidate.Phase, Confidence: candidate.Confidence,
					BarPhase: candidate.BarPhase, BarConfidence: candidate.BarConfidence, Candidates: []TempoCandidate{candidate},
				}
				break
			}
		}
	}
	return features
}

// BuildRequest 只在可信尾区提出候选；弱节拍使用更短的能量过渡，不猜测人声或小节语义。
func BuildRequest(current, next Features, end, earliest int64) Request {
	request := Request{}
	if end <= 0 || len(current.Points) < 100 || len(next.Points) < 100 || next.Start != 0 || next.End < SampleRate {
		return request
	}
	if current.End < end-12*SampleRate {
		return request
	}
	energyAt := func(points []Point, start, stop int64) float64 {
		var energy float64
		count := 0
		for _, point := range points {
			if point.Frame >= start && point.Frame < stop {
				energy += point.Energy
				count++
			}
		}
		if count == 0 {
			return -1
		}
		return energy / float64(count)
	}
	entry := int64(0)
	for _, point := range next.Points {
		if point.Energy >= .0032 {
			break
		}
		entry = point.Frame + FeatureHop
	}
	if entry < SampleRate*12/100 || entry > 2*SampleRate {
		entry = 0
	}
	currentTempos, nextTempos := current.Tempo.Candidates, next.Tempo.Candidates
	if len(currentTempos) == 0 {
		currentTempos = []TempoCandidate{
			{BPM: current.Tempo.BPM, Phase: current.Tempo.Phase, Confidence: current.Tempo.Confidence, BarPhase: current.Tempo.BarPhase, BarConfidence: current.Tempo.BarConfidence},
		}
	}
	if len(nextTempos) == 0 {
		nextTempos = []TempoCandidate{
			{BPM: next.Tempo.BPM, Phase: next.Tempo.Phase, Confidence: next.Tempo.Confidence, BarPhase: next.Tempo.BarPhase, BarConfidence: next.Tempo.BarConfidence},
		}
	}
	for _, currentTempo := range currentTempos {
		currentTempo, currentBass := bassAlignedTempo(current.Points, currentTempo, max(current.Start, end-8*SampleRate), end)
		for _, nextTempo := range nextTempos {
			nextTempo, nextBass := bassAlignedTempo(next.Points, nextTempo, entry, min(next.End, entry+8*SampleRate))
			locked := currentTempo.Confidence >= .55 && nextTempo.Confidence >= .55 && metricalAlternative(current.Tempo.BPM, currentTempo.BPM) && metricalAlternative(next.Tempo.BPM, nextTempo.BPM)
			barAligned := locked && currentTempo.BarConfidence >= .18 && nextTempo.BarConfidence >= .18
			ratio := 1.0
			if locked {
				ratio = currentTempo.BPM / nextTempo.BPM
				if ratio < .94 || ratio > 1.06 {
					locked = false
					ratio = 1
				}
			}
			durations := []float64{1.5, 2.5, 3.5}
			if locked {
				period := 60 / currentTempo.BPM
				durations = []float64{4 * period, 8 * period, 16 * period}
			}
			for _, seconds := range durations {
				start := end - int64(math.Round(seconds*SampleRate))
				if locked {
					period := 60 * SampleRate / currentTempo.BPM
					incomingPhase := (float64(nextTempo.Phase) - float64(entry)) / ratio
					anchor, grid := float64(currentTempo.Phase), period
					if barAligned {
						nextPeriod := 60 * SampleRate / nextTempo.BPM
						anchor += float64(currentTempo.BarPhase) * period
						incomingPhase = (float64(nextTempo.Phase) + float64(nextTempo.BarPhase)*nextPeriod - float64(entry)) / ratio
						grid *= 4
					}
					start = int64(math.Round(anchor + math.Ceil((float64(start)+incomingPhase-anchor)/grid)*grid - incomingPhase))
				}
				if start < max(earliest, end-12*SampleRate) || end-start < SampleRate || start >= end || float64(next.End-entry)/ratio < float64(end-start+2*SampleRate) {
					continue
				}
				if locked && (!supportsBeatWindow(current.Points, currentTempo, start, end) ||
					!supportsBeatWindow(next.Points, nextTempo, entry, entry+int64(float64(end-start)*ratio))) {
					continue
				}
				oldEnergy := energyAt(current.Points, start, min(end, start+SampleRate))
				newEnergy := energyAt(next.Points, entry, entry+SampleRate)
				tailEnergy := energyAt(current.Points, max(start, end-SampleRate), end)
				if oldEnergy < .0001 || newEnergy < .0001 {
					continue
				}
				currentWindow, nextWindow := int64(SampleRate), int64(SampleRate)
				if currentTempo.BPM > 0 {
					currentWindow = int64(4 * 60 * SampleRate / currentTempo.BPM)
				}
				if nextTempo.BPM > 0 {
					nextWindow = int64(4 * 60 * SampleRate / nextTempo.BPM)
				}
				harmony := tonalCompatibility(current.Points, start, min(end, start+currentWindow), next.Points, entry, min(next.End, entry+nextWindow))
				// 节拍决定能否锁定；和声只用于候选排序，不单独否决音乐。
				score := math.Abs(math.Log((oldEnergy+.001)/(newEnergy+.001))) + float64(end-start)/(12*SampleRate)
				if locked {
					// 主网格优先；倍频虽有强起音，也不应为少混一秒而压过实际拍速。
					score += .8 * (math.Abs(math.Log2(currentTempo.BPM/current.Tempo.BPM)) + math.Abs(math.Log2(nextTempo.BPM/next.Tempo.BPM)))
					ideal := 8 * 60 / currentTempo.BPM
					if currentBass && nextBass {
						// 两端都有持续底鼓时多保留一组八拍，让节奏交接能展开；弱尾奏仍用短方案。
						ideal *= 2
					}
					score += math.Abs(float64(end-start)/SampleRate-ideal) / max(ideal, 1)
				}
				score += (1 - harmony) * .2
				if tailEnergy >= 0 && tailEnergy < oldEnergy {
					score -= (oldEnergy - tailEnergy) / (oldEnergy + .001)
				}
				if !locked && tailEnergy > oldEnergy*.8 {
					// 自由节奏且没有可信收尾趋势时保留原曲，避免只是给固定淡化改名。
					continue
				}
				profiles := []string{"original"}
				if locked {
					profiles = []string{"matched"}
				}
				request.Candidates = append(request.Candidates, Candidate{
					ID: fmt.Sprintf("mix-%d", len(request.Candidates)+1), Start: start, End: end, Entry: entry, Ratio: ratio,
					OutPhase: currentTempo.Phase, InPhase: nextTempo.Phase, Harmony: harmony, BarAligned: locked && barAligned,
					Score: score, Profiles: profiles,
				})
			}
		}
	}
	// 先穷举再截断，低置信度的原速回退不能因循环顺序占满模型候选预算。
	sort.SliceStable(request.Candidates, func(first, second int) bool {
		left, right := request.Candidates[first], request.Candidates[second]
		leftMatched, rightMatched := slices.Contains(left.Profiles, ProfileMatched), slices.Contains(right.Profiles, ProfileMatched)
		if leftMatched != rightMatched {
			return leftMatched
		}
		return left.Score < right.Score
	})
	request.Candidates = request.Candidates[:min(len(request.Candidates), 16)]
	for index := range request.Candidates {
		request.Candidates[index].ID = fmt.Sprintf("mix-%d", index+1)
	}
	return request
}

func metricalAlternative(primary, candidate float64) bool {
	if primary <= 0 || candidate <= 0 {
		return false
	}
	ratio := candidate / primary
	for _, multiplier := range [...]float64{.5, 1, 2} {
		if math.Abs(ratio/multiplier-1) < .025 {
			return true
		}
	}
	return false
}

func tonalCompatibility(current []Point, currentStart, currentEnd int64, next []Point, nextStart, nextEnd int64) float64 {
	currentChroma, currentPower := chromaProfile(current, currentStart, currentEnd)
	nextChroma, nextPower := chromaProfile(next, nextStart, nextEnd)
	if currentPower <= 1e-20 || nextPower <= 1e-20 {
		return .5
	}
	var best float64
	for _, shift := range [...]int{0, 2, 3, 4, 5, 7, 8, 9, 10} {
		var product float64
		for pitch := range currentChroma {
			product += currentChroma[pitch] * nextChroma[(pitch+shift)%len(nextChroma)]
		}
		best = max(best, product/math.Sqrt(currentPower*nextPower))
	}
	return min(max(best, 0), 1)
}

func chromaProfile(points []Point, start, end int64) ([12]float64, float64) {
	var profile [12]float64
	for _, point := range points {
		if point.Frame < start || point.Frame >= end {
			continue
		}
		for pitch, value := range point.Chroma {
			profile[pitch] += value
		}
	}
	var power float64
	for _, value := range profile {
		power += value * value
	}
	return profile, power
}

func (request Request) LocalDecision() (Decision, bool) {
	candidates := append([]Candidate(nil), request.Candidates...)
	sort.SliceStable(candidates, func(first, second int) bool {
		left, right := slices.Contains(candidates[first].Profiles, ProfileMatched), slices.Contains(candidates[second].Profiles, ProfileMatched)
		if left != right {
			return left
		}
		return candidates[first].Score < candidates[second].Score
	})
	if len(candidates) == 0 {
		return Decision{}, false
	}
	candidate := candidates[0]
	profile := candidate.Profiles[len(candidate.Profiles)-1]
	return Decision{CandidateID: candidate.ID, Profile: profile}, true
}

func (request Request) Resolve(decision Decision) (Candidate, error) {
	for _, candidate := range request.Candidates {
		if candidate.ID == decision.CandidateID {
			for _, profile := range candidate.Profiles {
				if profile == decision.Profile {
					if profile == "original" {
						candidate.Ratio = 1
					}
					return candidate, nil
				}
			}
		}
	}
	return Candidate{}, errors.New("automix: decision is outside the offered candidates")
}

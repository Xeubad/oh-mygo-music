package automix

import (
	"math"
	"slices"
)

// RefineRenderedAlignment 用实际保调输出复核起音，不把理想速度比例当作已完成锁拍。
func RefineRenderedAlignment(current Features, rendered []float32, candidate Candidate, earliest int64) Candidate {
	if !slices.Contains(candidate.Profiles, ProfileMatched) || len(current.Points) < 400 || len(rendered) < 4*SampleRate*2 {
		return candidate
	}
	analyzer := NewAnalyzer(0)
	analyzer.Feed(rendered[:min(len(rendered), 14*SampleRate*2)])
	incoming := analyzer.Snapshot()
	channel := 1
	oldEnvelope, nextEnvelope := pulseEnvelope(current.Points, channel), pulseEnvelope(incoming.Points, channel)
	if len(oldEnvelope) == 0 || len(nextEnvelope) == 0 {
		oldEnvelope, nextEnvelope = pulseEnvelope(current.Points, 0), pulseEnvelope(incoming.Points, 0)
	}
	if len(oldEnvelope) == 0 || len(nextEnvelope) == 0 {
		return candidate
	}
	// 对称平滑只用于比较起音位置，不改动 PCM，也不放大音乐来制造听感差别。
	oldEnvelope, nextEnvelope = smoothPulseEnvelope(oldEnvelope), smoothPulseEnvelope(nextEnvelope)
	correlation := func(start int64) float64 {
		var product, oldPower, nextPower float64
		count := 0
		for index, nextValue := range nextEnvelope {
			frame := start + incoming.Points[index].Frame
			if frame >= candidate.End-FeatureHop || frame < start+2*FeatureHop {
				continue
			}
			position := float64(frame-current.Points[0].Frame) / FeatureHop
			oldIndex := int(math.Floor(position))
			if oldIndex < 0 || oldIndex+1 >= len(oldEnvelope) {
				continue
			}
			fraction := position - float64(oldIndex)
			oldValue := oldEnvelope[oldIndex] + fraction*(oldEnvelope[oldIndex+1]-oldEnvelope[oldIndex])
			oldValue, nextValue = min(oldValue, 1), min(nextValue, 1)
			progress := float64(frame-start) / float64(candidate.End-start)
			weight := math.Exp(-math.Pow((progress-.5)/.3, 2))
			product += weight * oldValue * nextValue
			oldPower += weight * oldValue * oldValue
			nextPower += weight * nextValue * nextValue
			count++
		}
		if count < 200 {
			return 0
		}
		return product / math.Sqrt(max(oldPower*nextPower, 1e-20))
	}
	initial := correlation(candidate.Start)
	bestStart, best := candidate.Start, initial
	radius := int64(.32 * SampleRate)
	minimum := max(earliest, candidate.End-12*SampleRate, current.Points[0].Frame)
	maximum := min(candidate.End-SampleRate, candidate.Start+radius)
	for start := max(minimum, candidate.Start-radius); start <= maximum; start += FeatureHop / 2 {
		score := correlation(start)
		if score > best {
			best, bestStart = score, start
		}
	}
	if best >= .25 && best > initial+.025 {
		candidate.Start = bestStart
		// 波形微调后的进场不再声称满足原始小节网格，结束帧及歌曲开头仍原样保留。
		candidate.BarAligned = false
	}
	return candidate
}

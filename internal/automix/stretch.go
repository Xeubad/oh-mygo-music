package automix

import (
	"errors"
	"math"
	"sort"
)

type Anchor struct {
	Output int64
	Source int64
}

// Mapping 用实际拼接锚点记账；变速后的歌词和故障恢复仍落在同一源时间轴。
type Mapping struct {
	Anchors []Anchor
}

func (mapping Mapping) SourceFrame(output int64) int64 {
	if len(mapping.Anchors) == 0 {
		return max(output, 0)
	}
	index := sort.Search(len(mapping.Anchors), func(index int) bool { return mapping.Anchors[index].Output > output })
	if index == 0 {
		return mapping.Anchors[0].Source
	}
	first := mapping.Anchors[index-1]
	if index == len(mapping.Anchors) {
		return first.Source + output - first.Output
	}
	second := mapping.Anchors[index]
	return first.Source + (output-first.Output)*(second.Source-first.Source)/(second.Output-first.Output)
}

func (mapping Mapping) OutputFrame(source int64) int64 {
	if len(mapping.Anchors) == 0 {
		return max(source, 0)
	}
	index := sort.Search(len(mapping.Anchors), func(index int) bool { return mapping.Anchors[index].Source > source })
	if index == 0 {
		return mapping.Anchors[0].Output
	}
	first := mapping.Anchors[index-1]
	if index == len(mapping.Anchors) {
		return first.Output + source - first.Source
	}
	second := mapping.Anchors[index]
	return first.Output + (source-first.Source)*(second.Output-first.Output)/(second.Source-first.Source)
}

// StretchIntro 仅改变有限前段的时间尺度，左右声道共用 WSOLA 拼接点以保留声像。
// 速度偏移在两秒内回到 1；原始末段直接接回，后续 decoder 无需持续变速。
func StretchIntro(samples []float32, ratio float64, hold int64) ([]float32, Mapping, error) {
	if len(samples)%2 != 0 || len(samples)/2 < 1920 || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < .94 || ratio > 1.06 || hold < 0 {
		return nil, Mapping{}, errors.New("automix: invalid stretch input")
	}
	total := len(samples) / 2
	mapping := Mapping{Anchors: []Anchor{{0, 0}}}
	if math.Abs(ratio-1) < .0001 {
		mapping.Anchors = append(mapping.Anchors, Anchor{int64(total), int64(total)})
		return append([]float32(nil), samples...), mapping, nil
	}
	const hop = 480
	const window = hop * 2
	const search = 240
	output := append(make([]float32, 0, len(samples)+len(samples)/10), samples[:window*2]...)
	nominal, previous := 0.0, 0
	for position := hop; ; position += hop {
		progress := min(max(float64(int64(position)-hold)/(2*SampleRate), 0), 1)
		speed := ratio + (1-ratio)*progress
		nominal += hop * speed
		target := int(math.Round(nominal))
		low, high := max(previous+1, target-search), min(total-window, target+search)
		if low > high {
			low, high = min(previous+hop, total-window), min(previous+hop, total-window)
		}
		best, bestScore := low, -math.MaxFloat64
		scoreAt := func(candidate int) float64 {
			var product, energy float64
			for offset := 0; offset < hop; offset += 4 {
				for channel := range 2 {
					reference := float64(output[(position+offset)*2+channel])
					value := float64(samples[(candidate+offset)*2+channel])
					product += reference * value
					energy += value * value
				}
			}
			return product / math.Sqrt(max(energy, 1e-20))
		}
		for candidate := low; candidate <= high; candidate += 8 {
			if score := scoreAt(candidate); score > bestScore {
				best, bestScore = candidate, score
			}
		}
		center := best
		for candidate := max(low, center-7); candidate <= min(high, center+7); candidate++ {
			if score := scoreAt(candidate); score > bestScore {
				best, bestScore = candidate, score
			}
		}
		mapping.Anchors = append(mapping.Anchors, Anchor{int64(position), int64(best)})
		final := progress == 1 || target+window+search+hop >= total
		needed := position + window
		if final {
			needed = position + total - best
		}
		if needed*2 > len(output) {
			output = append(output, make([]float32, needed*2-len(output))...)
		}
		for offset := range hop {
			weight := float32(offset) / float32(hop)
			for channel := range 2 {
				index := (position+offset)*2 + channel
				output[index] = output[index]*(1-weight) + samples[(best+offset)*2+channel]*weight
			}
		}
		if final {
			copy(output[(position+hop)*2:], samples[(best+hop)*2:])
			mapping.Anchors = append(mapping.Anchors, Anchor{int64(needed), int64(total)})
			return output, mapping, nil
		}
		copy(output[(position+hop)*2:], samples[(best+hop)*2:(best+window)*2])
		previous = best
	}
}

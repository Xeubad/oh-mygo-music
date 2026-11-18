package automix

import (
	"math"
	"sort"
)

type pulseFit struct {
	period, phase     float64
	score, confidence float64
}

// trackTempo 用连续网格的实测起音覆盖率判断节拍，允许切分、缺拍和强弱拍并存。
func trackTempo(points []Point) Tempo {
	points = points[max(0, len(points)-1200):]
	if len(points) < 400 {
		return Tempo{}
	}
	active := 0.0
	minimum, maximum := math.Inf(1), 0.0
	for _, point := range points[max(0, len(points)-300):] {
		active = max(active, point.Energy)
	}
	for _, point := range points {
		minimum = min(minimum, point.Energy)
		maximum = max(maximum, point.Energy)
	}
	if active < .0001 {
		return Tempo{}
	}
	if maximum-minimum < maximum*.025 {
		return Tempo{}
	}
	var fits []pulseFit
	for channel := 0; channel < 4; channel++ {
		envelope := pulseEnvelope(points, channel)
		if len(envelope) == 0 {
			continue
		}
		smoothed := smoothPulseEnvelope(envelope)
		var coarse []pulseFit
		for period := 28.; period <= 105; period += .5 {
			coarse = append(coarse, fitPulse(envelope, smoothed, period))
		}
		sort.SliceStable(coarse, func(first, second int) bool { return coarse[first].score > coarse[second].score })
		var refined []pulseFit
		for _, seed := range coarse {
			duplicate := false
			for _, existing := range refined {
				if math.Abs(6000/existing.period-6000/seed.period) < 3 {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			best := seed
			for offset := -12; offset <= 12; offset++ {
				candidate := fitPulse(envelope, smoothed, seed.period+float64(offset)/25)
				if candidate.score > best.score {
					best = candidate
				}
			}
			refined = append(refined, best)
			if len(refined) == 4 {
				break
			}
		}
		fits = append(fits, refined...)
	}
	if len(fits) == 0 {
		return Tempo{}
	}
	sort.SliceStable(fits, func(first, second int) bool { return fits[first].score > fits[second].score })
	var candidates []TempoCandidate
	for _, fit := range fits {
		if fit.confidence < .3 {
			continue
		}
		bpm := 6000 / fit.period
		duplicate := false
		for _, existing := range candidates {
			if math.Abs(existing.BPM-bpm) < 2 {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		periodFrames := fit.period * FeatureHop
		phase := math.Mod(float64(points[0].Frame)+fit.phase*FeatureHop, periodFrames)
		barPhase, barConfidence := estimateBarPhase(points, periodFrames, phase)
		candidates = append(candidates, TempoCandidate{BPM: bpm, Phase: int64(math.Round(phase)), Confidence: fit.confidence, BarPhase: barPhase, BarConfidence: barConfidence})
		if len(candidates) == 4 {
			break
		}
	}
	if len(candidates) == 0 {
		return Tempo{}
	}
	best := candidates[0]
	return Tempo{BPM: best.BPM, Phase: best.Phase, Confidence: best.Confidence, BarPhase: best.BarPhase, BarConfidence: best.BarConfidence, Candidates: candidates}
}

func pulseEnvelope(points []Point, channel int) []float64 {
	raw := make([]float64, len(points))
	reference := 0.0
	for index, point := range points {
		if channel == 0 {
			raw[index] = point.Onset
			continue
		}
		if index > 0 {
			raw[index] = max(math.Sqrt(max(point.Bands[channel-1], 0))-math.Sqrt(max(points[index-1].Bands[channel-1], 0)), 0)
			reference = max(reference, math.Sqrt(max(point.Bands[channel-1], 0)))
		}
	}
	envelope := make([]float64, len(points))
	maximum := 0.0
	for index := range raw {
		start, end := max(index-15, 0), min(index+16, len(raw))
		var background float64
		for _, value := range raw[start:end] {
			background += value
		}
		envelope[index] = max(raw[index]-background/float64(end-start), 0)
		maximum = max(maximum, envelope[index])
	}
	if maximum < 1e-5 || channel > 0 && maximum < reference*.005 {
		return nil
	}
	for index := range envelope {
		envelope[index] /= maximum
	}
	return envelope
}

func fitPulse(envelope, smoothed []float64, period float64) pulseFit {
	var mean, power float64
	for _, value := range envelope {
		mean += value
		power += value * value
	}
	mean /= float64(len(envelope))
	if mean < 1e-8 || power < 1e-12 {
		return pulseFit{period: period}
	}
	var best pulseFit
	best.period = period
	for phase := 0.; phase < period; phase++ {
		var total float64
		count := 0
		for at := phase; at < float64(len(envelope)); at += period {
			center := int(at)
			fraction := at - float64(center)
			value := smoothed[center]
			if center+1 < len(smoothed) {
				value += fraction * (smoothed[center+1] - value)
			}
			total += value
			count++
		}
		score := total / float64(max(count, 1))
		if score > best.score {
			best.phase, best.score = phase, score
		}
	}
	// 相位拟合必须由逐拍附近的峰值支持，不能只靠整段自相关峰自证可靠。
	var total, jitter float64
	beats, covered := 0, 0
	threshold := max(mean*1.5, .025)
	for at := best.phase; at < float64(len(envelope)); at += period {
		center := int(math.Round(at))
		peak, offset := 0., 0
		for index := max(center-2, 0); index <= min(center+2, len(envelope)-1); index++ {
			value := envelope[index] * math.Exp(-math.Pow((float64(index)-at)/2, 2)*.5)
			if value > peak {
				peak = value
				offset = index - center
			}
		}
		total += peak
		beats++
		if peak >= threshold {
			covered++
			jitter += float64(offset * offset)
		}
	}
	if beats < 8 {
		return pulseFit{period: period}
	}
	gridMean := total / float64(beats)
	contrast := max((gridMean-mean)/(gridMean+mean), 0)
	coverage := float64(covered) / float64(beats)
	lag := int(math.Round(period))
	var product, first, second float64
	for index := lag; index < len(envelope); index++ {
		product += envelope[index] * envelope[index-lag]
		first += envelope[index] * envelope[index]
		second += envelope[index-lag] * envelope[index-lag]
	}
	correlation := product / math.Sqrt(max(first*second, 1e-20))
	precision := math.Exp(-jitter / float64(max(covered, 1)) / 16)
	best.confidence = min(1, coverage*math.Sqrt(contrast*(.5+.5*correlation))*precision)
	var captured, events float64
	for index := 1; index < len(envelope)-1; index++ {
		value := envelope[index]
		if value < threshold || value < envelope[index-1] || value < envelope[index+1] {
			continue
		}
		events += value
		distance := math.Abs(math.Remainder(float64(index)-best.phase, period))
		if distance <= max(2, period*.07) {
			captured += value
		}
	}
	completeness := captured / max(events, 1e-20)
	best.score = gridMean / mean * (.6 + .4*coverage) * (.45 + .55*completeness) * (.85 + .15*math.Exp(-math.Pow(math.Log((6000/period)/120), 2)))
	var phaseShift, phaseWeight float64
	for at := best.phase; at < float64(len(envelope)); at += period {
		center := int(math.Round(at))
		peak, index := 0., center
		for probe := max(center-3, 1); probe <= min(center+3, len(envelope)-2); probe++ {
			if envelope[probe] > peak {
				peak, index = envelope[probe], probe
			}
		}
		if peak < threshold {
			continue
		}
		denominator := envelope[index-1] - 2*envelope[index] + envelope[index+1]
		offset := 0.
		if denominator < -1e-12 {
			offset = .5 * (envelope[index-1] - envelope[index+1]) / denominator
		}
		phaseShift += (float64(index) + offset - at) * peak
		phaseWeight += peak
	}
	if phaseWeight > 0 {
		best.phase = math.Mod(best.phase+phaseShift/phaseWeight+period, period)
	}
	return best
}

func smoothPulseEnvelope(envelope []float64) []float64 {
	smoothed := make([]float64, len(envelope))
	weights := [...]float64{.2493522088, .7066482779, 1, .7066482779, .2493522088}
	for center := range envelope {
		for offset := -2; offset <= 2; offset++ {
			index := center + offset
			if index >= 0 && index < len(envelope) {
				smoothed[center] += envelope[index] * weights[offset+2]
			}
		}
	}
	return smoothed
}

func bassAlignedTempo(points []Point, tempo TempoCandidate, start, end int64) (TempoCandidate, bool) {
	if tempo.BPM <= 0 || tempo.Confidence < .55 {
		return tempo, false
	}
	first := sort.Search(len(points), func(index int) bool { return points[index].Frame >= start })
	last := sort.Search(len(points), func(index int) bool { return points[index].Frame >= end })
	if last-first < 400 {
		return tempo, false
	}
	measured := points[first:last]
	var bassPower, totalPower float64
	for _, point := range measured {
		bassPower += point.Bands[0]
		for _, power := range point.Bands {
			totalPower += power
		}
	}
	if bassPower < float64(len(measured))*.00001 || bassPower < totalPower*.08 {
		return tempo, false
	}
	envelope := pulseEnvelope(measured, 1)
	if len(envelope) == 0 {
		return tempo, false
	}
	period := 60 * SampleRate / tempo.BPM
	fit := fitPulse(envelope, smoothPulseEnvelope(envelope), period/FeatureHop)
	if fit.confidence < .6 {
		return tempo, false
	}
	phase := int64(math.Round(math.Mod(float64(measured[0].Frame)+fit.phase*FeatureHop, period)))
	offset := math.Abs(math.Remainder(float64(phase-tempo.Phase), period))
	// 强底鼓和踩镲可能相差半拍；优先交接区底鼓，不能让两条全频网格自证锁拍。
	if offset > .01*SampleRate {
		tempo.Phase = phase
		if offset > .04*SampleRate {
			// 原小节重音属于旧相位，换网格后没有重新证实就不能沿用。
			tempo.BarConfidence = 0
		}
	}
	return tempo, true
}

func supportsBeatWindow(points []Point, tempo TempoCandidate, start, end int64) bool {
	if tempo.BPM <= 0 || end <= start {
		return false
	}
	first := sort.Search(len(points), func(index int) bool { return points[index].Frame >= start })
	last := sort.Search(len(points), func(index int) bool { return points[index].Frame >= end })
	if last-first < 50 {
		return false
	}
	measured := points[max(first-2, 0):last]
	period := 60 * SampleRate / tempo.BPM
	firstBeat := math.Ceil((float64(start) - float64(tempo.Phase)) / period)
	for channel := 0; channel < 4; channel++ {
		envelope := pulseEnvelope(measured, channel)
		if len(envelope) == 0 {
			continue
		}
		var mean float64
		for _, value := range envelope {
			mean += value
		}
		threshold := max(mean/float64(len(envelope))*1.5, .025)
		beats, supported := 0, 0
		var offsets []float64
		for beat := firstBeat; ; beat++ {
			target := float64(tempo.Phase) + beat*period
			if target >= float64(end) {
				break
			}
			position := (target - float64(measured[0].Frame)) / FeatureHop
			center := int(math.Round(position))
			radius := max(1, int(math.Min(period*.08, .04*SampleRate)/FeatureHop))
			best, peak := center, 0.0
			for index := max(center-radius, 0); index <= min(center+radius, len(envelope)-1); index++ {
				if envelope[index] > peak {
					best, peak = index, envelope[index]
				}
			}
			beats++
			if peak >= threshold {
				supported++
				offsets = append(offsets, (float64(best)-position)*FeatureHop)
			}
		}
		if supported < 3 || float64(supported)/float64(max(beats, 1)) < .6 {
			continue
		}
		// 首尾相位漂移过大时固定变速无法持续锁住，交还安全回退。
		if len(offsets) >= 6 {
			var early, late float64
			for index := 0; index < 3; index++ {
				early += offsets[index]
				late += offsets[len(offsets)-3+index]
			}
			if math.Abs(late-early)/3 > .025*SampleRate {
				continue
			}
		}
		return true
	}
	return false
}

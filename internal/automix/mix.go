package automix

import "math"

const (
	ProfileOriginal = "original"
	ProfileMatched  = "matched"

	bandLow  = 0
	bandMid  = 1
	bandHigh = 2
)

// LayeredGains 先引入新曲的高频节奏，中间集中交接底鼓；弱节奏保持等功率曲线。
func LayeredGains(profile string, progress float64) (old, next [3]float64) {
	progress = min(max(progress, 0), 1)
	for band := range old {
		shifted := progress
		if profile == ProfileMatched {
			switch band {
			case bandLow:
				// 低频仅在交接中点附近换手，避免整段混着两套底鼓和贝斯。
				shifted = min(max((progress-.42)/.16, 0), 1)
				shifted = shifted * shifted * (3 - 2*shifted)
			case bandHigh:
				shifted += .6 * progress * (1 - progress)
			}
		}
		old[band] = math.Cos(shifted * math.Pi / 2)
		next[band] = math.Sin(shifted * math.Pi / 2)
	}
	return old, next
}

type MixEngine struct {
	lowOld, lowNext [2]float32
	midOld, midNext [2]float32
	currentProfile  string
}

func (engine *MixEngine) Mix(profile string, progress float64, old, next [2]float32) [2]float32 {
	return engine.MixWithGains(profile, progress, old, next, 1, 1)
}

func (engine *MixEngine) Reset() {
	engine.lowOld, engine.lowNext = [2]float32{}, [2]float32{}
	engine.midOld, engine.midNext = [2]float32{}, [2]float32{}
	engine.currentProfile = ""
}

func (engine *MixEngine) MixWithGains(profile string, progress float64, old, next [2]float32, oldGain, nextGain float64) [2]float32 {
	oldGains, nextGains := LayeredGains(profile, progress)
	if engine.currentProfile != profile {
		engine.Reset()
		engine.currentProfile = profile
	}
	for channel := range old {
		old[channel] *= float32(oldGain)
		next[channel] *= float32(nextGain)
	}
	lowOld, lowNext := engine.filter(old, next, &engine.lowOld, &engine.lowNext, float32(1-math.Exp(-2*math.Pi*250/SampleRate)))
	upperOld, upperNext := engine.filter(old, next, &engine.midOld, &engine.midNext, float32(1-math.Exp(-2*math.Pi*2500/SampleRate)))
	var result [2]float32
	for channel := range result {
		// 互补频带相加精确还原输入；每轨、每声道独立保存滤波状态。
		value := oldGains[bandLow]*float64(lowOld[channel]) + nextGains[bandLow]*float64(lowNext[channel]) +
			oldGains[bandMid]*float64(upperOld[channel]-lowOld[channel]) + nextGains[bandMid]*float64(upperNext[channel]-lowNext[channel]) +
			oldGains[bandHigh]*float64(old[channel]-upperOld[channel]) + nextGains[bandHigh]*float64(next[channel]-upperNext[channel])
		result[channel] = float32(value)
	}
	// 两声道共同限峰，保留声像比例。
	if peak := max(math.Abs(float64(result[0])), math.Abs(float64(result[1]))); peak > 1 {
		result[0] /= float32(peak)
		result[1] /= float32(peak)
	}
	return result
}

func (engine *MixEngine) filter(old, next [2]float32, oldState, nextState *[2]float32, coefficient float32) (oldFiltered, nextFiltered [2]float32) {
	for channel := range old {
		oldFiltered[channel] = oldState[channel] + coefficient*(old[channel]-oldState[channel])
		nextFiltered[channel] = nextState[channel] + coefficient*(next[channel]-nextState[channel])
		oldState[channel], nextState[channel] = oldFiltered[channel], nextFiltered[channel]
	}
	return oldFiltered, nextFiltered
}

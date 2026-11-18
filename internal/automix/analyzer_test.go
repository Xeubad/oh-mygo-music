package automix

import (
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

// 流式分块不能改变节拍位置，弱尾音也不能被当作歌曲中段的结束。
func TestAnalyzerStreamingFeaturesIgnoreChunkBoundaries(t *testing.T) {
	samples := rhythmFixture(16, 120, .1)
	whole := NewAnalyzer(0)
	whole.Feed(samples)
	chunked := NewAnalyzer(0)
	for offset := 0; offset < len(samples); {
		count := min(1379, len(samples)-offset)
		chunked.Feed(samples[offset : offset+count])
		offset += count
	}
	expected, actual := whole.Snapshot(), chunked.Snapshot()
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal("PCM 分块边界改变了特征")
	}
	if math.Abs(actual.Tempo.BPM-120) > 1 || actual.Tempo.Confidence < .65 {
		t.Fatalf("120 BPM 音乐未锁定: %+v", actual.Tempo)
	}
	period := float64(SampleRate) * .5
	phase := math.Mod(float64(actual.Tempo.Phase)-.1*SampleRate, period)
	phase = math.Min(math.Abs(phase), period-math.Abs(phase))
	if phase > .035*SampleRate {
		t.Fatalf("拍点误差超过 35ms: %v", phase/SampleRate)
	}
	if len(actual.Points) > MaxFeaturePoints || actual.End != 16*SampleRate {
		t.Fatalf("特征窗口/时间轴异常: points=%d end=%d", len(actual.Points), actual.End)
	}
}

// 静音和持续长音不应凭空产生高置信度节奏；分析历史不能随播放时间无限增长。
func TestAnalyzerRejectsUnmeteredAudioAndBoundsHistory(t *testing.T) {
	analyzer := NewAnalyzer(0)
	buffer := make([]float32, SampleRate*2)
	for second := range 70 {
		for frame := range SampleRate {
			value := float32(.2 * math.Sin(2*math.Pi*440*float64(second*SampleRate+frame)/SampleRate))
			buffer[frame*2], buffer[frame*2+1] = value, -value
		}
		analyzer.Feed(buffer)
	}
	snapshot := analyzer.Snapshot()
	if snapshot.Tempo.Confidence >= .4 || len(snapshot.Points) > MaxFeaturePoints {
		t.Fatalf("无节奏误锁定或历史无界: tempo=%+v points=%d", snapshot.Tempo, len(snapshot.Points))
	}
	analyzer.Feed(make([]float32, SampleRate*2*8))
	if tempo := analyzer.Snapshot().Tempo; tempo.Confidence != 0 {
		t.Fatalf("八秒静音未失锁: %+v", tempo)
	}
}

// 非整数 BPM 不能被取整或误判半速后仍给出高置信度；拍点以合成音乐真值核对。
func TestAnalyzerFollowsFractionalTempos(t *testing.T) {
	for _, bpm := range []float64{83, 97, 118.4, 124, 151} {
		analyzer := NewAnalyzer(0)
		analyzer.Feed(rhythmFixture(18, bpm, .13))
		tempo := analyzer.Snapshot().Tempo
		if math.Abs(tempo.BPM-bpm) > .7 || tempo.Confidence < .6 {
			t.Fatalf("BPM=%v 检测=%+v", bpm, tempo)
		}
		period := 60 * SampleRate / bpm
		for beat := math.Ceil((12 - .13) * bpm / 60); .13+beat*60/bpm < 18; beat++ {
			expected := (.13 + beat*60/bpm) * SampleRate
			nearest := float64(tempo.Phase) + math.Round((expected-float64(tempo.Phase))/(60*SampleRate/tempo.BPM))*(60*SampleRate/tempo.BPM)
			if math.Abs(nearest-expected) > min(.035*SampleRate, period/10) {
				t.Fatalf("BPM=%v 拍点漂移: %.3fms", bpm, (nearest-expected)/48)
			}
		}
	}
}

// 稳定的四拍重音应被识别为乐句起点，均匀节拍不应伪造乐句置信度。
func TestAnalyzerFindsConfidentBarPhaseOnlyWithRepeatedAccent(t *testing.T) {
	accented := NewAnalyzer(0)
	accented.Feed(barRhythmFixture(18, 120, .13, true))
	accentedTempo := accented.Snapshot().Tempo
	if accentedTempo.BarConfidence < .18 || accentedTempo.BarPhase != 0 {
		t.Fatalf("重复四拍重音未识别: %+v", accentedTempo)
	}
	flat := NewAnalyzer(0)
	flat.Feed(barRhythmFixture(18, 120, .13, false))
	flatTempo := flat.Snapshot().Tempo
	if flatTempo.BarConfidence >= .15 {
		t.Fatalf("均匀节拍被误判成乐句边界: %+v", flatTempo)
	}
}

// 真实编曲中的切分和伴奏不能把稳定鼓点的置信度压到无法锁拍。
func TestAnalyzerTracksSyncopatedMusicWithoutDemandingPurePulse(t *testing.T) {
	for _, bpm := range []float64{95, 126, 168} {
		samples := syncopatedFixture(16, bpm)
		analyzer := NewAnalyzer(0)
		analyzer.Feed(samples)
		tempo := analyzer.Snapshot().Tempo
		found := false
		for _, candidate := range tempo.Candidates {
			if math.Abs(candidate.BPM-bpm) <= 1 && candidate.Confidence >= .55 {
				found = true
				period := 60 * SampleRate / candidate.BPM
				for beat := 10.; beat < 20; beat++ {
					expected := (.13 + beat*60/bpm) * SampleRate
					nearest := float64(candidate.Phase) + math.Round((expected-float64(candidate.Phase))/period)*period
					if math.Abs(nearest-expected) > .04*SampleRate {
						t.Fatalf("BPM=%v 锁拍候选相位偏差 %.1fms: %+v", bpm, (nearest-expected)/48, candidate)
					}
				}
			}
		}
		if !found {
			t.Fatalf("切分伴奏淹没了稳定鼓点 BPM=%v: %+v", bpm, tempo)
		}
	}
}

// 拍速持续变化时不能把局部重复冒充可固定变速锁定的稳定拍网格。
func TestAnalyzerRejectsContinuouslyChangingBeatPeriod(t *testing.T) {
	samples := make([]float32, 16*SampleRate*2)
	for beatTime := .13; beatTime < 16; {
		bpm := 85 + 5*beatTime
		for offset := 0; offset < int(.09*SampleRate); offset++ {
			frame := int(beatTime*SampleRate) + offset
			if frame >= len(samples)/2 {
				break
			}
			at := float64(offset) / SampleRate
			value := float32(.7 * math.Exp(-at*55) * math.Sin(2*math.Pi*82*at))
			samples[frame*2], samples[frame*2+1] = value, value
		}
		beatTime += 60 / bpm
	}
	analyzer := NewAnalyzer(0)
	analyzer.Feed(samples)
	tempo := analyzer.Snapshot().Tempo
	if tempo.Confidence >= .55 {
		t.Fatalf("变速鼓点被误判为固定网格: %+v", tempo)
	}
}

// 随机打击声虽有很多强起音，也不能被强行拟合成可锁拍的规则节奏。
func TestAnalyzerRejectsIrregularPercussiveEvents(t *testing.T) {
	random := rand.New(rand.NewPCG(17, 43))
	samples := make([]float32, 16*SampleRate*2)
	for event := .1; event < 16; event += .17 + random.Float64()*.63 {
		amplitude := .2 + random.Float64()*.5
		for offset := 0; offset < SampleRate/12; offset++ {
			frame := int(event*SampleRate) + offset
			if frame >= len(samples)/2 {
				break
			}
			at := float64(offset) / SampleRate
			value := float32(amplitude * math.Exp(-at*55) * math.Sin(2*math.Pi*110*at))
			samples[frame*2], samples[frame*2+1] = value, value
		}
	}
	analyzer := NewAnalyzer(0)
	analyzer.Feed(samples)
	for _, candidate := range analyzer.Snapshot().Tempo.Candidates {
		if candidate.Confidence >= .55 {
			t.Fatalf("不规则事件被强行锁拍: %+v", candidate)
		}
	}
}

func syncopatedFixture(seconds int, bpm float64) []float32 {
	samples := rhythmFixture(seconds, bpm, .13)
	period := 60 / bpm
	for frame := 0; frame < len(samples)/2; frame++ {
		at := float64(frame) / SampleRate
		eighth := math.Mod(at-.13+period, period/2)
		offbeat := math.Mod(at-.13+period*.75, period)
		value := .07 * math.Sin(2*math.Pi*(440+25*math.Sin(at*2.7))*at)
		value += .15 * math.Exp(-eighth*90) * math.Sin(2*math.Pi*7500*at)
		value += .28 * math.Exp(-offbeat*45) * math.Sin(2*math.Pi*1100*at)
		gain := .45 + .55*(.5+.5*math.Sin(at*.71))
		samples[frame*2] = samples[frame*2]*float32(gain) + float32(value)
		samples[frame*2+1] = samples[frame*2+1]*float32(gain) + float32(value*.7)
	}
	return samples
}

func rhythmFixture(seconds int, bpm, phase float64) []float32 {
	samples := make([]float32, seconds*SampleRate*2)
	period := 60 / bpm
	for frame := 0; frame < len(samples)/2; frame++ {
		at := float64(frame) / SampleRate
		distance := math.Mod(at-phase+period, period)
		value := .025 * math.Sin(2*math.Pi*330*at)
		if at >= phase && distance < .09 {
			value += .7 * math.Exp(-distance*55) * (math.Sin(2*math.Pi*82*at) + .3*math.Sin(2*math.Pi*1700*at))
		}
		samples[frame*2], samples[frame*2+1] = float32(value), float32(value*.8)
	}
	return samples
}

func barRhythmFixture(seconds int, bpm, phase float64, accented bool) []float32 {
	samples := make([]float32, seconds*SampleRate*2)
	period := 60 / bpm
	for frame := 0; frame < len(samples)/2; frame++ {
		at := float64(frame) / SampleRate
		beatPosition := (at - phase) / period
		beatIndex := int(math.Round(beatPosition))
		distance := math.Abs(at - (phase + float64(beatIndex)*period))
		value := .025 * math.Sin(2*math.Pi*330*at)
		if at >= phase && distance < .09 {
			strength := .7
			if accented && beatIndex%4 == 0 {
				strength = 1.1
			}
			value += strength * math.Exp(-distance*55) * (math.Sin(2*math.Pi*82*at) + .3*math.Sin(2*math.Pi*1700*at))
		}
		samples[frame*2], samples[frame*2+1] = float32(value), float32(value*.8)
	}
	return samples
}

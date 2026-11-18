package automix

import (
	"math"
	"math/cmplx"
	"sort"
	"sync"
)

const (
	SampleRate       = 48000
	FeatureHop       = 480
	MaxFeaturePoints = 2400
	analysisFFT      = 1024
)

type Tempo struct {
	BPM           float64          `json:"bpm"`
	Phase         int64            `json:"phase_frame"`
	Confidence    float64          `json:"confidence"`
	BarPhase      int              `json:"bar_phase"`
	BarConfidence float64          `json:"bar_confidence"`
	Candidates    []TempoCandidate `json:"candidates,omitempty"`
}

type TempoCandidate struct {
	BPM           float64 `json:"bpm"`
	Phase         int64   `json:"phase_frame"`
	Confidence    float64 `json:"confidence"`
	BarPhase      int     `json:"bar_phase"`
	BarConfidence float64 `json:"bar_confidence"`
}

type Point struct {
	Frame  int64       `json:"frame"`
	Energy float64     `json:"energy"`
	Onset  float64     `json:"onset"`
	Bands  [3]float64  `json:"bands"`
	Chroma [12]float64 `json:"chroma"`
}

// Features 仅含聚合声学特征；不能携带 PCM、歌曲身份、URL 或来源凭据。
type Features struct {
	Start  int64   `json:"start_frame"`
	End    int64   `json:"end_frame"`
	Tempo  Tempo   `json:"tempo"`
	Points []Point `json:"points"`
}

// Analyzer 在解码工作线程接收样本，快照可在计划工作线程读取，内存不随曲长增长。
type Analyzer struct {
	mu       sync.Mutex
	origin   int64
	frame    int64
	left     float32
	haveLeft bool
	pcm      [analysisFFT]complex128
	window   [analysisFFT]float64
	work     [analysisFFT]complex128
	previous [analysisFFT / 2]float64
	energy   float64
	points   [MaxFeaturePoints]Point
	count    int64
}

func NewAnalyzer(origin int64) *Analyzer {
	analyzer := &Analyzer{origin: origin, frame: origin}
	for index := range analyzer.window {
		analyzer.window[index] = .5 - .5*math.Cos(2*math.Pi*float64(index)/float64(analysisFFT-1))
	}
	return analyzer
}

func (analyzer *Analyzer) Feed(samples []float32) {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	for _, value := range samples {
		if !analyzer.haveLeft {
			analyzer.left, analyzer.haveLeft = value, true
			continue
		}
		left, right := float64(analyzer.left), float64(value)
		analyzer.haveLeft = false
		if math.IsNaN(left) || math.IsInf(left, 0) || math.IsNaN(right) || math.IsInf(right, 0) {
			left, right = 0, 0
		}
		offset := analyzer.frame - analyzer.origin
		analyzer.pcm[offset%analysisFFT] = complex(left, right)
		analyzer.energy += (left*left + right*right) / 2
		analyzer.frame++
		if (offset+1)%FeatureHop == 0 {
			analyzer.measure()
		}
	}
}

func (analyzer *Analyzer) measure() {
	end := analyzer.frame - analyzer.origin
	for index := range analyzer.work {
		position := end - analysisFFT + int64(index)
		value := complex(0, 0)
		if position >= 0 {
			value = analyzer.pcm[position%analysisFFT]
		}
		analyzer.work[index] = value * complex(analyzer.window[index], 0)
	}
	transform(analyzer.work[:])
	point := Point{Frame: analyzer.frame - FeatureHop, Energy: math.Sqrt(analyzer.energy / FeatureHop)}
	analyzer.energy = 0
	for bin := 1; bin < analysisFFT/2; bin++ {
		// 两声道独立求功率，反相立体声不能被错误当作静音。
		value, mirrored := analyzer.work[bin], cmplx.Conj(analyzer.work[analysisFFT-bin])
		left, right := (value+mirrored)/2, (value-mirrored)/complex(0, 2)
		power := (real(left)*real(left) + imag(left)*imag(left) + real(right)*real(right) + imag(right)*imag(right)) / (analysisFFT * analysisFFT)
		magnitude := math.Log1p(30 * math.Sqrt(power))
		point.Onset += max(magnitude-analyzer.previous[bin], 0)
		analyzer.previous[bin] = magnitude
		frequency := float64(bin) * SampleRate / analysisFFT
		band := 2
		if frequency < 250 {
			band = 0
		} else if frequency < 2500 {
			band = 1
		}
		point.Bands[band] += power
		if frequency >= 80 && frequency <= 4000 {
			pitch := int(math.Round(69+12*math.Log2(frequency/440))) % 12
			point.Chroma[pitch] += power
		}
	}
	point.Onset /= analysisFFT / 2
	analyzer.points[analyzer.count%MaxFeaturePoints] = point
	analyzer.count++
}

func (analyzer *Analyzer) Snapshot() Features {
	analyzer.mu.Lock()
	snapshot := Features{Start: analyzer.origin, End: analyzer.frame}
	count := min(analyzer.count, MaxFeaturePoints)
	snapshot.Points = make([]Point, count)
	for index := range count {
		snapshot.Points[index] = analyzer.points[(analyzer.count-count+index)%MaxFeaturePoints]
	}
	analyzer.mu.Unlock()
	snapshot.Tempo = estimateTempo(snapshot.Points)
	return snapshot
}

func estimateTempo(points []Point) Tempo {
	return trackTempo(points)
}

func estimateBarPhase(points []Point, beatPeriod float64, beatPhase float64) (int, float64) {
	if len(points) < 800 || beatPeriod <= 0 {
		return 0, 0
	}
	firstBeat := int64(math.Ceil((float64(points[0].Frame) - beatPhase) / beatPeriod))
	lastBeat := int64(math.Floor((float64(points[len(points)-1].Frame) - beatPhase) / beatPeriod))
	if lastBeat-firstBeat < 15 {
		return 0, 0
	}
	var totals [4]float64
	var counts [4]int
	window := beatPeriod * .16
	for beat := firstBeat; beat <= lastBeat; beat++ {
		target := beatPhase + float64(beat)*beatPeriod
		strength := 0.0
		start := sort.Search(len(points), func(index int) bool {
			return float64(points[index].Frame) >= target-window
		})
		for index := start; index < len(points) && float64(points[index].Frame) <= target+window; index++ {
			point := points[index]
			if math.Abs(float64(point.Frame)-target) <= window {
				strength = max(strength, point.Onset)
			}
		}
		barBeat := int((beat%4 + 4) % 4)
		totals[barBeat] += strength
		counts[barBeat]++
	}
	var averages [4]float64
	for beat := range averages {
		if counts[beat] < 2 {
			return 0, 0
		}
		averages[beat] = totals[beat] / float64(counts[beat])
	}
	phase := 0
	for beat := 1; beat < len(averages); beat++ {
		if averages[beat] > averages[phase] {
			phase = beat
		}
	}
	second := 0.0
	for beat, average := range averages {
		if beat != phase {
			second = max(second, average)
		}
	}
	confidence := (averages[phase] - second) / max(averages[phase], 1e-12)
	return phase, min(max(confidence, 0), 1)
}

func transform(values []complex128) {
	for index, reverse := 1, 0; index < len(values); index++ {
		bit := len(values) >> 1
		for ; reverse&bit != 0; bit >>= 1 {
			reverse ^= bit
		}
		reverse ^= bit
		if index < reverse {
			values[index], values[reverse] = values[reverse], values[index]
		}
	}
	for size := 2; size <= len(values); size <<= 1 {
		step := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < len(values); start += size {
			phase := complex(1, 0)
			for offset := range size / 2 {
				first, second := values[start+offset], values[start+offset+size/2]*phase
				values[start+offset], values[start+offset+size/2] = first+second, first-second
				phase *= step
			}
		}
	}
}

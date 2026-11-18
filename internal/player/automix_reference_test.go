package player

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mygo-music/internal/automix"
)

// 仓库外的真实音乐用于核对变速能量与音色分布，既不播放也不上传音频。
func TestAutoMixReferenceMusic(t *testing.T) {
	directory := os.Getenv("MM_AUTOMIX_AUDIO")
	if directory == "" {
		t.Skip("set MM_AUTOMIX_AUDIO to an external directory with reference WAV files")
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	tested := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".wav") {
			continue
		}
		tested++
		t.Run(file.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(directory, file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := openStream(downloaded(data), 0)
			if err != nil {
				t.Fatal(err)
			}
			source := newTransitionDecoder(decoded)
			defer source.Close()
			var samples []float32
			chunk := make([]float32, 2048)
			for len(samples) < 24*automix.SampleRate*2 {
				count, err := source.Read(chunk)
				samples = append(samples, chunk[:count]...)
				if err == io.EOF {
					break
				}
				if err != nil || count == 0 {
					t.Fatalf("音乐解码停止: %d %v", count, err)
				}
			}
			for _, ratio := range []float64{.95, 1.05} {
				output, mapping, err := automix.StretchIntro(samples, ratio, automix.SampleRate)
				if err != nil {
					t.Fatal(err)
				}
				original, processed := automix.NewAnalyzer(0), automix.NewAnalyzer(0)
				original.Feed(samples)
				processed.Feed(output)
				first, second := original.Snapshot(), processed.Snapshot()
				var firstChroma, secondChroma [12]float64
				var beforeEnergy, afterEnergy float64
				for _, point := range first.Points {
					beforeEnergy += point.Energy * point.Energy
					for pitch := range firstChroma {
						firstChroma[pitch] += point.Chroma[pitch]
					}
				}
				for _, point := range second.Points {
					afterEnergy += point.Energy * point.Energy
					for pitch := range secondChroma {
						secondChroma[pitch] += point.Chroma[pitch]
					}
				}
				var product, firstPower, secondPower float64
				for pitch := range firstChroma {
					product += firstChroma[pitch] * secondChroma[pitch]
					firstPower += firstChroma[pitch] * firstChroma[pitch]
					secondPower += secondChroma[pitch] * secondChroma[pitch]
				}
				timbre := product / math.Sqrt(firstPower*secondPower)
				energyDB := 10 * math.Log10(afterEnergy/beforeEnergy)
				if math.IsNaN(timbre) || timbre < .94 || math.Abs(energyDB) > 2 {
					t.Fatalf("变速破坏音色或能量: ratio=%.3f chroma=%.5f energy=%.3fdB", ratio, timbre, energyDB)
				}
				if mapping.SourceFrame(int64(len(output)/2)) != int64(len(samples)/2) {
					t.Fatal("真实音乐变速丢失源时间")
				}
				t.Logf("speed=%.3f frames=%d->%d chroma=%.5f energy=%.3fdB tempo=%.2f confidence=%.3f", ratio, len(samples)/2, len(output)/2, timbre, energyDB, first.Tempo.BPM, first.Tempo.Confidence)
				if frames := os.Getenv("MM_FRAMES"); frames != "" {
					writeAutoMixWave(t, filepath.Join(frames, strings.TrimSuffix(file.Name(), ".wav")+"-speed-"+map[bool]string{true: "105", false: "095"}[ratio > 1]+".wav"), output)
				}
			}
		})
	}
	if tested == 0 {
		t.Fatal("reference directory contains no WAV files")
	}
}

func writeAutoMixWave(t *testing.T, path string, samples []float32) {
	t.Helper()
	data := make([]byte, 44+len(samples)*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 2)
	binary.LittleEndian.PutUint32(data[24:], automix.SampleRate)
	binary.LittleEndian.PutUint32(data[28:], automix.SampleRate*4)
	binary.LittleEndian.PutUint16(data[32:], 4)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(samples)*2))
	for index, sample := range samples {
		value := max(-1, min(1, sample))
		binary.LittleEndian.PutUint16(data[44+index*2:], uint16(int16(value*32767)))
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

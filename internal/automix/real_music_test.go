package automix

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// 仓库外的真实声学特征必须能恢复独立参考拍速，避免只在合成鼓点上通过。
func TestRealMusicBeatReferences(t *testing.T) {
	directory := os.Getenv("MM_REAL_BEATS")
	if directory == "" {
		t.Skip("set MM_REAL_BEATS to a local feature/reference validation directory")
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "validation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name        string   `json:"name"`
		Features    Features `json:"features"`
		ExpectedBPM float64  `json:"expected_bpm"`
	}
	if err := json.Unmarshal(encoded, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 4 {
		t.Fatal("real validation requires several distinct segments")
	}
	for _, scenario := range cases {
		t.Run(scenario.Name, func(t *testing.T) {
			tempo := estimateTempo(scenario.Features.Points)
			// 参考由独立跟踪器给出；半倍速歧义可接受，三连音速度不能冒充相容。
			compatible := false
			for _, multiplier := range []float64{.5, 1, 2} {
				if math.Abs(tempo.BPM-scenario.ExpectedBPM*multiplier) < 1.5 {
					compatible = true
				}
			}
			if !compatible || tempo.Confidence < .55 {
				t.Fatalf("real reference failed: bpm=%.3f expected=%.3f confidence=%.3f", tempo.BPM, scenario.ExpectedBPM, tempo.Confidence)
			}
		})
	}
}

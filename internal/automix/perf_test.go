package automix

import "testing"

func BenchmarkAnalyzeMusic(b *testing.B) {
	samples := rhythmFixture(24, 118.4, .13)
	b.SetBytes(int64(len(samples) * 4))
	b.ResetTimer()
	for range b.N {
		analyzer := NewAnalyzer(0)
		analyzer.Feed(samples)
		_ = analyzer.Snapshot()
	}
}

func BenchmarkStretchMusic(b *testing.B) {
	samples := rhythmFixture(16, 118.4, .13)
	b.SetBytes(int64(len(samples) * 4))
	b.ResetTimer()
	for range b.N {
		if _, _, err := StretchIntro(samples, 1.04, 8*SampleRate); err != nil {
			b.Fatal(err)
		}
	}
}

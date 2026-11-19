package player_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mygo-music/internal/music"
)

// AAC 必须依据实际编码载荷放行，服务虚报的类型和档位不能改变保存或开播结果。
func TestCooperativeAACMeasuredQuality(t *testing.T) {
	for _, tc := range []struct {
		name, quality string
		min, max      int
	}{
		{"seek.aac", "128k", 128, 140},
		{"seek-aac.m4a", "128k", 128, 140},
		{"seek-aac-frag.m4a", "128k", 128, 140},
		{"seek.m4a", "lossless", 0, 0},
		{"seek-flac-frag.m4a", "lossless", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := tc.name
			src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){ return {targetQuality:%q,minimumQuality:%q,candidates:[{id:"aac",platform:"qs",trackID:"one",quality:%q}]}; }
export function resolveCandidate(){ return {stream:{url:%s,type:"mp3",quality:"flac",bitRate:999}}; }
`, tc.quality, tc.quality, tc.quality, audioPath(t, name)))
			p, posts := cooperativePlayer(src)
			track := music.Track{ID: "one", Source: "cooperative"}
			song, err := p.FindSong(src, track, "standard")
			if err != nil {
				t.Fatal(err)
			}
			defer song.Close()
			kind := filepath.Ext(name)[1:]
			if song.Type != kind {
				t.Fatalf("真实保存类型错误：%q，期望%q", song.Type, kind)
			}
			path := filepath.Join(t.TempDir(), "saved."+kind)
			if err := song.SaveTo(path); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("音质探测改变了保存的原始媒体")
			}
			p.PlayTrack([]music.Track{track}, 0)
			finishLoading(t, p, posts)
			defer func() {
				if p.Playing() || p.Loading() {
					p.TogglePlay()
				}
			}()
			st := p.Stream()
			if !p.Ready() || !p.Playing() || p.Err() != "" || st == nil || st.Type != kind || st.Quality != tc.quality || st.BitRate < tc.min || st.BitRate > tc.max || p.Current().ID != "one" {
				t.Fatalf("AAC实际音质或首次开播错误：stream=%+v error=%q", st, p.Err())
			}
		})
	}
}

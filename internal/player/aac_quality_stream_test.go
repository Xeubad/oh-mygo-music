package player_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mygo-music/internal/music"
)

// 有界前缀足以核验就不能等整首；证据不足时取消必须终止真实的本地HTTP等待。
func TestCooperativeAACQualityStreaming(t *testing.T) {
	for _, tc := range []struct {
		name         string
		full, prefix []byte
		cancel       bool
	}{
		{"adts-bounded", qualityFixture(t, "seek.aac"), adtsPrefix(t, qualityFixture(t, "seek.aac"), 128), false},
		{"adts-cancel", qualityFixture(t, "seek.aac"), adtsPrefix(t, qualityFixture(t, "seek.aac"), 16), true},
		{"m4a-cancel", qualityFixture(t, "seek-aac-frag.m4a"), qualityFixture(t, "seek-aac-frag.m4a")[:10<<10], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered, canceled := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(tc.full)))
				w.Write(tc.prefix)
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			defer server.CloseClientConnections()
			src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){return {minimumQuality:"128k",candidates:[{id:"one",platform:"qs",trackID:"raw",quality:"128k"}]};}
export function resolveCandidate(){return {stream:{url:%q,type:"flac",quality:"flac"}};}
`, server.URL))
			p, _ := cooperativePlayer(src)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				song, err := p.FindSongContext(ctx, src, music.Track{ID: "one", Source: "cooperative"}, "standard")
				if song != nil {
					song.Close()
				}
				done <- err
			}()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-entered:
			case <-timer.C:
				t.Fatal("没有进入本地媒体请求")
			}
			if tc.cancel {
				cancel()
			}
			select {
			case err := <-done:
				if tc.cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("取消未阻止候选返回: %v", err)
				}
				if !tc.cancel && err != nil {
					t.Fatalf("完整采样窗口未放行: %v", err)
				}
			case <-timer.C:
				t.Fatal("音质核验仍在等待未交付的尾部")
			}
			select {
			case <-canceled:
			case <-timer.C:
				t.Fatal("媒体请求没有取消")
			}
		})
	}
}

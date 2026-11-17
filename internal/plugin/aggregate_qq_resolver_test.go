package plugin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// QQ 使用原通用服务的 GET 契约和 songmid，不发送网易专用密钥、Cookie 或内部目录 ID。
func TestAggregateQQResolverUsesGeneralKeyAndSongMID(t *testing.T) {
	oldStore, oldTransport := systemCredentials, http.DefaultTransport
	store := &memoryCredentials{values: map[string]string{}}
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = oldStore; http.DefaultTransport = oldTransport })
	calls := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Hostname() != "source.shiqianjiang.cn" || r.URL.Path != "/api/music/url" {
			t.Error("QQ 解析没有沿用已核实的通用 GET 服务")
		}
		q := r.URL.Query()
		if len(q) != 3 || q.Get("source") != "tx" || q.Get("songId") != "FixtureSongA1" || q.Get("quality") != "128k" {
			t.Error("QQ 解析没有发送原始 songmid 和档位")
		}
		if r.Header.Get("X-API-Key") != "fixture-general-key" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Request-ID") == "" {
			t.Error("QQ 解析凭据、匿名隔离或请求身份错误")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"0","url":"https://audio.example.test/qq-media"}`)), Request: r}, nil
	})
	p := loadAggregate(t)
	for _, input := range []map[string]any{{"value": "fixture-general-key", "temporary": true}, {"platform": "wy", "value": "fixture-wy-key", "temporary": true}} {
		if _, err := p.Aggregate("setResolverKey", input); err != nil {
			t.Fatal(err)
		}
	}
	id := "catalog:tx:track:FixtureSongA1"
	plan, err := p.PlaybackPlan(context.Background(), id, "high", "qq-public")
	if err != nil || plan == nil || len(plan.Candidates) != 3 {
		t.Fatalf("QQ 目录未建立声明档位候选：%v", err)
	}
	for i, q := range []string{"flac", "320k", "128k"} {
		c := plan.Candidates[i]
		if c.Platform != "tx" || c.TrackID != "FixtureSongA1" || c.AccountID != "public" || c.PlaybackID != id || c.Quality != q {
			t.Fatal("QQ 候选身份或音质路径错误")
		}
	}
	result, err := p.ResolveCandidate(context.Background(), id, "qq-public", plan.Candidates[2])
	if err != nil {
		t.Fatalf("QQ 播放解析未接入：%v", err)
	}
	if calls != 1 || result.Platform != "tx" || result.RequestedQuality != "128k" || result.Stream.URL != "https://audio.example.test/qq-media" {
		t.Fatal("QQ 解析结果身份丢失")
	}
	if result.Stream.Quality != "" || result.Stream.BitRate != 0 || len(result.Stream.Headers) != 0 {
		t.Fatal("QQ 请求档位冒充实际质量或密钥被转发到媒体")
	}
}

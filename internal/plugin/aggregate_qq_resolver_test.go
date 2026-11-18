package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// QQ 新源使用 POST 与独立 tx 密钥；旧通用密钥、网易密钥和账号 Cookie 都不能带入请求。
func TestAggregateQQResolverUsesDedicatedKeyAndSongMID(t *testing.T) {
	oldStore, oldTransport := systemCredentials, http.DefaultTransport
	store := &memoryCredentials{values: map[string]string{}}
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = oldStore; http.DefaultTransport = oldTransport })
	calls := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Scheme != "https" || r.URL.Host != "c.wwwweb.top" || r.URL.Path != "/music/url" || r.URL.RawQuery != "" {
			t.Error("QQ 解析没有采用已核实的新源 POST 协议")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 3 || body["source"] != "tx" || body["musicId"] != "FixtureSongA1" || body["quality"] != "128k" {
			t.Error("QQ 解析没有在请求体发送原始 songmid 和档位")
		}
		if r.Header.Get("X-Api-Key") != "fixture-qq-new-key" || r.Header.Get("Cookie") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("QQ 解析凭据或匿名隔离错误")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"url":"https://audio.example.test/qq-media"}`)), Request: r}, nil
	})
	p := loadAggregate(t)
	for _, input := range []map[string]any{
		{"value": "fixture-old-general-key", "temporary": true},
		{"platform": "wy", "value": "fixture-wy-key", "temporary": true},
		{"platform": "tx", "value": "fixture-qq-new-key", "temporary": true},
	} {
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

package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// QQ 账号快照经冻结身份进入专用 POST 解析，退出后同一候选不能重新请求。
func TestAggregateQQAccountSnapshotResolvesFrozenCandidate(t *testing.T) {
	p, _ := qqAccountTest(t, func(*http.Request, string) (string, error) {
		return qqFixtureSongs(1, 0, qqFixtureSong("FixtureSongA1")), nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Liked(0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("setResolverKey", map[string]any{"platform": "tx", "value": "fixture-qq-key", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	home, err := p.Home()
	if err != nil || len(home.Sections) != 1 || len(home.Sections[0].Tracks) != 1 {
		t.Fatalf("缺少 QQ 聚合曲目：%v", err)
	}
	id := home.Sections[0].Tracks[0].ID
	plan, err := p.PlaybackPlan(context.Background(), id, "high", "qq-account-round")
	if err != nil || plan == nil || len(plan.Candidates) != 3 {
		t.Fatalf("QQ 账号快照未生成解析候选：%v", err)
	}
	candidate := plan.Candidates[2]
	if candidate.Platform != "tx" || candidate.AccountID != "700" || candidate.TrackID != "FixtureSongA1" || candidate.Quality != "128k" {
		t.Fatal("冻结候选丢失来源身份或档位")
	}
	previous := http.DefaultTransport
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "c.wwwweb.top" {
			return previous.RoundTrip(r)
		}
		requests++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != http.MethodPost || r.URL.Path != "/music/url" || r.URL.RawQuery != "" || len(body) != 3 || body["source"] != "tx" || body["musicId"] != "FixtureSongA1" || body["quality"] != "128k" || r.Header.Get("X-Api-Key") != "fixture-qq-key" || r.Header.Get("Cookie") != "" {
			t.Error("账号解析越过QQ新源、songmid或凭据隔离边界")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"url":"https://audio.example.test/qq-media"}`)), Request: r}, nil
	})
	stream, err := p.ResolveCandidate(context.Background(), id, "qq-account-round", candidate)
	if err != nil || stream == nil || stream.Platform != "tx" || stream.RequestedQuality != "128k" || stream.Stream.URL != "https://audio.example.test/qq-media" {
		t.Fatalf("QQ 账号曲目未进入专用解析：%v", err)
	}
	if stream.Stream.Quality != "" || len(stream.Stream.Headers) != 0 {
		t.Fatal("解析结果冒充实际音质或带出鉴权")
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ResolveCandidate(context.Background(), id, "qq-account-round", candidate); err == nil {
		t.Fatal("退出后冻结候选仍可解析")
	}
	if requests != 1 {
		t.Fatal("退出后的旧候选继续请求了解析服务")
	}
}

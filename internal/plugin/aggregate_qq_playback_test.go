package plugin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// QQ 账号喜欢快照经聚合冻结身份进入通用解析，主动退出后同一候选不可重新请求解析。
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
	if _, err := p.Aggregate("setResolverKey", map[string]any{"value": "fixture-general-key", "temporary": true}); err != nil {
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
		t.Fatal("聚合冻结候选丢失真实来源或音质路径")
	}
	previous := http.DefaultTransport
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "source.shiqianjiang.cn" {
			return previous.RoundTrip(r)
		}
		requests++
		if r.Method != http.MethodGet || r.URL.Query().Get("source") != "tx" || r.URL.Query().Get("songId") != "FixtureSongA1" || r.Header.Get("X-API-Key") != "fixture-general-key" || r.Header.Get("Cookie") != "" {
			t.Error("账号解析越过通用服务、songmid 或凭据隔离边界")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"url":"https://audio.example.test/qq-media"}`)), Request: r}, nil
	})
	stream, err := p.ResolveCandidate(context.Background(), id, "qq-account-round", candidate)
	if err != nil || stream == nil || stream.Platform != "tx" || stream.RequestedQuality != "128k" || stream.Stream.URL != "https://audio.example.test/qq-media" {
		t.Fatalf("QQ 账号曲目未进入通用解析：%v", err)
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

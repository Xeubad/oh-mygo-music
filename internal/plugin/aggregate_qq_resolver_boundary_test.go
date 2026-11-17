package plugin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"mygo-music/internal/music"
)

func TestAggregateQQResolverNeverFallsBackToNetEaseKey(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("缺少通用密钥时不应发请求")
		return nil, errors.New("unexpected")
	})
	p := loadAggregate(t)
	if _, err := p.Aggregate("setResolverKey", map[string]any{"platform": "wy", "value": "fixture-wy-only", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	id := "catalog:tx:track:FixtureSongA1"
	plan, err := p.PlaybackPlan(context.Background(), id, "high", "missing-qq")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.ResolveCandidate(context.Background(), id, "missing-qq", plan.Candidates[0]); !errors.Is(err, music.ErrSetup) {
		t.Fatal("QQ 没有独立报告通用密钥配置缺失")
	}
}

// 原始服务文案不得回显，403/429 不能被推断成全平台禁播；无效媒体拒绝交给播放器。
func TestAggregateQQResolverRejectsUnsafeResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		setup  bool
	}{
		{"auth", 403, `{"code":403,"message":"fixture-qq-secret https://example.test/token"}`, true},
		{"rate", 429, `{"code":429,"message":"fixture-qq-secret"}`, false},
		{"missing-code", 200, `{"url":"https://audio.example.test/media"}`, false},
		{"null-code", 200, `{"code":null,"url":"https://audio.example.test/media"}`, false},
		{"no-url", 200, `{"code":200}`, false},
		{"not-json", 200, `fixture-qq-secret`, false},
		{"not-http", 200, `{"code":0,"url":"file:///private/audio"}`, false},
		{"userinfo", 200, `{"code":200,"url":"https://fixture-qq-secret@audio.example.test/media"}`, false},
		{"failure", 200, `{"code":500,"message":"fixture-qq-secret https://example.test/token"}`, false},
		{"http-error", 500, `{"code":0,"url":"https://audio.example.test/media"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			p := loadAggregate(t)
			if _, err := p.Aggregate("setResolverKey", map[string]any{"value": "fixture-qq-secret", "temporary": true}); err != nil {
				t.Fatal(err)
			}
			id := "catalog:tx:track:FixtureSongA1"
			plan, err := p.PlaybackPlan(context.Background(), id, "high", "qq-failure")
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.ResolveCandidate(context.Background(), id, "qq-failure", plan.Candidates[0])
			if err == nil || got != nil || errors.Is(err, music.ErrPlaybackBlocked) || errors.Is(err, music.ErrSetup) != tc.setup {
				t.Fatal("QQ 服务失败或错误范围处理不正确")
			}
			if strings.Contains(err.Error(), "fixture-qq-secret") || strings.Contains(err.Error(), "https://example.test") {
				t.Fatal("QQ 解析错误泄露敏感信息")
			}
		})
	}
}

func TestAggregateQQResolverPropagatesCancellation(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	started, stopped := make(chan struct{}), make(chan struct{})
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
			return nil, r.Context().Err()
		case <-time.After(3 * time.Second):
			return nil, errors.New("not cancelled")
		}
	})
	p := loadAggregate(t)
	if _, err := p.Aggregate("setResolverKey", map[string]any{"value": "fixture-qq-cancel", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	id := "catalog:tx:track:FixtureSongA1"
	plan, err := p.PlaybackPlan(context.Background(), id, "high", "qq-cancel")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.ResolveCandidate(ctx, id, "qq-cancel", plan.Candidates[0]); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("QQ 解析未发起")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("QQ 解析未返回取消")
		}
	case <-time.After(time.Second):
		t.Fatal("QQ 解析未及时取消")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("QQ 取消未传入HTTP")
	}
}

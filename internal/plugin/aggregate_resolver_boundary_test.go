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

// 通用服务凭据不能作为网易专用凭据的隐式回退，错误范围也不能升级为播放器全局停止。
func TestAggregateWyResolverMissingDedicatedKeyNeverCallsService(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("缺失网易密钥仍向服务发出了请求")
		return nil, errors.New("unexpected request")
	})
	p := loadAggregate(t)
	if _, err := p.Aggregate("setResolverKey", map[string]any{"value": "fixture-other-service", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	plan, err := p.PlaybackPlan(context.Background(), "catalog:wy:track:101", "high", "missing-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.ResolveCandidate(context.Background(), "catalog:wy:track:101", "missing-key", plan.Candidates[0])
	if !errors.Is(err, music.ErrSetup) {
		t.Fatal("缺失专用密钥没有提示配置")
	}
}

// 服务原始错误可能包含密钥和签名地址；固定错误分类不能回显它们或将403/429猜成全局禁播。
func TestAggregateWyResolverRejectsUnsafeOrFailedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		setup  bool
	}{
		{"auth", 403, `{"code":403,"message":"fixture-wy-boundary-secret https://example.test/token"}`, true},
		{"rate", 429, `{"code":429,"message":"fixture-wy-boundary-secret"}`, false},
		{"missing-url", 200, `{"code":200}`, false},
		{"wrong-code", 200, `{"code":"200","url":"https://audio.example.test/media"}`, false},
		{"not-json", 200, `fixture-wy-boundary-secret`, false},
		{"not-http", 200, `{"code":200,"url":"file:///private/audio.mp3"}`, false},
		{"userinfo", 200, `{"code":200,"url":"https://fixture-wy-boundary-secret@audio.example.test/media"}`, false},
		{"candidate-failure", 200, `{"code":500,"message":"fixture-wy-boundary-secret https://example.test/token"}`, false},
		{"http-error-with-success-body", 500, `{"code":200,"url":"https://audio.example.test/media"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			p := loadAggregate(t)
			if _, err := p.Aggregate("setResolverKey", map[string]any{"platform": "wy", "value": "fixture-wy-boundary-secret", "temporary": true}); err != nil {
				t.Fatal(err)
			}
			plan, err := p.PlaybackPlan(context.Background(), "catalog:wy:track:101", "high", "failure-round")
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.ResolveCandidate(context.Background(), "catalog:wy:track:101", "failure-round", plan.Candidates[0])
			if err == nil || result != nil {
				t.Fatal("失败响应被当作可播放媒体")
			}
			if errors.Is(err, music.ErrPlaybackBlocked) {
				t.Fatal("局部服务错误被误升为全局停止")
			}
			if tc.setup != errors.Is(err, music.ErrSetup) {
				t.Fatal("解析失败分类不正确")
			}
			if strings.Contains(err.Error(), "fixture-wy-boundary-secret") || strings.Contains(err.Error(), "https://example.test") {
				t.Fatal("解析错误泄露秘密或签名地址")
			}
		})
	}
}

// 宿主的候选截止必须传到真实 fetch；取消后禁止旧请求继续占用网络。
func TestAggregateWyResolverCancelsInFlightHTTP(t *testing.T) {
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
			return nil, errors.New("HTTP request was not cancelled")
		}
	})
	p := loadAggregate(t)
	if _, err := p.Aggregate("setResolverKey", map[string]any{"platform": "wy", "value": "fixture-cancel-key", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	plan, err := p.PlaybackPlan(context.Background(), "catalog:wy:track:101", "high", "cancel-round")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.ResolveCandidate(ctx, "catalog:wy:track:101", "cancel-round", plan.Candidates[0])
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("解析请求未开始")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("候选取消未返回取消状态")
		}
	case <-time.After(time.Second):
		t.Fatal("解析没有遵守取消截止")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("取消没有传递到网络请求")
	}
}

package plugin

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// 已关闭插件的取消请求不得进入随后安装的 HTTP 替身，保持真实 Transport 的取消边界。
func TestAggregateTransportRejectsCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	called := false
	transport := aggregateCatalogTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	response, err := transport.RoundTrip(request)
	if called || response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消请求仍进入系统边界：called=%v err=%v", called, err)
	}
}

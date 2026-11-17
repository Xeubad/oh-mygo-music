package plugin

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 授权响应只信任唯一且匹配的 state、code 和官方 HTTPS 回跳，不得跟随任意地址。
func TestAggregateQQAccountRejectsUnsafeAuthorization(t *testing.T) {
	for _, mode := range []string{"wrong-host", "wrong-path", "http", "userinfo", "fragment", "wrong-state", "missing-state", "duplicate-state", "duplicate-code", "invalid-code"} {
		t.Run(mode, func(t *testing.T) {
			p, store := qqAccountTest(t, func(*http.Request, string) (string, error) {
				t.Error("拒绝授权后不应读取账号")
				return `{"code":500}`, nil
			})
			previous := http.DefaultTransport
			exchanges := 0
			http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() != "graph.qq.com" {
					exchanges++
				}
				response, err := previous.RoundTrip(r)
				if err != nil || r.URL.Hostname() != "graph.qq.com" {
					return response, err
				}
				location := response.Header.Get("Location")
				switch mode {
				case "wrong-host":
					location = strings.Replace(location, "y.qq.com", "example.test", 1)
				case "wrong-path":
					location = strings.Replace(location, "/portal/wx_redirect.html", "/untrusted", 1)
				case "http":
					location = strings.Replace(location, "https:", "http:", 1)
				case "userinfo":
					location = strings.Replace(location, "https://", "https://fixture-secret@", 1)
				case "fragment":
					location += "#untrusted"
				case "wrong-state":
					location += "changed"
				case "missing-state":
					location = strings.Split(location, "&state=")[0]
				case "duplicate-state":
					location += "&state=duplicate"
				case "duplicate-code":
					location += "&code=0123456789ABCDEF0123456789ABCDEF"
				case "invalid-code":
					location = strings.Replace(location, "0123456789ABCDEF0123456789ABCDEF", "bad", 1)
				}
				response.Header.Set("Location", location)
				return response, nil
			})
			_, err := p.SignIn()
			if !errors.Is(err, music.ErrSignIn) || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "example.test") {
				t.Fatalf("不安全授权未被脱敏拒绝：%v", err)
			}
			if exchanges != 0 || len(store.values) != 0 {
				t.Fatal("无效授权继续交换或保存了会话")
			}
		})
	}
}

// 新登录授权失败只能结束本次尝试，不能删除之前仍有效的平台会话与快照。
func TestAggregateQQAccountFailedReauthenticationKeepsPreviousSession(t *testing.T) {
	p, store := qqAccountTest(t, func(*http.Request, string) (string, error) { return qqFixtureSongs(1, 0, qqFixtureSong("OldA")), nil })
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Liked(0); err != nil {
		t.Fatal(err)
	}
	before, key := qqSnapshotBody(t, p), p.credentialTarget(`platform:["tx","700"]`)
	saved := store.values[key]
	previous := http.DefaultTransport
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		response, err := previous.RoundTrip(r)
		if err == nil && r.URL.Hostname() == "graph.qq.com" {
			response.Header.Set("Location", "https://example.test/rejected")
		}
		return response, err
	})
	if _, err := p.SignIn(); !errors.Is(err, music.ErrSignIn) {
		t.Fatalf("重登应失败：%v", err)
	}
	if store.values[key] != saved || qqSnapshotBody(t, p) != before {
		t.Fatal("重登失败覆盖了旧凭据或快照")
	}
	if me, err := p.Account(); err != nil || me == nil {
		t.Fatalf("重登失败误退旧会话：%v", err)
	}
}

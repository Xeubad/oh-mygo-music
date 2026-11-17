package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 登录资料缺失时不能借凭据自带昵称伪装成已通过账号确认。
func TestAggregateQQAccountRejectsUnconfirmedProfile(t *testing.T) {
	p, store := qqAccountTest(t, func(*http.Request, string) (string, error) { return `{"code":500}`, nil })
	previous := http.DefaultTransport
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		response, err := previous.RoundTrip(r)
		if err != nil || r.URL.Hostname() != "u.y.qq.com" {
			return response, err
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(data), `"info":{"nick":"QQ测试账号"}`, `"unexpected":"fixture-music-key"`)))
		return response, nil
	})
	if _, err := p.SignIn(); !errors.Is(err, music.ErrSignIn) {
		t.Fatalf("资料未确认仍完成登录：%v", err)
	}
	if len(store.values) != 0 {
		t.Fatal("未确认的账号仍被保存")
	}
}

// 明确过期的音乐凭据不能仅凭资料接口成功就被持久保存为已登录。
func TestAggregateQQAccountRejectsExpiredLoginCredential(t *testing.T) {
	p, store := qqAccountTest(t, func(*http.Request, string) (string, error) { return `{"code":500}`, nil })
	previous := http.DefaultTransport
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		response, err := previous.RoundTrip(r)
		if err != nil || r.URL.Hostname() != "u.y.qq.com" {
			return response, err
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(data), `"expired_at":4102444800`, `"musickeyCreateTime":1,"keyExpiresIn":1`)))
		return response, nil
	})
	if _, err := p.SignIn(); !errors.Is(err, music.ErrSignIn) {
		t.Fatalf("过期凭据没有拒绝：%v", err)
	}
	if len(store.values) != 0 {
		t.Fatal("过期凭据仍被持久保存")
	}
}

// 上游明确拒绝会话时暂停 QQ 读取但保留旧快照，主动退出仍能清除该账号缓存。
func TestAggregateQQAccountExpiryPreservesCacheUntilSignOut(t *testing.T) {
	expired := false
	p, _ := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		if expired {
			return `{"code":0,"req_0":{"code":104401}}`, nil
		}
		return `{"code":0,"req_0":{"code":0,"data":{"total_song_num":1,"hasmore":0,"songlist":[{"mid":"FixtureSongA1","title":"保留曲目","interval":3,"singer":[]}]}}}`, nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Liked(0); err != nil {
		t.Fatal(err)
	}
	expired = true
	if _, err := p.Liked(0); !errors.Is(err, music.ErrSignIn) {
		t.Fatalf("失效没有要求重新登录：%v", err)
	}
	me, err := p.Account()
	if err != nil || me != nil {
		t.Fatalf("失效的 QQ 会话仍显示为已登录：%v", err)
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []struct {
		RefreshState string `json:"refreshState"`
	}
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 1 || lists[0].RefreshState != "signIn" {
		t.Fatal("失效没有保留带重新登录状态的旧快照")
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	raw, err = p.Aggregate("lists", nil)
	if err != nil || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatal("失效后主动退出没有清除旧缓存")
	}
}

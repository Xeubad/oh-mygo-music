package plugin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"mygo-music/internal/music"
)

// 账号列表已入队后退出，迟到的真实解析回执不能恢复该账号的播放资格。
func TestAggregateWyAccountSignOutRejectsInflightResolution(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			return `{"code":200,"ids":[101]}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":101,"name":"账号列表曲目"}]}`, nil
		case "/music/url":
			if r.URL.Host != "c.wwwweb.top" || r.Header.Get("X-Api-Key") != "fixture-account-wy-key" || r.Header.Get("Cookie") != "" {
				t.Error("账号会话不得复制给解析服务，解析仍须使用网易专用密钥")
			}
			close(started)
			select {
			case <-release:
				return `{"code":200,"url":"https://audio.example.test/late-account-media"}`, nil
			case <-r.Context().Done():
				return "", r.Context().Err()
			}
		default:
			t.Errorf("意外请求: %s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	if _, err := p.Aggregate("setResolverKey", map[string]any{"platform": "wy", "value": "fixture-account-wy-key", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Liked(0); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil || len(page.Sections) != 1 || len(page.Sections[0].Tracks) != 1 {
		t.Fatal("账号喜欢列表未形成聚合曲目", err)
	}
	id := page.Sections[0].Tracks[0].ID
	if !strings.HasPrefix(id, "aggregate:") {
		t.Fatal("账号曲目退化成公开目录身份")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	plan, err := p.PlaybackPlan(ctx, id, "high", "account-signout")
	if err != nil || len(plan.Candidates) == 0 {
		t.Fatal("账号曲目缺少播放候选", err)
	}
	candidate := plan.Candidates[0]
	if candidate.Platform != "wy" || candidate.AccountID != "700" || candidate.TrackID != "101" {
		t.Fatal("播放计划丢失账号或原始曲目归属")
	}
	resolved := make(chan error, 1)
	go func() {
		stream, err := p.ResolveCandidate(ctx, id, "account-signout", candidate)
		if stream != nil {
			err = errors.New("退出后仍返回迟到媒体地址")
		}
		resolved <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("解析请求未启动")
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	select {
	case err := <-resolved:
		if err == nil || errors.Is(err, music.ErrPlaybackBlocked) {
			t.Fatal("退出后解析未失效，或被误判为全局禁播", err)
		}
	case <-ctx.Done():
		t.Fatal("退出后的在途解析未收敛")
	}
	valid, err := p.CandidateValid(ctx, id, "account-signout", candidate)
	if err != nil || valid {
		t.Fatal("已退出账号仍有播放资格", err)
	}
	page, err = p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 0 {
		t.Fatal("退出后仍展示账号快照", err)
	}
}

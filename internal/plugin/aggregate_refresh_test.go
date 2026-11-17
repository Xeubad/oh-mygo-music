package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 仅给异步宿主完成提供超时保护；刷新间隔本身由插件的可控时钟测试验证。
func waitNeteaseRefresh(t *testing.T, p *Plugin, want string) {
	t.Helper()
	deadline, tick := time.NewTimer(3*time.Second), time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	for {
		raw, err := p.Aggregate("refreshStatus", nil)
		var status struct {
			State string `json:"state"`
		}
		if err != nil || json.Unmarshal(raw, &status) != nil {
			t.Fatalf("读取刷新状态失败：%v", err)
		}
		if status.State == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("刷新状态未变为 %s，仍为 %s", want, status.State)
		case <-tick.C:
		}
	}
}

// 同账号重新登录也必须主动读取新列表，不能因本轮已经展示过首页而一直留在旧缓存。
func TestAggregateNeteaseReloginStartsRefresh(t *testing.T) {
	refreshed := make(chan struct{}, 4)
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			refreshed <- struct{}{}
			return `{"code":200,"ids":[]}`, nil
		default:
			t.Errorf("重登录刷新意外请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("refresh", nil); err != nil {
		t.Fatal(err)
	}
	for len(refreshed) > 0 {
		<-refreshed
	}
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshed:
	case <-time.After(3 * time.Second):
		t.Fatal("重新登录成功后没有自动刷新喜欢列表")
	}
	waitNeteaseRefresh(t, p, "success")
}

// 重启先返回完整旧缓存，后台读取被阻塞时不能卡住首页，完整结果随后替换旧内容。
func TestAggregateNeteaseStartupRefreshKeepsCacheVisible(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var began, unblock sync.Once
	var reads atomic.Int32
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			reads.Add(1)
			began.Do(func() { close(started) })
			<-release
			return `{"code":200,"ids":[102]}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":102,"name":"新缓存"}]}`, nil
		default:
			t.Errorf("自动刷新发起意外请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "liked", "complete": true, "refreshedAt": int64(1000), "tracks": []map[string]any{{"id": "101", "name": "旧缓存"}}}); err != nil {
		t.Fatal(err)
	}
	p.Close()
	restored, err := Load(p.dir, filepath.Dir(p.data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	page, err := restored.Home()
	if err != nil || page == nil || len(page.Sections) != 1 || len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].Name != "旧缓存" {
		t.Fatalf("启动联网不能挡住旧缓存：%v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首次展示缓存后没有启动后台刷新")
	}
	unblock.Do(func() { close(release) })
	// 不在释放网络后另起手动请求来等待，否则可能把正常完成误判为未合并。
	waitNeteaseRefresh(t, restored, "success")
	raw, err := restored.Aggregate("snapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "liked"})
	var snapshot struct {
		Tracks []struct {
			ID string `json:"id"`
		} `json:"tracks"`
	}
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Tracks) != 1 || snapshot.Tracks[0].ID != "102" {
		t.Fatal("后台完整结果未替换旧快照")
	}
	if reads.Load() != 1 {
		t.Fatalf("启动刷新重复读取：次数=%d", reads.Load())
	}
}

// 真实插件宿主中登录失效也只暂停读取；重启仍保留旧缓存、成功时间和需登录状态。
func TestAggregateNeteaseExpiredRefreshSurvivesRestart(t *testing.T) {
	var reads atomic.Int32
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		if r.URL.Path != "/eapi/song/like/get" {
			t.Errorf("失效后仍发起请求：%s", r.URL.Path)
		}
		reads.Add(1)
		return `{"code":301}`, nil
	})
	input := map[string]any{"platform": "wy", "accountID": "700", "listID": "liked"}
	if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "liked", "complete": true, "refreshedAt": 1000, "tracks": []map[string]any{{"id": "101", "name": "保留缓存"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := p.Aggregate("snapshot", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("refresh", nil); err == nil {
		t.Fatal("失效刷新被当作成功")
	}
	waitNeteaseRefresh(t, p, "signIn")
	p.Close()
	restored, err := Load(p.dir, filepath.Dir(p.data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	waitNeteaseRefresh(t, restored, "signIn")
	page, err := restored.Home()
	if err != nil || len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].Name != "保留缓存" {
		t.Fatal("失效后重启未保留成功缓存")
	}
	after, err := restored.Aggregate("snapshot", input)
	if err != nil || string(before) != string(after) {
		t.Fatal("登录失效改变了快照或成功时间")
	}
	if reads.Load() != 1 {
		t.Fatal("失效后重启重新发起了账号读取")
	}
}

// 暂停只能被明确登录成功解除，随后应自动拿到新列表而不要求额外手动刷新。
func TestAggregateNeteaseReloginResumesExpiredRefresh(t *testing.T) {
	var expired atomic.Bool
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			if expired.Load() {
				return `{"code":301}`, nil
			}
			return `{"code":200,"ids":[102]}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":102,"name":"重新登录后的音乐"}]}`, nil
		default:
			t.Errorf("重新登录发出意外请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	expired.Store(true)
	if _, err := p.Aggregate("refresh", nil); err == nil {
		t.Fatal("未进入登录失效状态")
	}
	waitNeteaseRefresh(t, p, "signIn")
	expired.Store(false)
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	waitNeteaseRefresh(t, p, "success")
	page, err := p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].Name != "重新登录后的音乐" {
		t.Fatal("重登录后没有自动恢复列表刷新")
	}
}

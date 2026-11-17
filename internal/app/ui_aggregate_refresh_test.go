package app

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

type aggregateBackgroundSource struct {
	*fakeSource
	version     atomic.Int64
	statusCalls atomic.Int32
	homeCalls   atomic.Int32
	listCalls   atomic.Int32
	statusFail  atomic.Bool
	state       atomic.Value
	lastSuccess atomic.Int64
	homeReply   func(*music.Browse) (*music.Browse, error)
}

func (s *aggregateBackgroundSource) Aggregate(action string, _ map[string]any) (json.RawMessage, error) {
	switch action {
	case "refreshStatus":
		s.statusCalls.Add(1)
		if s.statusFail.Load() {
			return nil, errors.New("synthetic status unavailable")
		}
		state := "success"
		if value := s.state.Load(); value != nil {
			state = value.(string)
		}
		return json.Marshal(map[string]any{"state": state, "version": s.version.Load(), "nextRefreshAt": 0})
	case "lists":
		s.listCalls.Add(1)
		state, lastSuccess := "success", s.lastSuccess.Load()
		if value := s.state.Load(); value != nil {
			state = value.(string)
		}
		if lastSuccess == 0 {
			lastSuccess = s.version.Load() * 1000
		}
		return json.Marshal([]map[string]any{{"platform": "wy", "accountID": "synthetic", "listID": "liked", "listName": "喜欢", "refreshedAt": lastSuccess, "refreshState": state}})
	}
	return json.RawMessage(`{}`), nil
}

func aggregateBackgroundFixture(t *testing.T, configure ...func(*app, *aggregateBackgroundSource)) (*app, *aggregateBackgroundSource, *ui.Tester, chan func()) {
	t.Helper()
	a := newTestApp()
	post := posts(a)
	src := &aggregateBackgroundSource{fakeSource: fake(a)}
	src.version.Store(1)
	src.home = func() (*music.Browse, error) {
		src.homeCalls.Add(1)
		name := "旧缓存歌曲"
		if src.version.Load() > 1 {
			name = "后台新歌曲"
		}
		page := &music.Browse{Sections: []music.Section{{Tracks: []music.Track{{ID: name, Name: name, AggregateSources: []music.AggregateSource{{Platform: "wy", ListID: "liked"}}}}}}}
		if src.homeReply != nil {
			return src.homeReply(page)
		}
		return page, nil
	}
	a.library.src = src
	a.library.home, _ = src.Home()
	a.library.started = true
	a.library.homeAt = time.Now()
	a.route = route{page: pageHome, src: a.library.id}
	// Tester 构造时就可能构建视图，替身必须在它之前接好，避免首请求竞态。
	for _, setup := range configure {
		setup(a, src)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		if a.route.page == pageHome {
			a.pal = paletteFor(c.Theme())
			a.homePage(c)
		} else {
			ui.Text(c, "另一页面")
		}
	}, 1100, 900)
	return a, src, tt, post
}

// 手动重载尚未返回就离开页面时，旧回执不能覆写缓存，也不能在后台继续轮询。
func TestAggregateBackgroundLeavingDropsManualReloadReceipt(t *testing.T) {
	a, src, tt, post := aggregateBackgroundFixture(t)
	tt.Frame()
	until(t, post, func() bool { return !a.aggregateViews[a.library].polling && a.aggregateViews[a.library].listsLoaded })
	tt.Frame()
	oldHome := a.library.home
	started, release := make(chan struct{}), make(chan struct{})
	src.homeReply = func(page *music.Browse) (*music.Browse, error) {
		close(started)
		<-release
		return page, nil
	}
	src.version.Store(2)
	if err := tt.Click("Reload Cached Data"); err != nil {
		t.Fatal(err)
	}
	wait(t, started, "delayed cache reload")
	a.route.page = pageSearch
	tt.Frame()
	close(release)
	runPost(t, post)
	if a.library.home != oldHome {
		t.Fatal("离开页面后的旧重载回执覆写了缓存")
	}
	statusCalls := src.statusCalls.Load()
	tt.Advance(time.Hour)
	if src.statusCalls.Load() != statusCalls {
		t.Fatal("不可见聚合页仍在后台轮询")
	}
}

// 离开后重新打开同一来源也建立新视图，未完成的旧后台回执不能覆盖它。
func TestAggregateBackgroundReopenedViewRejectsOldRefreshReceipt(t *testing.T) {
	a, src, tt, post := aggregateBackgroundFixture(t)
	tt.Frame()
	until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
	oldView := a.aggregateViews[a.library]
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var calls atomic.Int32
	src.homeReply = func(page *music.Browse) (*music.Browse, error) {
		if calls.Add(1) == 1 {
			page.Sections[0].Tracks[0].Name = "已离页的旧后台回执"
			close(started)
			<-release
		}
		return page, nil
	}
	src.version.Store(2)
	tt.Advance(3 * time.Second)
	wait(t, started, "old background refresh")
	a.route.page = pageSearch
	tt.Frame()
	src.version.Store(3)
	a.route.page = pageHome
	tt.Frame()
	until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
	newView := a.aggregateViews[a.library]
	if newView == oldView {
		t.Fatal("重新打开页面复用了仍有旧请求的视图")
	}
	close(release)
	runPost(t, post)
	if a.library.home.Sections[0].Tracks[0].Name != "后台新歌曲" || newView.lists[0].RefreshedAt != 3000 {
		t.Fatal("旧后台回执覆写了重新打开的页面")
	}
}

// 后台旧快照仍在返回途中时，较新的手动重载结果不能被它倒写回旧版本。
func TestAggregateBackgroundOldPollCannotOverwriteManualReload(t *testing.T) {
	a, src, tt, post := aggregateBackgroundFixture(t)
	tt.Frame()
	until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var calls atomic.Int32
	src.homeReply = func(page *music.Browse) (*music.Browse, error) {
		if calls.Add(1) == 1 {
			page.Sections[0].Tracks[0].Name = "较早的后台快照"
			close(started)
			<-release
		} else {
			page.Sections[0].Tracks[0].Name = "较新的手动快照"
		}
		return page, nil
	}
	src.version.Store(2)
	tt.Advance(3 * time.Second)
	wait(t, started, "delayed background snapshot")
	src.version.Store(3)
	if err := tt.Click("Reload Cached Data"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !a.aggregateViews[a.library].busy })
	close(release)
	runPost(t, post)
	if a.library.home.Sections[0].Tracks[0].Name != "较新的手动快照" {
		t.Fatal("旧后台回执覆盖了较新的手动重载结果")
	}
}

// 多种刷新状态均保留真实的上次成功时间，通过无窗口帧检查提示与旧缓存的共存。
func TestAggregateBackgroundRefreshFrame(t *testing.T) {
	dir := framesDir(t)
	for _, state := range []string{"refreshing", "error", "signIn", "success"} {
		t.Run(state, func(t *testing.T) {
			a, _, tt, post := aggregateBackgroundFixture(t, func(_ *app, src *aggregateBackgroundSource) {
				src.state.Store(state)
				src.lastSuccess.Store(time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC).UnixMilli())
			})
			tt.Frame()
			until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
			tt.Frame()
			saveFrame(t, dir, "aggregate-background-"+state+".png", tt.Image())
		})
	}
}

type aggregateLegacyBackgroundSource struct {
	*aggregateBackgroundSource
	reply json.RawMessage
}

func (s *aggregateLegacyBackgroundSource) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	if action == "refreshStatus" {
		s.statusCalls.Add(1)
		return s.reply, nil
	}
	return s.aggregateBackgroundSource.Aggregate(action, input)
}

// 旧插件的缺失版本及非对象回执只降级一次，不能导致无限 Home 重取或后台轮询。
func TestAggregateBackgroundLegacyStatusDoesNotLoop(t *testing.T) {
	for _, reply := range []string{`[]`, `{}`, `{"state":"idle"}`, "  \n "} {
		t.Run(reply, func(t *testing.T) {
			a, src, tt, post := aggregateBackgroundFixture(t, func(a *app, src *aggregateBackgroundSource) {
				a.library.src = &aggregateLegacyBackgroundSource{aggregateBackgroundSource: src, reply: json.RawMessage(reply)}
			})
			tt.Frame()
			until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
			for range 10 {
				tt.Advance(time.Minute)
				tt.Frame()
			}
			if src.statusCalls.Load() != 1 || src.homeCalls.Load() != 1 || src.listCalls.Load() != 1 {
				t.Fatalf("旧回执导致重复请求：status=%d home=%d lists=%d", src.statusCalls.Load(), src.homeCalls.Load(), src.listCalls.Load())
			}
		})
	}
}

// 状态读取失败时仍保留上次首页、列表和成功时间；恢复后可继续发现新版本。
func TestAggregateBackgroundStatusFailureKeepsLastSuccessfulCache(t *testing.T) {
	a, src, tt, post := aggregateBackgroundFixture(t)
	tt.Frame()
	until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
	view := a.aggregateViews[a.library]
	oldHome, lastSuccess := a.library.home, view.lists[0].RefreshedAt
	src.statusFail.Store(true)
	src.version.Store(2)
	tt.Advance(3 * time.Second)
	until(t, post, func() bool { return !view.polling })
	if a.library.home != oldHome || len(view.lists) != 1 || view.lists[0].RefreshedAt != lastSuccess || view.refreshErr == "" {
		t.Fatalf("状态失败丢弃旧缓存或伪造成功时间：home=%p lists=%+v error=%q", a.library.home, view.lists, view.refreshErr)
	}
	src.statusFail.Store(false)
	tt.Advance(3 * time.Second)
	until(t, post, func() bool { return !view.polling })
	if a.library.home == oldHome || view.refreshErr != "" || view.lists[0].RefreshedAt != 2000 {
		t.Fatalf("状态恢复后没有继续更新：lists=%+v error=%q", view.lists, view.refreshErr)
	}
}

// 同版本的连续帧只复用缓存；低频状态探测不会反复读取 Home 和列表。
func TestAggregateBackgroundPollingDoesNotRefetchStableVersionEveryFrame(t *testing.T) {
	a, src, tt, post := aggregateBackgroundFixture(t)
	tt.Frame()
	until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded })
	statusCalls, homeCalls, listCalls := src.statusCalls.Load(), src.homeCalls.Load(), src.listCalls.Load()
	for range 40 {
		tt.Frame()
	}
	tt.Advance(2 * time.Second)
	if src.statusCalls.Load() != statusCalls {
		t.Fatal("普通重绘或未到期帧发出了状态请求")
	}
	tt.Advance(time.Second)
	until(t, post, func() bool { return !a.aggregateViews[a.library].polling })
	if src.statusCalls.Load() != statusCalls+1 || src.homeCalls.Load() != homeCalls || src.listCalls.Load() != listCalls {
		t.Fatalf("稳定版本仍重取了快照：status=%d home=%d lists=%d", src.statusCalls.Load(), src.homeCalls.Load(), src.listCalls.Load())
	}
}

// 后台缓存版本改变后自动展示新首页和列表，但不重建已经冻结的播放队列。
func TestAggregateBackgroundVersionUpdatesHomeWithoutChangingQueue(t *testing.T) {
	a, src, tt, post := aggregateBackgroundFixture(t)
	a.pl.Restore(player.Session{Queue: []music.Track{{ID: "queued", Name: "原队列", Source: a.library.id}}, Order: []int{0}, At: 0})
	tt.Frame()
	until(t, post, func() bool { return a.aggregateViews[a.library].listsLoaded && src.statusCalls.Load() > 0 })
	// 先收齐首轮回执，再推进测试器时钟；不依赖真实时间或平台登录。
	for src.homeCalls.Load() < 2 {
		runPost(t, post)
	}
	tt.Frame()
	src.version.Store(2)
	tt.Advance(6 * time.Second)
	until(t, post, func() bool { return a.library.home.Sections[0].Tracks[0].Name == "后台新歌曲" })
	view := a.aggregateViews[a.library]
	if len(view.lists) != 1 || view.lists[0].RefreshedAt != 2000 {
		t.Fatalf("后台新版本没有同步列表：%+v", view.lists)
	}
	if current := a.pl.Current(); current == nil || current.ID != "queued" || len(a.pl.Queue()) != 1 {
		t.Fatalf("后台刷新修改了已冻结队列：%+v", a.pl.Queue())
	}
}

package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// aggregateWriteSource 只实现云端写入契约，让界面测试能观察真正发出的操作。
type aggregateWriteSource struct {
	*fakeSource
	page       *music.Browse
	lists      []aggregateList
	caps       map[string][]string
	generation int64
	snapshot   map[string][]map[string]any
	writes     chan map[string]any
	writeReply func(map[string]any) (map[string]any, error)
}

func (s *aggregateWriteSource) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	switch action {
	case "writeCapabilities":
		platforms := make([]map[string]any, 0, 3)
		for _, platform := range []string{"wy", "tx", "qs"} {
			operations, ok := s.caps[platform]
			platforms = append(platforms, map[string]any{"platform": platform, "available": ok, "operations": operations})
		}
		return json.Marshal(map[string]any{"version": 1, "platforms": platforms})
	case "snapshotGeneration":
		return json.Marshal(map[string]any{"generation": s.generation})
	case "write":
		s.writes <- input
		if s.writeReply != nil {
			reply, err := s.writeReply(input)
			if err != nil {
				return nil, err
			}
			return json.Marshal(reply)
		}
		reply := map[string]any{"version": 1, "ok": true, "code": "done", "message": "done", "platform": input["platform"], "operation": input["operation"]}
		if listID, ok := input["listID"]; ok {
			reply["listID"] = listID
		}
		if name, ok := input["name"]; ok {
			reply["name"] = name
		}
		if source, ok := input["source"].(map[string]any); ok {
			reply["trackID"] = source["trackID"]
		}
		if input["operation"] == "playlistCreate" {
			reply["listID"], reply["name"] = "created-1", input["name"]
		}
		return json.Marshal(reply)
	case "lists":
		return json.Marshal(s.lists)
	case "snapshot":
		listID, _ := input["listID"].(string)
		return json.Marshal(map[string]any{"refreshedAt": 1, "tracks": s.snapshot[listID]})
	case "refreshStatus":
		return json.Marshal(map[string]any{"state": "success", "version": 1, "nextRefreshAt": 0})
	case "refresh", "selectList":
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func aggregateWriteTrack(name string, source music.AggregateSource) music.Track {
	frozen, _ := json.Marshal(map[string]any{"version": 1, "identity": source, "sources": []music.AggregateSource{source}})
	return music.Track{ID: "aggregate:" + string(frozen), Source: "mygo-aggregate", Name: name,
		AggregateSources: []music.AggregateSource{source}}
}

func aggregateWriteFixture(t *testing.T, platform string) (*app, *aggregateWriteSource, *ui.Tester, chan func()) {
	t.Helper()
	a := newTestApp()
	post := posts(a)
	road := music.AggregateSource{Platform: platform, AccountID: "acct-1", TrackID: "raw-01", ListID: "list-1", ListName: "Road songs"}
	liked := music.AggregateSource{Platform: platform, AccountID: "acct-1", TrackID: "raw-02", ListID: "liked", ListName: "Liked"}
	src := &aggregateWriteSource{
		fakeSource: fake(a),
		generation: 7,
		writes:     make(chan map[string]any, 8),
		lists: []aggregateList{
			{Platform: platform, AccountID: "acct-1", ListID: "liked", ListName: "Liked", RefreshedAt: 1},
			{Platform: platform, AccountID: "acct-1", ListID: "list-1", ListName: "Road songs", RefreshedAt: 1},
			{Platform: platform, AccountID: "acct-1", ListID: "list-2", ListName: "Focus", RefreshedAt: 1},
		},
		snapshot: map[string][]map[string]any{
			"list-1": {{"id": "raw-02", "name": "Liked Song"}, {"id": "raw-01", "name": "Cloud Song"}},
			"list-2": {{"id": "raw-02", "name": "Liked Song"}},
		},
		caps: map[string][]string{},
	}
	all := []string{"like", "unlike", "playlistCreate", "playlistRename", "playlistDelete",
		"playlistAddTrack", "playlistRemoveTrack", "playlistMoveTrack"}
	switch platform {
	case "wy":
		src.caps["wy"] = all
	case "tx":
		// QQ 未实现移动，界面不能提供会失败的入口。
		src.caps["tx"] = []string{"like", "unlike", "playlistCreate", "playlistRename", "playlistDelete",
			"playlistAddTrack", "playlistRemoveTrack"}
	case "qs":
		src.caps["qs"] = []string{"like", "unlike"}
	}
	src.page = &music.Browse{Sections: []music.Section{{Title: "Cloud", Tracks: []music.Track{
		aggregateWriteTrack("Cloud Song", road),
		aggregateWriteTrack("Liked Song", liked),
	}}}}
	a.library.src = src
	a.library.caps = probeCaps(src)
	a.library.home = src.page
	src.home = func() (*music.Browse, error) { return src.page, nil }
	a.library.started = true
	a.library.homeAt = time.Now()
	a.route = route{page: pageHome, src: a.library.id}
	tt := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.aggregateLibrary(c, a.library, a.pal, false)
		a.aggregateWriteDialog(c)
	}, 1100, 1200)
	return a, src, tt, post
}

func aggregateWriteReady(t *testing.T, a *app, tt *ui.Tester, post chan func()) *aggregateView {
	t.Helper()
	tt.Frame()
	view := a.aggregateViews[a.library]
	until(t, post, func() bool { return view.listsLoaded && view.writeCapsLoaded && !view.loadingLists })
	tt.Frame()
	return view
}

// 喜欢先确认平台与账号，确认后才按原始平台曲目 ID 和当前快照世代写入，播放队列保持不变。
func TestAggregateCloudLikeConfirmsBeforeWriting(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	view := aggregateWriteReady(t, a, tt, post)
	a.pl.Cue([]music.Track{{ID: "queued", Name: "正在播放", Source: a.library.id}})

	if err := tt.Click("Cloud Actions Cloud Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Like"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	select {
	case input := <-src.writes:
		t.Fatalf("确认前就发出了写入：%+v", input)
	default:
	}
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1", "Cloud Song"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}

	if err := tt.Click("Like"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "喜欢写入")
	if input["operation"] != "like" || input["platform"] != "wy" || input["accountID"] != "acct-1" {
		t.Fatalf("写入目标不正确：%+v", input)
	}
	if input["generation"] != int64(7) {
		t.Fatalf("没有携带当前快照世代：%+v", input)
	}
	source, _ := input["source"].(map[string]any)
	if source["trackID"] != "raw-01" || source["platform"] != "wy" || source["accountID"] != "acct-1" {
		t.Fatalf("用聚合身份代替平台曲目：%+v", source)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if q := a.pl.Queue(); len(q) != 1 || q[0].ID != "queued" {
		t.Fatalf("云端写入改变了播放队列：%+v", q)
	}
	key := aggregateSourceKey(music.AggregateSource{Platform: "wy", AccountID: "acct-1", TrackID: "raw-01"})
	if !view.writeLiked[key] {
		t.Fatal("写入成功后喜欢状态没有更新")
	}
}

// 账号失效时喜欢回滚到写入前状态，确认框保留并说明原因，远端与本地都不假装成功。
func TestAggregateCloudLikeFailureRollsBack(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	view := aggregateWriteReady(t, a, tt, post)
	src.writeReply = func(map[string]any) (map[string]any, error) {
		return map[string]any{"version": 1, "ok": false, "code": "accountInvalid", "message": "账号已退出"}, nil
	}
	key := aggregateSourceKey(music.AggregateSource{Platform: "wy", AccountID: "acct-1", TrackID: "raw-01"})

	if err := tt.Click("Cloud Actions Cloud Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Like"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	if err := tt.Click("Like"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if view.writeLiked[key] {
		t.Fatal("账号失效后乐观喜欢没有回滚")
	}
	if !a.aggregateWrite.open {
		t.Fatal("失败后关闭了确认框，用户无法直接重试")
	}
	if !strings.Contains(strings.Join(tt.Texts(), " "), "signed out") {
		t.Fatalf("失败原因没有区分账号失效：%q", tt.Texts())
	}
}

// 添加歌曲先选择目标歌单并确认平台与账号，确认后才写入原始平台曲目身份，播放队列不变。
func TestAggregateCloudAddTrackToChosenPlaylist(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	_ = aggregateWriteReady(t, a, tt, post)
	a.pl.Cue([]music.Track{{ID: "queued", Name: "正在播放", Source: a.library.id}})

	if err := tt.Click("Cloud Actions Cloud Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Add to Playlist", "Focus"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	select {
	case input := <-src.writes:
		t.Fatalf("确认前就发出了写入：%+v", input)
	default:
	}
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1", "Cloud Song", "Focus"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}

	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "添加歌曲写入")
	if input["operation"] != "playlistAddTrack" || input["listID"] != "list-2" {
		t.Fatalf("写入目标歌单不正确：%+v", input)
	}
	source, _ := input["source"].(map[string]any)
	if source["trackID"] != "raw-01" || source["platform"] != "wy" || source["accountID"] != "acct-1" {
		t.Fatalf("用聚合身份代替平台曲目：%+v", source)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if q := a.pl.Queue(); len(q) != 1 || q[0].ID != "queued" {
		t.Fatalf("云端写入改变了播放队列：%+v", q)
	}
	if !strings.Contains(a.toast, "Added") {
		t.Fatalf("成功后没有结果反馈：%q", a.toast)
	}
}

// 创建歌单先选平台账号并填写名称，确认后才写入，成功后新歌单立即可作为添加目标。
func TestAggregateCloudCreatePlaylist(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	view := aggregateWriteReady(t, a, tt, post)

	if err := tt.Click("Create Playlist"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("NetEase Music · acct-1"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}
	select {
	case input := <-src.writes:
		t.Fatalf("确认前就发出了写入：%+v", input)
	default:
	}

	if err := tt.Click("Playlist Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Morning")
	if err := tt.Click("Create"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "创建歌单写入")
	if input["operation"] != "playlistCreate" || input["name"] != "Morning" ||
		input["platform"] != "wy" || input["accountID"] != "acct-1" {
		t.Fatalf("创建歌单参数不正确：%+v", input)
	}
	if input["generation"] != int64(7) {
		t.Fatalf("没有携带当前快照世代：%+v", input)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if !aggregateWriteHasList(view.lists, "created-1", "Morning") {
		t.Fatalf("新歌单没有进入本地列表：%+v", view.lists)
	}
	if !strings.Contains(a.toast, "Created") {
		t.Fatalf("成功后没有结果反馈：%q", a.toast)
	}
}

func aggregateWriteHasList(lists []aggregateList, id, name string) bool {
	for _, list := range lists {
		if list.ListID == id && list.ListName == name {
			return true
		}
	}
	return false
}

// 重命名歌单先确认平台、账号和目标歌单，成功后本地列表与远端一致。
func TestAggregateCloudRenamePlaylist(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	view := aggregateWriteReady(t, a, tt, post)

	if err := tt.Click("Playlist Actions Focus"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1", "Focus"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}
	if err := tt.Click("Playlist Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Deep Work")
	if err := tt.Click("Rename"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "重命名歌单写入")
	if input["operation"] != "playlistRename" || input["listID"] != "list-2" || input["name"] != "Deep Work" {
		t.Fatalf("重命名参数不正确：%+v", input)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if !aggregateWriteHasList(view.lists, "list-2", "Deep Work") {
		t.Fatalf("本地歌单名没有更新：%+v", view.lists)
	}
	if !strings.Contains(a.toast, "Renamed") {
		t.Fatalf("成功后没有结果反馈：%q", a.toast)
	}
}

// 删除歌单必须二次确认，确认后携带 confirm 写入，成功后从本地列表移除。
func TestAggregateCloudDeletePlaylist(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	view := aggregateWriteReady(t, a, tt, post)

	if err := tt.Click("Playlist Actions Focus"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1", "Focus"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "删除歌单写入")
	if input["operation"] != "playlistDelete" || input["listID"] != "list-2" || input["confirm"] != true {
		t.Fatalf("删除参数不正确：%+v", input)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if aggregateWriteHasList(view.lists, "list-2", "Focus") {
		t.Fatalf("删除成功后本地列表仍在：%+v", view.lists)
	}
	if !strings.Contains(a.toast, "Deleted") {
		t.Fatalf("成功后没有结果反馈：%q", a.toast)
	}
}

// 从歌单移除歌曲先确认平台、账号和所在歌单，写入使用原始平台曲目身份。
func TestAggregateCloudRemoveTrackFromPlaylist(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	_ = aggregateWriteReady(t, a, tt, post)
	a.pl.Cue([]music.Track{{ID: "queued", Name: "正在播放", Source: a.library.id}})

	if err := tt.Click("Cloud Actions Cloud Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Remove from Playlist"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1", "Cloud Song", "Road songs"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}
	if err := tt.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "移除歌曲写入")
	if input["operation"] != "playlistRemoveTrack" || input["listID"] != "list-1" {
		t.Fatalf("移除目标歌单不正确：%+v", input)
	}
	source, _ := input["source"].(map[string]any)
	if source["trackID"] != "raw-01" {
		t.Fatalf("用聚合身份代替平台曲目：%+v", source)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if q := a.pl.Queue(); len(q) != 1 || q[0].ID != "queued" {
		t.Fatalf("云端写入改变了播放队列：%+v", q)
	}
	if !strings.Contains(a.toast, "Removed") {
		t.Fatalf("成功后没有结果反馈：%q", a.toast)
	}
}

// 移动歌曲先选择目标歌单并填写目标位置，写入携带 toIndex，成功后给出反馈。
func TestAggregateCloudMoveTrackInPlaylist(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	_ = aggregateWriteReady(t, a, tt, post)

	if err := tt.Click("Cloud Actions Cloud Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Move in Playlist", "Focus"); err != nil {
		t.Fatalf("%v; 菜单显示 %q", err, tt.Menu())
	}
	tt.Frame()
	texts := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"NetEase Music", "acct-1", "Cloud Song", "Focus"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("确认框缺少 %q：%q", want, texts)
		}
	}
	if err := tt.Click("Position"); err != nil {
		t.Fatal(err)
	}
	tt.Type("1")
	if err := tt.Click("Move"); err != nil {
		t.Fatal(err)
	}
	input := wait(t, src.writes, "移动歌曲写入")
	// 界面位置从 1 开始，契约 toIndex 从 0 开始。
	if input["operation"] != "playlistMoveTrack" || input["listID"] != "list-2" || input["toIndex"] != 0 {
		t.Fatalf("移动参数不正确：%+v", input)
	}
	until(t, post, func() bool { return !a.aggregateWrite.busy })
	tt.Frame()
	if !strings.Contains(a.toast, "Moved") {
		t.Fatalf("成功后没有结果反馈：%q", a.toast)
	}
}

// 写入未返回时确认框显示加载状态并收起原确认按钮，避免重复提交。
func TestAggregateCloudWriteShowsBusyState(t *testing.T) {
	a, src, tt, post := aggregateWriteFixture(t, "wy")
	_ = aggregateWriteReady(t, a, tt, post)
	started, release := make(chan struct{}), make(chan struct{})
	src.writeReply = func(map[string]any) (map[string]any, error) {
		close(started)
		<-release
		return map[string]any{"version": 1, "ok": true, "code": "done"}, nil
	}

	if err := tt.Click("Playlist Actions Focus"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Playlist Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Deep Work")
	if err := tt.Click("Rename"); err != nil {
		t.Fatal(err)
	}
	wait(t, started, "写入开始")
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), " "), "Writing to your account") {
		t.Fatalf("写入中没有加载状态：%q", tt.Texts())
	}
	if err := tt.Click("Rename"); err == nil {
		t.Fatal("写入中仍能用原按钮重复提交")
	}
	close(release)
	until(t, post, func() bool { return !a.aggregateWrite.busy })
}

// 歌单写入失败时保留原列表并留在确认框，按账号失效、冲突、不支持与可重试错误给出不同提示。
func TestAggregateCloudPlaylistFailureKeepsState(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		cause error
		want  string
	}{
		{name: "account invalid", code: "accountInvalid", want: "signed out"},
		{name: "unsupported", code: "unsupported", want: "does not support"},
		{name: "conflict", code: "conflict", want: "changed"},
		{name: "retryable", cause: errors.New("network down"), want: "retry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, src, tt, post := aggregateWriteFixture(t, "wy")
			view := aggregateWriteReady(t, a, tt, post)
			src.writeReply = func(map[string]any) (map[string]any, error) {
				if tc.cause != nil {
					return nil, tc.cause
				}
				return map[string]any{"version": 1, "ok": false, "code": tc.code, "message": "failed"}, nil
			}
			if err := tt.Click("Playlist Actions Focus"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("Rename"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("Playlist Name"); err != nil {
				t.Fatal(err)
			}
			tt.Type("Broken")
			if err := tt.Click("Rename"); err != nil {
				t.Fatal(err)
			}
			until(t, post, func() bool { return !a.aggregateWrite.busy })
			tt.Frame()
			if !aggregateWriteHasList(view.lists, "list-2", "Focus") {
				t.Fatalf("失败后本地歌单被改动：%+v", view.lists)
			}
			if !a.aggregateWrite.open {
				t.Fatal("失败后关闭了确认框，用户无法直接重试")
			}
			if !strings.Contains(strings.Join(tt.Texts(), " "), tc.want) {
				t.Fatalf("失败提示没有区分 %s：%q", tc.name, tt.Texts())
			}
		})
	}
}

// QQ 不支持移动歌曲时界面不提供必然失败的入口。
func TestAggregateCloudHidesUnsupportedMove(t *testing.T) {
	a, _, tt, post := aggregateWriteFixture(t, "tx")
	_ = aggregateWriteReady(t, a, tt, post)

	if err := tt.Click("Cloud Actions Cloud Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Move in Playlist", "Focus"); err == nil {
		t.Fatalf("QQ 仍显示移动入口：%q", tt.Menu())
	}
}

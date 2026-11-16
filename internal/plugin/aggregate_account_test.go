package plugin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 仅替换 HTTP 与系统凭据边界，账号建立仍经过真实扫码适配器和插件公开接口。
func neteaseAccountTest(t *testing.T, serve func(*http.Request) (string, error)) (*Plugin, *memoryCredentials) {
	t.Helper()
	store := &memoryCredentials{values: map[string]string{}}
	previousStore, previousTransport := systemCredentials, http.DefaultTransport
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previousStore; http.DefaultTransport = previousTransport })
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		var body string
		var err error
		switch r.URL.Path {
		case "/eapi/login/qrcode/unikey":
			body = `{"code":200,"unikey":"synthetic-qr-key"}`
		case "/eapi/login/qrcode/client/login":
			header.Add("Set-Cookie", "MUSIC_U=synthetic-account-cookie; Path=/; HttpOnly")
			body = `{"code":803}`
		case "/eapi/w/nuser/account/get":
			body = `{"code":200,"profile":{"userId":700,"nickname":"测试账号"}}`
		default:
			body, err = serve(r)
		}
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	p := loadAggregate(t)
	if _, err := p.SignIn(); err != nil {
		t.Fatalf("建立受控账号失败：%v", err)
	}
	return p, store
}

// 主动退出必须同时移除账号快照和凭据，不能让聚合入口继续保留该账号内容。
func TestAggregateAccountNeteaseSignOutClearsSnapshots(t *testing.T) {
	p, store := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		t.Errorf("退出不应请求平台：%s", r.URL.Path)
		return `{"code":404}`, nil
	})
	_, err := p.Aggregate("commitSnapshot", map[string]any{
		"platform": "wy", "accountID": "700", "listID": "liked",
		"complete": true, "refreshedAt": 1000,
		"tracks": []map[string]any{{"id": "101", "name": "已有喜欢"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("主动退出仍保留该账号的云端聚合快照")
	}
	if _, found := store.values[p.credentialTarget(`platform:["wy","700"]`)]; found {
		t.Fatal("主动退出仍保留该账号凭据")
	}
	account, err := p.Account()
	if err != nil || account != nil {
		t.Fatal("主动退出后账号状态恢复")
	}
}

// 喜欢列表必须读完再进入聚合，保留原始曲目身份而不是把目录命名空间发给平台。
func TestAggregateAccountNeteaseLikedCommitsCompleteSnapshot(t *testing.T) {
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		if !strings.Contains(r.Header.Get("Cookie"), "MUSIC_U=synthetic-account-cookie") {
			t.Error("账号列表请求没有使用当前账号凭据")
		}
		switch r.URL.Path {
		case "/eapi/song/like/get":
			return `{"code":200,"ids":[101,102,103]}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":103,"name":"第三首"},{"id":101,"name":"第一首"}]}`, nil
		default:
			t.Errorf("意外的喜欢列表请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	page, err := p.Liked(0)
	if err != nil {
		t.Fatalf("账号喜欢列表尚未接入：%v", err)
	}
	if page.More || len(page.Tracks) != 3 {
		t.Fatal("喜欢列表没有完整读取")
	}
	for i, id := range []string{"101", "102", "103"} {
		if page.Tracks[i].ID != "catalog:wy:track:"+id || page.Tracks[i].Source != p.Info().ID {
			t.Fatal("喜欢列表顺序或目录身份丢失")
		}
	}
	if !page.Tracks[1].Unavailable {
		t.Fatal("下架歌曲没有保留位置")
	}
	raw, err := p.Aggregate("snapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "liked"})
	var snapshot struct {
		RefreshedAt int64         `json:"refreshedAt"`
		Tracks      []music.Track `json:"tracks"`
	}
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || snapshot.RefreshedAt <= 0 || len(snapshot.Tracks) != 3 || snapshot.Tracks[0].ID != "101" {
		t.Fatal("完整喜欢列表没有按账号和原始ID提交快照")
	}
	home, err := p.Home()
	if err != nil || len(home.Sections) != 1 || len(home.Sections[0].Tracks) != 3 || !strings.HasPrefix(home.Sections[0].Tracks[0].ID, "aggregate:") {
		t.Fatal("账号喜欢列表没有进入聚合首页")
	}
}

// 账号歌单读取使用当前凭据，浏览后保留原始ID快照，但只有明确选择才进入聚合。
func TestAggregateAccountNeteasePlaylistSnapshotRequiresSelection(t *testing.T) {
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		if !strings.Contains(r.Header.Get("Cookie"), "MUSIC_U=synthetic-account-cookie") {
			t.Error("账号歌单读取没有使用当前账号凭据")
		}
		switch r.URL.Path {
		case "/eapi/user/playlist":
			return `{"code":200,"more":false,"playlist":[{"id":900,"name":"我的歌单","trackCount":1}]}`, nil
		case "/eapi/v6/playlist/detail":
			return `{"code":200,"playlist":{"id":900,"name":"我的歌单","trackCount":1,"trackIds":[{"id":101}]}}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":101,"name":"歌单歌曲"}]}`, nil
		default:
			t.Errorf("意外的账号歌单请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	lists, err := p.Playlists()
	if err != nil || len(lists) != 1 || lists[0].ID != "catalog:wy:playlist:900" {
		t.Fatalf("账号歌单目录尚未接入：%v", err)
	}
	list, err := p.Playlist(lists[0].ID)
	if err != nil || len(list.Tracks) != 1 {
		t.Fatalf("账号歌单无法完整读取：%v", err)
	}
	home, err := p.Home()
	if err != nil || len(home.Sections[0].Tracks) != 0 {
		t.Fatal("仅浏览歌单就擅自纳入聚合")
	}
	_, err = p.Aggregate("selectList", map[string]any{"platform": "wy", "accountID": "700", "listID": "900", "selected": true})
	if err != nil {
		t.Fatalf("已读完的账号歌单没有可选择快照：%v", err)
	}
	home, err = p.Home()
	if err != nil || len(home.Sections[0].Tracks) != 1 || home.Sections[0].Tracks[0].AggregateSources[0].TrackID != "101" {
		t.Fatal("选择歌单后未按原始身份加入聚合")
	}
}

// 系统库不可读时保留旧凭据，但主动退出后不能因库恢复而重新使用旧登录。
func TestAggregateAccountNeteaseSignOutUnavailableStoreBlocksAccess(t *testing.T) {
	p, store := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		t.Errorf("退出后不应产生账号请求：%s", r.URL.Path)
		return `{"code":404}`, nil
	})
	store.readErr = errCredentialsUnavailable
	if err := p.SignOut(); err == nil {
		t.Fatal("系统库不可读没有如实报告")
	}
	store.readErr = nil
	me, err := p.Account()
	if err != nil || me != nil {
		t.Fatal("凭据库恢复后，已经退出的账号又恢复访问")
	}
	if len(store.values) != 1 {
		t.Fatal("不可读期间擅自删除了原凭据")
	}
}

// 退出期间迟到的喜欢ID不能再发起详情请求，也不能重建刚清除的账号快照。
func TestAggregateAccountNeteaseLateReadStopsAfterSignOut(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		if r.URL.Path == "/eapi/song/like/get" {
			close(started)
			<-release
			return `{"code":200,"ids":[101]}`, nil
		}
		t.Errorf("退出后的旧读取仍发出请求：%s", r.URL.Path)
		return `{"code":200,"songs":[{"id":101,"name":"迟到歌曲"}]}`, nil
	})
	done := make(chan error, 1)
	go func() { _, err := p.Liked(0); done <- err }()
	<-started
	err := p.SignOut()
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("迟到读取被当作有效账号结果")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("迟到读取重建已退出快照")
	}
}

// 服务未返回详情数组不等于歌曲全部下架，不能覆盖上次成功内容与时间。
func TestAggregateAccountNeteaseMissingDetailsPreservesSnapshot(t *testing.T) {
	broken := false
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			return `{"code":200,"ids":[101]}`, nil
		case "/eapi/v3/song/detail":
			if broken {
				return `{"code":200}`, nil
			}
			return `{"code":200,"songs":[{"id":101,"name":"上次成功歌曲"}]}`, nil
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	if _, err := p.Liked(0); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"platform": "wy", "accountID": "700", "listID": "liked"}
	before, err := p.Aggregate("snapshot", input)
	if err != nil {
		t.Fatal(err)
	}
	broken = true
	if _, err := p.Liked(0); err == nil {
		t.Fatal("缺失歌曲详情被伪装为完整成功")
	}
	after, err := p.Aggregate("snapshot", input)
	if err != nil || string(before) != string(after) {
		t.Fatal("读取失败改变了旧快照或成功时间")
	}
}

// 手动刷新分别读取喜欢和已选歌单，完整成功后才替换对应快照。
func TestAggregateAccountNeteaseRefreshUpdatesSelectedLists(t *testing.T) {
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			return `{"code":200,"ids":[101]}`, nil
		case "/eapi/v6/playlist/detail":
			return `{"code":200,"playlist":{"id":900,"name":"已选歌单","trackCount":1,"trackIds":[{"id":102}]}}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":101,"name":"新喜欢"},{"id":102,"name":"新歌单歌曲"}]}`, nil
		default:
			t.Errorf("刷新出现意外请求：%s", r.URL.Path)
			return `{"code":404}`, nil
		}
	})
	_, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "900", "selected": true, "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": "103", "name": "旧歌单歌曲"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("refresh", nil); err != nil {
		t.Fatal(err)
	}
	home, err := p.Home()
	if err != nil || len(home.Sections) != 1 || len(home.Sections[0].Tracks) != 2 {
		t.Fatal("云端刷新没有读取喜欢与已选歌单")
	}
	names := map[string]bool{}
	for _, track := range home.Sections[0].Tracks {
		names[track.Name] = true
	}
	if !names["新喜欢"] || !names["新歌单歌曲"] || names["旧歌单歌曲"] {
		t.Fatal("云端刷新未替换对应列表快照")
	}
}

// 切换账号期间的旧账号查询不能把新Cookie错绑给旧身份，也不能撤销已完成的登录。
func TestAggregateAccountNeteaseStaleAccountCannotUndoLogin(t *testing.T) {
	p, store := neteaseAccountTest(t, func(r *http.Request) (string, error) { return `{"code":404}`, nil })
	upstream := http.DefaultTransport
	keyStarted, oldStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		header := make(http.Header)
		switch r.URL.Path {
		case "/eapi/login/qrcode/unikey":
			close(keyStarted)
			return upstream.RoundTrip(r)
		case "/eapi/login/qrcode/client/login":
			header.Add("Set-Cookie", "MUSIC_U=synthetic-new-cookie; Path=/")
			body = `{"code":803}`
		case "/eapi/w/nuser/account/get":
			if strings.Contains(r.Header.Get("Cookie"), "synthetic-new-cookie") {
				body = `{"code":200,"profile":{"userId":701,"nickname":"新账号"}}`
			} else {
				close(oldStarted)
				<-release
				body = `{"code":200,"profile":{"userId":700,"nickname":"旧账号"}}`
			}
		default:
			return upstream.RoundTrip(r)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	loginDone, queryDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := p.SignIn(); loginDone <- err }()
	<-keyStarted
	go func() { _, err := p.Account(); queryDone <- err }()
	<-oldStarted
	loginErr := <-loginDone
	close(release)
	<-queryDone
	if loginErr != nil {
		t.Fatal(loginErr)
	}
	old := store.values[p.credentialTarget(`platform:["wy","700"]`)]
	if strings.Contains(old, "synthetic-new-cookie") {
		t.Fatal("迟到查询把新账号Cookie错绑给旧账号")
	}
	p.mu.Lock()
	active := string(p.storage["platform:wy:active"])
	p.mu.Unlock()
	if active != `"701"` {
		t.Fatal("迟到账号查询撤销了已完成的账号切换")
	}
}

// 较旧读取晚完成时，不能回滚另一轮已提交的新列表或伪造更新的成功时间。
func TestAggregateAccountNeteaseOlderReadCannotRollbackSnapshot(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	idsRequests, detailsRequests := 0, 0
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			idsRequests++
			if idsRequests == 1 {
				return `{"code":200,"ids":[101]}`, nil
			}
			return `{"code":200,"ids":[101,102]}`, nil
		case "/eapi/v3/song/detail":
			detailsRequests++
			if detailsRequests == 1 {
				close(started)
				<-release
				return `{"code":200,"songs":[{"id":101,"name":"旧歌曲"}]}`, nil
			}
			return `{"code":200,"songs":[{"id":101,"name":"新歌曲"},{"id":102,"name":"新增歌曲"}]}`, nil
		default:
			return `{"code":404}`, nil
		}
	})
	done := make(chan error, 1)
	go func() { _, err := p.Liked(0); done <- err }()
	<-started
	_, newerErr := p.Liked(0)
	input := map[string]any{"platform": "wy", "accountID": "700", "listID": "liked"}
	newer, snapshotErr := p.Aggregate("snapshot", input)
	close(release)
	<-done
	if newerErr != nil || snapshotErr != nil {
		t.Fatalf("较新读取失败：%v / %v", newerErr, snapshotErr)
	}
	after, err := p.Aggregate("snapshot", input)
	if err != nil || string(newer) != string(after) {
		t.Fatal("旧读取回滚了已成功的新快照或成功时间")
	}
}

// 同账号重新登录也必须更换会话代次；旧Cookie轮换或失效回包不能覆盖、删除新凭据。
func TestAggregateAccountNeteaseReloginRejectsOldSessionResponses(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "cookie", true: "expired"}[expired], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			p, store := neteaseAccountTest(t, func(r *http.Request) (string, error) { return `{"code":404}`, nil })
			upstream := http.DefaultTransport
			http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
				header := make(http.Header)
				var body string
				switch r.URL.Path {
				case "/eapi/song/like/get":
					close(started)
					<-release
					if expired {
						body = `{"code":301}`
					} else {
						header.Add("Set-Cookie", "MUSIC_U=synthetic-stale-cookie; Path=/")
						body = `{"code":200,"ids":[101]}`
					}
				case "/eapi/login/qrcode/client/login":
					header.Add("Set-Cookie", "MUSIC_U=synthetic-renewed-cookie; Path=/")
					body = `{"code":803}`
				case "/eapi/v3/song/detail":
					t.Error("旧会话失效后继续读取详情")
					body = `{"code":200,"songs":[]}`
				default:
					return upstream.RoundTrip(r)
				}
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			done := make(chan error, 1)
			go func() { _, err := p.Liked(0); done <- err }()
			<-started
			_, loginErr := p.SignIn()
			close(release)
			oldErr := <-done
			if loginErr != nil {
				t.Fatal(loginErr)
			}
			if oldErr == nil {
				t.Fatal("重登录后旧会话结果仍被接受")
			}
			value := store.values[p.credentialTarget(`platform:["wy","700"]`)]
			if !strings.Contains(value, "synthetic-renewed-cookie") || strings.Contains(value, "synthetic-stale-cookie") {
				t.Fatal("旧响应覆盖或删除了新会话凭据")
			}
			me, err := p.Account()
			if err != nil || me == nil || me.ID != "catalog:wy:account:700" {
				t.Fatal("旧响应使新登录失效")
			}
		})
	}
}

// 整轮刷新固定发起账号；第一张列表失败后切换账号，不能拿新凭据读取旧账号的已选歌单。
func TestAggregateAccountNeteaseRefreshCannotCrossAccounts(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) { return `{"code":404}`, nil })
	_, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "900", "selected": true, "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": "101"}}})
	if err != nil {
		t.Fatal(err)
	}
	upstream := http.DefaultTransport
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		header := make(http.Header)
		switch r.URL.Path {
		case "/eapi/song/like/get":
			close(started)
			<-release
			return nil, io.ErrUnexpectedEOF
		case "/eapi/w/nuser/account/get":
			body = `{"code":200,"profile":{"userId":701,"nickname":"新账号"}}`
		case "/eapi/v6/playlist/detail":
			t.Error("切换账号后旧刷新继续读取歌单")
			body = `{"code":200,"playlist":{"id":900,"name":"旧账号歌单","trackCount":0,"trackIds":[]}}`
		default:
			return upstream.RoundTrip(r)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := p.Aggregate("refresh", nil); done <- err }()
	<-started
	_, loginErr := p.SignIn()
	close(release)
	refreshErr := <-done
	if loginErr != nil {
		t.Fatal(loginErr)
	}
	if refreshErr == nil {
		t.Fatal("旧账号刷新未报告失效")
	}
}

// 复用真实上游扫码流程；登录秘密只能保存到系统凭据边界，不能落入普通配置。
func TestAggregateAccountNeteaseQRProtectsSession(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previousStore, previousTransport := systemCredentials, http.DefaultTransport
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previousStore; http.DefaultTransport = previousTransport })
	const secret = "synthetic-aggregate-account-cookie"
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != http.MethodPost || r.URL.Hostname() != "interface.music.163.com" {
			t.Error("扫码登录离开已核对的上游请求边界")
		}
		header := make(http.Header)
		var body string
		switch r.URL.Path {
		case "/eapi/login/qrcode/unikey":
			body = `{"code":200,"unikey":"synthetic-qr-key"}`
		case "/eapi/login/qrcode/client/login":
			header.Add("Set-Cookie", "MUSIC_U="+secret+"; Path=/; HttpOnly")
			body = `{"code":803}`
		case "/eapi/w/nuser/account/get":
			if !strings.Contains(r.Header.Get("Cookie"), "MUSIC_U="+secret) {
				t.Error("账号确认没有使用本次扫码获得的凭据")
			}
			body = `{"code":200,"profile":{"userId":700,"nickname":"测试账号"}}`
		default:
			t.Errorf("登录发生意外请求：%s", r.URL.Path)
			body = `{"code":404}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	p := loadAggregate(t)
	prompts := 0
	p.OnSignInPrompt(func(prompt music.SignInPrompt) {
		if strings.Contains(prompt.QR, "synthetic-qr-key") {
			prompts++
		}
	})
	account, err := p.SignIn()
	if err != nil {
		t.Fatalf("现成扫码登录未接入：%v", err)
	}
	if account.ID != "catalog:wy:account:700" || account.Name != "测试账号" || prompts == 0 || requests != 3 {
		t.Fatal("登录结果没有保留可恢复账号身份或扫码提示")
	}
	value := store.values[p.credentialTarget(`platform:["wy","700"]`)]
	if !strings.Contains(value, secret) {
		t.Fatal("登录凭据没有进入账号隔离的系统库")
	}
	p.mu.Lock()
	ordinary, marshalErr := json.Marshal(p.storage)
	p.mu.Unlock()
	if marshalErr != nil || strings.Contains(string(ordinary), secret) || strings.Contains(string(ordinary), "MUSIC_U") {
		t.Fatal("登录秘密泄漏到普通配置")
	}
}

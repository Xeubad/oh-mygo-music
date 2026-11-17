package plugin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 仅替换新网页登录、HTTP 和系统凭据边界，正式插件仍完成授权码交换与账号确认。
func qqAccountTest(t *testing.T, serve func(*http.Request, string) (string, error)) (*Plugin, *memoryCredentials) {
	t.Helper()
	previousStore, previousTransport, previousSignIn := systemCredentials, http.DefaultTransport, webSignIn
	store := &memoryCredentials{values: map[string]string{}}
	systemCredentials = store
	t.Cleanup(func() {
		systemCredentials = previousStore
		http.DefaultTransport = previousTransport
		webSignIn = previousSignIn
	})
	SetWebSignIn(func(o WebSignIn) (string, error) {
		if !strings.HasPrefix(o.URL, "https://graph.qq.com/oauth2.0/authorize?") || o.CookiesOf != "https://graph.qq.com/" || len(o.Until) != 3 {
			t.Error("QQ 登录没有使用独立的官方授权入口")
		}
		return "p_uin=o700; p_skey=fixture-graph-key; pt_oauth_token=fixture-oauth-token; unrelated=drop", nil
	})
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		if r.URL.Hostname() == "graph.qq.com" && r.URL.Path == "/oauth2.0/authorize" {
			if r.Method != http.MethodPost || !strings.Contains(r.Header.Get("Cookie"), "p_skey=fixture-graph-key") || strings.Contains(r.Header.Get("Cookie"), "unrelated") {
				t.Error("QQ 授权 Cookie 没有按域和名称隔离")
			}
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			if len(r.PostForm.Get("state")) < 16 {
				t.Error("QQ 授权缺少随机状态")
			}
			header.Set("Location", "https://y.qq.com/portal/wx_redirect.html?code=0123456789ABCDEF0123456789ABCDEF&state="+r.PostForm.Get("state"))
			return &http.Response{StatusCode: 302, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if r.URL.Hostname() != "u.y.qq.com" || r.URL.Path != "/cgi-bin/musicu.fcg" || r.Method != http.MethodPost {
			t.Fatalf("账号读取越过已核对边界：%s %s", r.Method, r.URL.Hostname()+r.URL.Path)
		}
		if strings.Contains(r.Header.Get("Cookie"), "fixture-graph-key") || r.Header.Get("X-API-Key") != "" {
			t.Error("QQ 音乐账号请求泄露全局 Cookie 或解析密钥")
		}
		var body struct {
			Comm map[string]any `json:"comm"`
			Req  struct {
				Method string `json:"method"`
			} `json:"req"`
			Read struct {
				Method string `json:"method"`
			} `json:"req_0"`
		}
		encoded, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			return nil, readErr
		}
		r.Body = io.NopCloser(strings.NewReader(string(encoded)))
		if err := json.Unmarshal(encoded, &body); err != nil {
			return nil, err
		}
		if body.Comm["ct"] != float64(24) || body.Comm["cv"] != float64(0) {
			t.Error("网页登录不能伪造 Android 设备上下文")
		}
		var response string
		var err error
		switch {
		case body.Req.Method == "QQLogin":
			response = `{"code":0,"req":{"code":0,"data":{"musicid":700,"musickey":"fixture-music-key","encryptUin":"fixture-encrypted-uin","loginType":2,"nick":"QQ测试账号","expired_at":4102444800}}}`
		case body.Read.Method == "GetLoginUserInfo":
			response = `{"code":0,"req_0":{"code":0,"data":{"info":{"nick":"QQ测试账号"}}}}`
		default:
			response, err = serve(r, body.Read.Method)
		}
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	p := loadAggregate(t)
	if err := p.SetSetting("accountPlatform", "tx"); err != nil {
		t.Fatal(err)
	}
	return p, store
}

// QQ 必须先交换音乐凭据并确认账号，完整会话只进系统库，不写普通配置。
func TestAggregateQQAccountUsesOfficialIsolatedWebSession(t *testing.T) {
	p, store := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		t.Errorf("登录不应请求其他读取：%s", method)
		return `{"code":500}`, nil
	})
	me, err := p.SignIn()
	if err != nil || me == nil || me.ID != "catalog:tx:account:700" || me.Name != "QQ测试账号" {
		t.Fatalf("QQ 官方会话未完成音乐账号确认：%v", err)
	}
	me, err = p.Account()
	if err != nil || me == nil || me.ID != "catalog:tx:account:700" {
		t.Fatalf("QQ 账号查询失去身份：%v", err)
	}
	key := p.credentialTarget(`platform:["tx","700"]`)
	if !strings.Contains(store.values[key], "fixture-music-key") || strings.Contains(store.values[key], "fixture-graph-key") {
		t.Fatal("QQ 音乐会话未保存到独立凭据或持久保存了全局授权 Cookie")
	}
	p.mu.Lock()
	stored, _ := json.Marshal(p.storage)
	p.mu.Unlock()
	if strings.Contains(string(stored), "fixture-music-key") || strings.Contains(string(stored), "fixture-graph-key") {
		t.Fatal("QQ 秘密落入普通存储")
	}
}

// QQ 喜欢列表完整读取后按原始 songmid 入快照，聚合候选可用通用服务解析。
func TestAggregateQQAccountLikedSnapshotAndPlaybackIdentity(t *testing.T) {
	p, _ := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		if method != "CgiGetDiss" || !strings.Contains(r.Header.Get("Cookie"), "qm_keyst=fixture-music-key") {
			t.Error("QQ 喜欢列表没有使用官方音乐会话")
		}
		return `{"code":0,"req_0":{"code":0,"data":{"total_song_num":2,"hasmore":0,"songlist":[{"mid":"FixtureSongA1","title":"第一首","interval":3,"singer":[]},{"mid":"FixtureSongB2","title":"第二首","interval":4,"singer":[]}]}}}`, nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	page, err := p.Liked(0)
	if err != nil || page.More || len(page.Tracks) != 2 || page.Tracks[0].ID != "catalog:tx:track:FixtureSongA1" {
		t.Fatalf("QQ 喜欢列表未完整接通：%v", err)
	}
	raw, err := p.Aggregate("snapshot", map[string]any{"platform": "tx", "accountID": "700", "listID": "liked"})
	var snapshot struct {
		Tracks []struct {
			ID        string   `json:"id"`
			Qualities []string `json:"qualities"`
		} `json:"tracks"`
	}
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Tracks) != 2 || snapshot.Tracks[0].ID != "FixtureSongA1" || len(snapshot.Tracks[0].Qualities) == 0 {
		t.Fatal("QQ 快照没有原始身份或解析声明")
	}
	home, err := p.Home()
	if err != nil || len(home.Sections) != 1 || len(home.Sections[0].Tracks) != 2 {
		t.Fatalf("QQ 喜欢列表未进入聚合：%v", err)
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	raw, err = p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("QQ 主动退出没有清理快照")
	}
}

// 账号歌单保留账号与原始歌单身份，浏览不会替用户选入聚合。
func TestAggregateQQAccountPlaylistRequiresSelection(t *testing.T) {
	p, _ := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		switch method {
		case "GetPlaylistByUin":
			return `{"code":0,"req_0":{"code":0,"data":{"v_playlist":[{"tid":900,"dirName":"我的QQ歌单","songNum":1}]}}}`, nil
		case "CgiGetPlaylistFavInfo":
			return `{"code":0,"req_0":{"code":0,"data":{"v_list":[],"total":0,"hasmore":0}}}`, nil
		case "CgiGetDiss":
			return `{"code":0,"req_0":{"code":0,"data":{"total_song_num":1,"hasmore":0,"songlist":[{"mid":"FixtureSongA1","title":"歌单歌曲","interval":3,"singer":[]}]}}}`, nil
		default:
			t.Errorf("意外账号请求：%s", method)
			return `{"code":500}`, nil
		}
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	lists, err := p.Playlists()
	if err != nil || len(lists) != 1 || lists[0].ID != "account:tx:700:playlist:900" {
		t.Fatalf("QQ 账号歌单目录未接通：%v", err)
	}
	list, err := p.Playlist(lists[0].ID)
	if err != nil || len(list.Tracks) != 1 || list.Name != "我的QQ歌单" {
		t.Fatalf("QQ 账号歌单未完整读取：%v", err)
	}
	home, err := p.Home()
	if err != nil || len(home.Sections) != 1 || len(home.Sections[0].Tracks) != 0 {
		t.Fatal("浏览 QQ 歌单就擅自选入聚合")
	}
	if _, err := p.Aggregate("selectList", map[string]any{"platform": "tx", "accountID": "700", "listID": "900", "selected": true}); err != nil {
		t.Fatal(err)
	}
	home, err = p.Home()
	if err != nil || len(home.Sections[0].Tracks) != 1 || home.Sections[0].Tracks[0].AggregateSources[0].TrackID != "FixtureSongA1" {
		t.Fatal("选择 QQ 歌单没有纳入原始来源")
	}
}

// QQ 手动刷新必须读取 QQ 喜欢列表，不得借公共入口误读网易。
func TestAggregateQQAccountManualRefreshUsesSelectedPlatform(t *testing.T) {
	reads := 0
	p, _ := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		if method != "CgiGetDiss" {
			t.Errorf("意外刷新请求：%s", method)
		}
		reads++
		return `{"code":0,"req_0":{"code":0,"data":{"total_song_num":0,"hasmore":0,"songlist":[]}}}`, nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("refresh", nil); err != nil {
		t.Fatalf("QQ 手动刷新仍走其他平台：%v", err)
	}
	if reads != 1 {
		t.Fatal("QQ 手动刷新没有读取本平台喜欢列表")
	}
	raw, err := p.Aggregate("refreshStatus", nil)
	var status struct {
		State string `json:"state"`
	}
	if err != nil || json.Unmarshal(raw, &status) != nil || status.State != "success" {
		t.Fatal("QQ 刷新完成状态没有更新")
	}
	raw, err = p.Aggregate("status", nil)
	var overview struct {
		Refresh struct {
			State string `json:"state"`
		} `json:"refresh"`
	}
	if err != nil || json.Unmarshal(raw, &overview) != nil || overview.Refresh.State != "success" {
		t.Fatal("设置页状态仍读取其他平台的刷新结果")
	}
}

// 账号平台与目录搜索独立：切到尚未登录的 QQ 后，查看或退出不能误用网易会话。
func TestAggregateQQAccountSelectionDoesNotSignOutNetEase(t *testing.T) {
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		t.Errorf("未登录 QQ 的账号操作不应请求平台：%s", r.URL.Path)
		return `{"code":404}`, nil
	})
	if err := p.SetSetting("accountPlatform", "tx"); err != nil {
		t.Fatalf("无法独立选择 QQ 账号入口：%v", err)
	}
	me, err := p.Account()
	if err != nil || me != nil {
		t.Fatalf("QQ 入口显示了其他平台账号：%v", err)
	}
	if err := p.SignOut(); err != nil {
		t.Fatalf("退出尚未登录的 QQ 失败：%v", err)
	}
	if err := p.SetSetting("accountPlatform", "wy"); err != nil {
		t.Fatal(err)
	}
	me, err = p.Account()
	if err != nil || me == nil || me.ID != "catalog:wy:account:700" {
		t.Fatalf("QQ 退出操作破坏了网易会话：%v", err)
	}
}

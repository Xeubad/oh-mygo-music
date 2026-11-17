package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 有限联网验收仅经正式插件读取公开 QQ 目录，最多三次请求，不登录、不解析、不播放。
func TestAggregateQQCatalogLiveReadOnly(t *testing.T) {
	if os.Getenv("MYGO_AGGREGATE_LIVE_QQ_CATALOG") != "1" {
		t.Skip("显式开启 QQ 公开目录联网检查")
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		allowed := r.URL.Hostname() == "u.y.qq.com" && r.URL.Path == "/cgi-bin/musicu.fcg" && r.Method == http.MethodPost || r.URL.Hostname() == "c.y.qq.com" && r.URL.Path == "/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg" && r.Method == http.MethodGet
		if requests > 3 || !allowed || r.Header.Get("X-Api-Key") != "" || strings.Contains(r.Header.Get("Cookie"), "MUSIC_U") {
			return nil, fmt.Errorf("公开目录请求超出本轮限定范围")
		}
		return previous.RoundTrip(r)
	})
	p := loadAggregate(t)
	if err := p.SetSetting("catalogPlatform", "tx"); err != nil {
		t.Fatal(err)
	}
	tracks, err := p.Search("纯音乐", music.SearchTracks, 0)
	if err != nil || len(tracks.Tracks) == 0 {
		t.Fatalf("QQ 实际公开曲目搜索未通过：%v", err)
	}
	for _, track := range tracks.Tracks {
		if !strings.HasPrefix(track.ID, "catalog:tx:track:") || track.Name == "" || track.Source != p.Info().ID {
			t.Fatal("QQ 实际搜索缺少歌曲身份")
		}
	}
	lists, err := p.Search("纯音乐", music.SearchPlaylists, 0)
	if err != nil || len(lists.Playlists) == 0 {
		t.Fatalf("QQ 实际公开歌单搜索未通过：%v", err)
	}
	var selected *music.Playlist
	for i := range lists.Playlists {
		if lists.Playlists[i].TrackCount > 0 && lists.Playlists[i].TrackCount <= 200 {
			selected = &lists.Playlists[i]
			break
		}
	}
	if selected == nil {
		t.Fatal("本次范围内没有适合有限验证的公开歌单，未扩大搜索")
	}
	list, err := p.Playlist(selected.ID)
	if err != nil || list == nil || list.ID != selected.ID || list.TrackCount == 0 || list.TrackCount != len(list.Tracks) {
		t.Fatalf("QQ 实际公开歌单未完整读取：%v", err)
	}
	for _, track := range list.Tracks {
		if !strings.HasPrefix(track.ID, "catalog:tx:track:") || track.Source != p.Info().ID {
			t.Fatal("QQ 实际歌单曲目身份丢失")
		}
	}
	t.Logf("QQ 公开曲目=%d，公开歌单=%d，完整读取=%d 首，请求=%d；未登录、未解析、未播放", len(tracks.Tracks), len(lists.Playlists), len(list.Tracks), requests)
}

// 公开 QQ 歌单完整返回原顺序及 songmid，但不能自动纳入账号列表或触发解析。
func TestAggregateQQCatalogPublicPlaylistDoesNotCreateAccount(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Hostname() != "c.y.qq.com" || r.URL.Path != "/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg" || q.Get("disstid") != "900" || q.Get("new_format") != "1" || r.Header.Get("Referer") != "https://y.qq.com/" {
			t.Error("QQ 歌单读取偏离已核对的公开接口")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("QQ 公开歌单不能携带账号或解析密钥")
		}
		response := `{"code":0,"subcode":0,"cdlist":[{"disstid":"900","dissname":"公开歌单","total_song_num":2,"songnum":2,"cur_song_num":2,"song_begin":0,"songlist":[{"mid":"FixtureSongB2","title":"第二首","interval":4,"singer":{"mid":"FixtureSingerA1","name":"歌手"}},{"mid":"FixtureSongA1","title":"第一首","interval":3,"singer":[],"album":{"mid":"FixtureAlbumA1","name":"专辑"}}]}]}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	p := loadAggregate(t)
	got, err := p.Playlist("catalog:tx:playlist:900")
	if err != nil {
		t.Fatalf("QQ 公开歌单读取失败：%v", err)
	}
	if got.ID != "catalog:tx:playlist:900" || got.Name != "公开歌单" || got.TrackCount != 2 || len(got.Tracks) != 2 || got.Tracks[0].ID != "catalog:tx:track:FixtureSongB2" || got.Tracks[1].ID != "catalog:tx:track:FixtureSongA1" || got.Tracks[0].Source != p.Info().ID || got.Tracks[0].ArtistNames() != "歌手" {
		t.Fatal("QQ 歌单没有保留完整歌曲及原始顺序")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("浏览 QQ 公开歌单不能创建账号快照")
	}
}

// QQ 歌单搜索保留 dissid，后续浏览不把数字身份混成歌曲 songmid。
func TestAggregateQQCatalogPlaylistSearch(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		var req struct {
			Param struct {
				Type int `json:"search_type"`
			} `json:"param"`
		}
		if json.Unmarshal(body["req_0"], &req) != nil || req.Param.Type != 3 {
			t.Error("QQ 歌单搜索没有使用歌单类型")
		}
		response := `{"code":0,"req_0":{"code":0,"data":{"code":0,"meta":{"curpage":1,"nextpage":-1},"body":{"item_songlist":[{"dissid":"900","dissname":"公开歌单","songnum":2,"logo":"https://example.test/cover.jpg"}]}}}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	p := loadAggregate(t)
	if err := p.SetSetting("catalogPlatform", "tx"); err != nil {
		t.Fatal(err)
	}
	got, err := p.Search("纯音乐", music.SearchPlaylists, 0)
	if err != nil {
		t.Fatalf("QQ 歌单搜索失败：%v", err)
	}
	if got.More || len(got.Playlists) != 1 || got.Playlists[0].ID != "catalog:tx:playlist:900" || got.Playlists[0].Name != "公开歌单" || got.Playlists[0].TrackCount != 2 {
		t.Fatal("QQ 歌单身份、数量或结束标记丢失")
	}
}

// 选择 QQ 目录后，搜索保留 songmid 和页码；公开读取不能借用网易登录或解析密钥。
func TestAggregateQQCatalogSearchPreservesSongMID(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != http.MethodPost || r.URL.Hostname() != "u.y.qq.com" || r.URL.Path != "/cgi-bin/musicu.fcg" {
			t.Errorf("QQ 搜索没有使用已核对的公开目录边界：%s %s", r.Method, r.URL.Hostname()+r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "tmeLoginType=-1" {
			t.Error("公开目录不能附带账号会话或解析密钥")
		}
		var body struct {
			Comm    map[string]any `json:"comm"`
			Request struct {
				Module string `json:"module"`
				Method string `json:"method"`
				Param  struct {
					Query string `json:"query"`
					Page  int    `json:"page_num"`
					Limit int    `json:"num_per_page"`
					Type  int    `json:"search_type"`
				} `json:"param"`
			} `json:"req_0"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.Request.Module != "music.search.SearchCgiService" || body.Request.Method != "DoSearchForQQMusicLite" ||
			body.Request.Param.Query != "测试录音" || body.Request.Param.Page != 2 || body.Request.Param.Limit != 20 || body.Request.Param.Type != 7 || body.Comm["tmeAppID"] != "qqmusiclight" {
			t.Error("QQ 匿名搜索身份、原始关键字或分页错误")
		}
		response := `{"code":0,"req_0":{"code":0,"data":{"code":0,"meta":{"sum":21,"curpage":2,"nextpage":-1},"body":{"item_song":[{"id":101,"mid":"FixtureSongA1","title":"测试录音","interval":180,"singer":[{"id":201,"mid":"FixtureSingerA1","name":"测试歌手"}],"album":{"id":301,"mid":"FixtureAlbumA1","name":"测试专辑"},"file":{"media_mid":"DifferentMediaMID"}}]}}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	p := loadAggregate(t)
	if err := p.SetSetting("catalogPlatform", "tx"); err != nil {
		t.Fatal(err)
	}
	got, err := p.Search("测试录音", music.SearchTracks, 20)
	if err != nil {
		t.Fatalf("QQ 公开搜索失败：%v", err)
	}
	if len(got.Tracks) != 1 || got.More || requests != 1 {
		t.Fatal("QQ 搜索未保留分页结果")
	}
	track := got.Tracks[0]
	if track.ID != "catalog:tx:track:FixtureSongA1" || track.Name != "测试录音" || track.Source != p.Info().ID || track.Duration != 180000 || track.ArtistNames() != "测试歌手" || track.Album.ID != "catalog:tx:album:FixtureAlbumA1" {
		t.Fatalf("QQ 曲目身份或元数据丢失：%+v", track)
	}
	// 可用标记只代表解析路径已接通，不保证服务存在该曲目或实际媒体可播放。
	if track.Unavailable {
		t.Fatal("已接入解析路径的 QQ 目录曲目仍被标记为不可用")
	}
	me, err := p.Account()
	if err != nil || me != nil {
		t.Fatal("公开 QQ 搜索不能创建账号")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("公开 QQ 搜索不能写入账号快照")
	}
}

// QQ 公开曲目应能经正式播放计划接入解析，但搜索本身仍不能发起解析请求。
func TestAggregateQQCatalogTrackEntersPlaybackPlan(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Hostname() != "u.y.qq.com" {
			t.Errorf("目录搜索意外访问解析或其他服务：%s", r.URL.Hostname())
		}
		response := `{"code":0,"req_0":{"code":0,"data":{"code":0,"meta":{"curpage":1,"nextpage":-1},"body":{"item_song":[{"mid":"FixtureSongA1","title":"测试录音","interval":180,"singer":[]}]}}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	p := loadAggregate(t)
	if err := p.SetSetting("catalogPlatform", "tx"); err != nil {
		t.Fatal(err)
	}
	got, err := p.Search("测试录音", music.SearchTracks, 0)
	if err != nil || len(got.Tracks) != 1 {
		t.Fatalf("QQ 公开搜索失败：%v", err)
	}
	track := got.Tracks[0]
	if track.Unavailable {
		t.Fatal("已接入通用解析的 QQ 目录曲目仍标记为不可用")
	}
	plan, err := p.PlaybackPlan(context.Background(), track.ID, "high", "qq-catalog-playback")
	if err != nil || plan == nil || len(plan.Candidates) == 0 {
		t.Fatalf("QQ 目录曲目未生成正式解析候选：%v", err)
	}
	candidate := plan.Candidates[0]
	if candidate.Platform != "tx" || candidate.AccountID != "public" || candidate.TrackID != "FixtureSongA1" || candidate.PlaybackID != track.ID {
		t.Fatal("QQ 目录解析候选没有保留来源身份")
	}
	if requests != 1 {
		t.Fatal("浏览 QQ 目录不应自动调用解析服务")
	}
}

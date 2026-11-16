package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 用真实宿主和已发布平台协议的 HTTP 边界，避免把适配器替身当成接入实现。
type aggregateCatalogTransport func(*http.Request) (*http.Response, error)

func (f aggregateCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// 联网检查显式启用，只读公开目录；不登录、不解析播放地址，也不读取已有账号凭据。
func TestAggregateCatalogNeteaseLiveReadOnly(t *testing.T) {
	if os.Getenv("MYGO_AGGREGATE_LIVE_CATALOG") != "1" {
		t.Skip("显式启用 MYGO_AGGREGATE_LIVE_CATALOG=1 后验证公开目录")
	}
	p := loadAggregate(t)
	tracks, err := p.Search("晴天", music.SearchTracks, 0)
	if err != nil {
		t.Fatalf("真实公开歌曲搜索失败：%v", err)
	}
	if len(tracks.Tracks) == 0 {
		t.Fatal("真实搜索未返回歌曲")
	}
	for _, track := range tracks.Tracks {
		if !strings.HasPrefix(track.ID, "catalog:wy:track:") || track.Name == "" || track.Source != p.Info().ID {
			t.Fatal("真实歌曲未保留可恢复的平台身份")
		}
	}
	lists, err := p.Search("纯音乐", music.SearchPlaylists, 0)
	if err != nil {
		t.Fatalf("真实公开歌单搜索失败：%v", err)
	}
	var selected *music.Playlist
	for i := range lists.Playlists {
		if count := lists.Playlists[i].TrackCount; count > 0 && count <= 100 {
			selected = &lists.Playlists[i]
			break
		}
	}
	if selected == nil {
		t.Fatal("当前搜索结果没有适合有限只读验证的小型歌单；未扩大请求")
	}
	playlist, err := p.Playlist(selected.ID)
	if err != nil {
		t.Fatalf("真实公开歌单读取失败：%v", err)
	}
	if playlist.ID != selected.ID || playlist.TrackCount == 0 || len(playlist.Tracks) != playlist.TrackCount {
		t.Fatal("真实公开歌单未完整返回，不计为接入通过")
	}
	for _, track := range playlist.Tracks {
		if !strings.HasPrefix(track.ID, "catalog:wy:track:") || track.Source != p.Info().ID {
			t.Fatal("真实歌单歌曲缺少可恢复的平台身份")
		}
	}
	t.Logf("公开歌曲=%d，公开歌单=%d，实际完整读取=%d 首；未登录、未解析、未播放", len(tracks.Tracks), len(lists.Playlists), len(playlist.Tracks))
}

// 服务声称三首却只返回两个身份时，不能把局部歌单作为完整结果展示或提交。
func TestAggregateCatalogNeteaseRejectsTruncatedPlaylist(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/eapi/v6/playlist/detail":
			body = `{"code":200,"playlist":{"id":900,"name":"截断歌单","trackCount":3,"trackIds":[{"id":101},{"id":102}]}}`
		case "/eapi/v3/song/detail":
			body = `{"code":200,"songs":[{"id":101,"name":"第一首"},{"id":102,"name":"第二首"}]}`
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
			body = `{"code":404}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	p := loadAggregate(t)
	got, err := p.Playlist("catalog:wy:playlist:900")
	if !errors.Is(err, music.ErrUnavailable) || got != nil {
		t.Fatal("截断歌单被错误地当作完整结果")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("不完整歌单污染了账号聚合快照")
	}
}

// 缺少服务原始总数时，上游回退到已读数量；聚合不能据此证明歌单完整。
func TestAggregateCatalogNeteaseRequiresOriginalTrackCount(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":200,"playlist":{"id":900,"trackIds":[{"id":101},{"id":102}]}}`
		if r.URL.Path == "/eapi/v3/song/detail" {
			body = `{"code":200,"songs":[{"id":101,"name":"第一首"},{"id":102,"name":"第二首"}]}`
		} else if r.URL.Path != "/eapi/v6/playlist/detail" {
			t.Errorf("意外请求：%s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	p := loadAggregate(t)
	got, err := p.Playlist("catalog:wy:playlist:900")
	if !errors.Is(err, music.ErrUnavailable) || got != nil {
		t.Fatal("缺少原始总数的歌单被错误地当作完整结果")
	}
}

// 上游会分批补齐详情；聚合不能依赖详情返回顺序，也不能丢掉下架歌曲的位置。
func TestAggregateCatalogNeteasePlaylistPreservesPositions(t *testing.T) {
	previous := http.DefaultTransport
	paths := []string{}
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if r.URL.Hostname() != "interface.music.163.com" || r.Method != http.MethodPost {
			t.Error("歌单请求离开已核实的只读平台边界")
		}
		var body string
		switch r.URL.Path {
		case "/eapi/v6/playlist/detail":
			body = `{"code":200,"playlist":{"id":900,"name":"公开歌单","trackCount":3,"trackIds":[{"id":101},{"id":102},{"id":103}]}}`
		case "/eapi/v3/song/detail":
			body = `{"code":200,"songs":[{"id":103,"name":"第三首","dt":3000},{"id":101,"name":"第一首","dt":1000}]}`
		default:
			t.Errorf("意外的平台请求：%s", r.URL.Path)
			body = `{"code":404}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	p := loadAggregate(t)
	got, err := p.Playlist("catalog:wy:playlist:900")
	if err != nil {
		t.Fatalf("已复用的平台歌单不能读取：%v", err)
	}
	if got.ID != "catalog:wy:playlist:900" || got.Name != "公开歌单" || got.TrackCount != 3 || len(got.Tracks) != 3 {
		t.Fatal("歌单身份或完整歌曲位置丢失")
	}
	for i, id := range []string{"101", "102", "103"} {
		if got.Tracks[i].ID != "catalog:wy:track:"+id || got.Tracks[i].Source != p.Info().ID {
			t.Fatalf("第 %d 个歌曲位置或来源身份改变", i)
		}
	}
	if !got.Tracks[1].Unavailable || len(paths) != 2 {
		t.Fatal("下架歌曲未保留为不可用位置，或产生额外请求")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("浏览公开歌单不能自动纳入账号喜欢或聚合列表")
	}
}

// 聚合复用开源网易云搜索，保留分页和原始 ID 的可恢复命名空间，不虚构登录账号。
func TestAggregateCatalogNeteaseSearchUsesUpstream(t *testing.T) {
	previous := http.DefaultTransport
	requests := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Hostname() != "interface.music.163.com" || r.URL.Path != "/eapi/search/get" || r.Method != http.MethodPost {
			t.Errorf("搜索没有使用已核实的上游请求：%s %s", r.Method, r.URL.Hostname()+r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("params") == "" {
			t.Error("现成适配器未生成平台要求的请求参数")
		}
		response := `{"code":200,"result":{"songs":[{"id":347230,"name":"测试录音","ar":[{"id":11,"name":"测试歌手"}],"al":{"id":22,"name":"测试专辑"},"dt":185000}],"songCount":102}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	p := loadAggregate(t)
	got, err := p.Search("测试录音", music.SearchTracks, 50)
	if err != nil {
		t.Fatalf("已复用的平台搜索不能调用：%v", err)
	}
	if requests != 1 || len(got.Tracks) != 1 || !got.More {
		t.Fatalf("搜索分页或请求次数不符：requests=%d tracks=%d more=%v", requests, len(got.Tracks), got.More)
	}
	track := got.Tracks[0]
	if track.ID != "catalog:wy:track:347230" || track.Source != p.Info().ID || track.Name != "测试录音" || track.Duration != 185000 || track.ArtistNames() != "测试歌手" {
		t.Fatalf("聚合搜索丢失原始歌曲信息或身份：%+v", track)
	}
	account, err := p.Account()
	if err != nil || account != nil {
		t.Fatal("公开搜索不应被当成已登录账号")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []any
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 0 {
		t.Fatal("公开搜索不应伪造账号列表快照")
	}
}

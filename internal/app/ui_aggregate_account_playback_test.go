package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 聚合页点击账号歌单曲目后仍使用冻结账号身份和完整队列，不能变成公开目录点播或修改列表选择。
func TestAggregateAccountTrackClickKeepsFrozenPlaybackIdentity(t *testing.T) {
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	resolved := make(chan map[string]string, 8)
	http.DefaultTransport = aggregateAccountPlaybackTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != "c.wwwweb.top" || r.URL.Path != "/music/url" {
			t.Errorf("点歌触发了非预期网络请求")
		}
		if r.Header.Get("X-Api-Key") != "fixture-account-ui-key" || r.Header.Get("Cookie") != "" {
			t.Error("账号点歌没有隔离解析密钥与平台 Cookie")
		}
		var request map[string]string
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("无法读取解析请求")
		}
		resolved <- request
		// 本接缝只观察真实插件发出的解析请求；固定失败保证测试不会访问媒体或打开声卡。
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":500}`)), Request: r}, nil
	})
	a, src, tt, post := aggregateFixture(t)
	for _, input := range []map[string]any{
		{"platform": "wy", "accountID": "700", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []map[string]any{{"id": "101", "name": "Account liked track", "qualities": []string{"128k"}}}},
		{"platform": "wy", "accountID": "700", "listID": "900", "selected": true, "complete": true, "refreshedAt": 2, "tracks": []map[string]any{{"id": "102", "name": "Selected account track", "qualities": []string{"128k"}}}},
		{"platform": "wy", "accountID": "700", "listID": "901", "selected": false, "complete": true, "refreshedAt": 3, "tracks": []map[string]any{{"id": "103", "name": "Excluded account track", "qualities": []string{"128k"}}}},
	} {
		if _, err := src.Aggregate("commitSnapshot", input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.Aggregate("setResolverKey", map[string]any{"platform": "wy", "value": "fixture-account-ui-key", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Aggregate("configure", map[string]any{"targetQuality": "128k", "minimumQuality": "128k", "consecutiveFailureLimit": 1}); err != nil {
		t.Fatal(err)
	}
	page, err := src.Home()
	if err != nil || len(page.Sections) != 1 || len(page.Sections[0].Tracks) != 2 {
		t.Fatal("完整账号列表未形成聚合页", err)
	}
	a.library.home = page
	tt.Frame()
	if err := tt.Click("Selected account track"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !a.pl.Loading() })
	select {
	case request := <-resolved:
		if len(request) != 3 || request["source"] != "wy" || request["musicId"] != "102" || request["quality"] != "128k" {
			t.Fatalf("聚合点歌没有解析所选账号曲目的原始身份: %+v", request)
		}
	default:
		t.Fatal("聚合点歌没有经过真实插件的解析路径")
	}
	queue := a.pl.Queue()
	if len(queue) != 2 {
		t.Fatalf("账号点歌丢失完整来源队列: %d", len(queue))
	}
	for i, track := range queue {
		if track.ID != page.Sections[0].Tracks[i].ID || track.Source != src.Info().ID || !strings.HasPrefix(track.ID, "aggregate:") {
			t.Fatal("账号队列的冻结身份、来源或原顺序被替换")
		}
	}
	var selectedTrack music.Track
	for _, track := range queue {
		if track.Name == "Selected account track" {
			selectedTrack = track
			break
		}
	}
	// 测试所点的是账号曲目，不绑定快照字段的枚举顺序或歌单行位置。
	var frozen struct {
		Version  int                     `json:"version"`
		Identity music.AggregateSource   `json:"identity"`
		Sources  []music.AggregateSource `json:"sources"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(selectedTrack.ID, "aggregate:")), &frozen) != nil || frozen.Version != 1 ||
		frozen.Identity.Platform != "wy" || frozen.Identity.AccountID != "700" || frozen.Identity.TrackID != "102" || len(frozen.Sources) != 1 || frozen.Sources[0] != frozen.Identity {
		t.Fatalf("所选曲目不再携带唯一的网易账号冻结来源: %+v", frozen)
	}
	raw, err := src.Aggregate("lists", nil)
	var lists []struct {
		ListID   string `json:"listID"`
		Selected bool   `json:"selected"`
	}
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 3 {
		t.Fatal("点歌改变了账号缓存列表", err)
	}
	selected := map[string]bool{}
	for _, list := range lists {
		selected[list.ListID] = list.Selected
	}
	if !selected["900"] || selected["901"] || selected["liked"] {
		t.Fatal("点歌修改了歌单的聚合选择")
	}
}

type aggregateAccountPlaybackTransport func(*http.Request) (*http.Response, error)

func (f aggregateAccountPlaybackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

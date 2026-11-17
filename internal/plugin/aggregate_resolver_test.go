package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 网易解析只发送原始数字 ID 与独立密钥，不挪用其他平台密钥或声称请求档位即实际质量。
func TestAggregateWyResolverUsesDedicatedKeyAndOriginalID(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	oldStore, oldTransport := systemCredentials, http.DefaultTransport
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = oldStore; http.DefaultTransport = oldTransport })
	calls := 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "c.wwwweb.top" || r.URL.Path != "/music/url" || r.Method != "POST" {
			t.Errorf("没有使用网易专用 POST 契约")
		}
		if r.Header.Get("X-Api-Key") != "fixture-wy-key" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
			t.Error("凭据不正确或被复制到查询参数/Cookie")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 3 || body["source"] != "wy" || body["musicId"] != "101" || body["quality"] != "128k" {
			t.Errorf("没有传递原始网易身份及请求档位: %v", body)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"url":"https://audio.example.test/signed-media"}`)), Request: r}, nil
	})
	p := loadAggregate(t)
	if _, err := p.Aggregate("setResolverKey", map[string]any{"value": "fixture-other-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("setResolverKey", map[string]any{"platform": "wy", "value": "fixture-wy-key", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": "700", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []map[string]any{{"id": "101", "qualities": []string{"128k"}}}}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 1 {
		t.Fatal("没有取得聚合队列曲目", err)
	}
	id := page.Sections[0].Tracks[0].ID
	plan, err := p.PlaybackPlan(context.Background(), id, "high", "wy-first-round")
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatal("没有取得已声明的候选", err)
	}
	result, err := p.ResolveCandidate(context.Background(), id, "wy-first-round", plan.Candidates[0])
	if err != nil {
		t.Fatalf("网易专用解析尚未接入: %v", err)
	}
	if calls != 1 || result.Platform != "wy" || result.RequestedQuality != "128k" || result.Stream.URL != "https://audio.example.test/signed-media" {
		t.Fatal("解析结果没有保留候选身份与请求档位")
	}
	if result.Stream.Quality != "" || result.Stream.BitRate != 0 || len(result.Stream.Headers) != 0 {
		t.Fatal("解析标签冒充实际质量或把密钥带到媒体请求")
	}
	if store.values[p.credentialTarget("resolver")] != "fixture-other-key" {
		t.Fatal("网易密钥覆盖了其他平台密钥")
	}
}

// 完整网易列表采用服务声明档位；下架占位不能获得播放候选。
func TestAggregateWyLibraryBuildsPlanFromServiceDeclaration(t *testing.T) {
	p, _ := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		switch r.URL.Path {
		case "/eapi/song/like/get":
			return `{"code":200,"ids":[101,102]}`, nil
		case "/eapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":101,"name":"可读取曲目"}]}`, nil
		default:
			t.Error("意外的账号读取")
			return `{"code":404}`, nil
		}
	})
	if _, err := p.Liked(0); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 2 {
		t.Fatal("完整喜欢列表未进入聚合", err)
	}
	plan, err := p.PlaybackPlan(context.Background(), page.Sections[0].Tracks[0].ID, "high", "declared-wy")
	if err != nil || len(plan.Candidates) != 3 {
		t.Fatalf("网易列表没有生成服务声明的普通档候选: %+v %v", plan, err)
	}
	for i, quality := range []string{"flac", "320k", "128k"} {
		if plan.Candidates[i].Quality != quality || plan.Candidates[i].TrackID != "101" {
			t.Fatal("声明档位或原始曲目身份被改写")
		}
	}
	missing, err := p.PlaybackPlan(context.Background(), page.Sections[0].Tracks[1].ID, "high", "missing-wy")
	if err != nil || len(missing.Candidates) != 0 {
		t.Fatal("下架占位被误当成支持全部服务档位")
	}
}

// 从公开歌单直接点播只使用该网易原始身份，不虚构登录账号或把目录结果写成云端缓存。
func TestAggregateWyCatalogPlaylistCanPlanWithoutAccount(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":404}`
		switch r.URL.Path {
		case "/eapi/v6/playlist/detail":
			body = `{"code":200,"playlist":{"id":900,"name":"公开歌单","trackCount":1,"trackIds":[{"id":101}]}}`
		case "/eapi/v3/song/detail":
			body = `{"code":200,"songs":[{"id":101,"name":"公开曲目"}]}`
		default:
			t.Error("播放计划不应增加隐式平台请求")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	p := loadAggregate(t)
	list, err := p.Playlist("catalog:wy:playlist:900")
	if err != nil || len(list.Tracks) != 1 {
		t.Fatal("公开目录读取失败", err)
	}
	plan, err := p.PlaybackPlan(context.Background(), list.Tracks[0].ID, "high", "catalog-round")
	if err != nil || len(plan.Candidates) != 3 {
		t.Fatalf("目录歌曲无法进入网易专用播放: %+v %v", plan, err)
	}
	for _, candidate := range plan.Candidates {
		if candidate.Platform != "wy" || candidate.TrackID != "101" || candidate.AccountID != "public" || candidate.PlaybackID != list.Tracks[0].ID {
			t.Fatal("目录候选混入账号或替换身份")
		}
		valid, err := p.CandidateValid(context.Background(), list.Tracks[0].ID, "catalog-round", candidate)
		if err != nil || !valid {
			t.Fatal("有效的目录候选被拒绝", err)
		}
		candidate.TrackID = "102"
		valid, err = p.CandidateValid(context.Background(), list.Tracks[0].ID, "catalog-round", candidate)
		if err != nil || valid {
			t.Fatal("目录候选错误地允许切换原始身份")
		}
	}
	raw, err := p.Aggregate("lists", nil)
	if err != nil || string(raw) != "[]" {
		t.Fatal("公开目录污染了账号列表缓存", err)
	}
}

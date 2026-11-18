package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// 真实喜欢列表会返回没有 mid 的歌曲；保留数字原始身份的不可播占位，不能拖垮整张列表或冒充 songmid。
func TestAggregateQQAccountKeepsMissingMIDAsUnavailable(t *testing.T) {
	p, _ := qqAccountTest(t, func(_ *http.Request, method string) (string, error) {
		if method != "CgiGetDiss" {
			t.Fatalf("意外列表读取: %s", method)
		}
		return qqFixtureSongs(3, 0, qqFixtureSong("PlayableA"), map[string]any{"id": 501, "mid": "", "title": "无播放标识一", "interval": 12, "singer": []any{}}, map[string]any{"id": 502, "title": "无播放标识二", "interval": 0, "singer": []any{}}), nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	page, err := p.Liked(0)
	if err != nil || len(page.Tracks) != 3 || page.More {
		t.Fatalf("缺少mid导致完整列表被拒绝: %v", err)
	}
	if page.Tracks[0].Unavailable || !page.Tracks[1].Unavailable || !page.Tracks[2].Unavailable || page.Tracks[1].ID != "catalog:tx:unavailable-songid:501" || page.Tracks[2].ID == page.Tracks[1].ID {
		t.Fatal("占位身份、可播标记或条目数量错误")
	}
	var snapshot struct {
		Tracks []struct {
			ID          string
			Unavailable bool
			Qualities   []string
		}
	}
	if json.Unmarshal([]byte(qqSnapshotBody(t, p)), &snapshot) != nil || len(snapshot.Tracks) != 3 || snapshot.Tracks[0].ID != "PlayableA" || snapshot.Tracks[1].ID != "unavailable-songid:501" || len(snapshot.Tracks[1].Qualities) != 0 {
		t.Fatal("快照丢弃条目或把数字id声明为播放候选")
	}
	home, err := p.Home()
	if err != nil || len(home.Sections) != 1 || len(home.Sections[0].Tracks) != 3 {
		t.Fatalf("聚合首页没有保留完整列表: %v", err)
	}
	for _, id := range []string{page.Tracks[1].ID, home.Sections[0].Tracks[1].ID} {
		plan, err := p.PlaybackPlan(context.Background(), id, "128k", "placeholder-round")
		if err != nil || plan == nil || len(plan.Candidates) != 0 {
			t.Fatalf("不可播占位产生解析候选: %v", err)
		}
	}
}

// 自建目录必须保留所属标记，且收藏同一张自建歌单不能覆盖此归属。
func TestAggregateQQAccountPreservesOwnedPlaylistFlag(t *testing.T) {
	p, _ := qqAccountTest(t, func(_ *http.Request, method string) (string, error) {
		switch method {
		case "GetPlaylistByUin":
			return `{"code":0,"req_0":{"code":0,"data":{"v_playlist":[{"tid":900,"dirName":"自建","songNum":1}]}}}`, nil
		case "CgiGetPlaylistFavInfo":
			return `{"code":0,"req_0":{"code":0,"data":{"v_list":[{"tid":900,"dirName":"自建","songNum":1},{"tid":901,"dirName":"收藏","songNum":1}],"total":2,"hasmore":0}}}`, nil
		}
		t.Fatalf("意外目录请求: %s", method)
		return "", nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	lists, err := p.Playlists()
	if err != nil || len(lists) != 2 || !lists[0].Mine || lists[1].Mine {
		t.Fatalf("自建与收藏归属丢失: %v", err)
	}
}

// 占位仍计入原始分页偏移，跨页不能跳过它或覆盖正常歌曲。
func TestAggregateQQAccountUnavailableRowsKeepPagingOffset(t *testing.T) {
	calls := 0
	p, _ := qqAccountTest(t, func(r *http.Request, _ string) (string, error) {
		var body struct {
			Read struct {
				Param struct {
					Begin int `json:"song_begin"`
				} `json:"param"`
			} `json:"req_0"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("请求无法解析")
		}
		if body.Read.Param.Begin != calls*100 {
			t.Fatal("占位改变了分页偏移")
		}
		calls++
		if calls == 2 {
			return qqFixtureSongs(101, 0, qqFixtureSong("LastA")), nil
		}
		rows := make([]map[string]any, 100)
		for i := range rows {
			rows[i] = qqFixtureSong(fmt.Sprintf("Playable%d", i))
		}
		rows[50] = map[string]any{"id": 501, "mid": "", "title": "不可播", "interval": 0}
		return qqFixtureSongs(101, 1, rows...), nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	page, err := p.Liked(100)
	if err != nil || len(page.Tracks) != 1 || page.Tracks[0].ID != "catalog:tx:track:LastA" || calls != 2 {
		t.Fatalf("占位影响完整分页: %v", err)
	}
}

// 只为确实缺 mid 且有可靠数字身份的条目保留占位，其余异常与重复仍须保留旧快照。
func TestAggregateQQAccountUnavailableDoesNotRelaxIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []map[string]any
	}{
		{"no-identity", []map[string]any{{"mid": "", "title": "缺失", "interval": 0}}},
		{"zero-id", []map[string]any{{"id": 0, "mid": "", "title": "缺失", "interval": 0}}},
		{"string-id", []map[string]any{{"id": "501", "mid": "", "title": "缺失", "interval": 0}}},
		{"malformed-mid", []map[string]any{{"id": 501, "mid": "bad_mid", "title": "异常", "interval": 0}}},
		{"duplicate-placeholder", []map[string]any{{"id": 501, "mid": "", "title": "占位", "interval": 0}, {"id": 501, "mid": "", "title": "重复", "interval": 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := false
			p, _ := qqAccountTest(t, func(_ *http.Request, _ string) (string, error) {
				if bad {
					return qqFixtureSongs(len(tc.rows), 0, tc.rows...), nil
				}
				return qqFixtureSongs(1, 0, qqFixtureSong("OldA")), nil
			})
			if _, err := p.SignIn(); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Liked(0); err != nil {
				t.Fatal(err)
			}
			before := qqSnapshotBody(t, p)
			bad = true
			if _, err := p.Liked(0); err == nil {
				t.Fatal("不可靠或重复身份被接受")
			}
			if qqSnapshotBody(t, p) != before {
				t.Fatal("异常覆盖旧快照")
			}
		})
	}
}

package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"mygo-music/internal/music"
)

func qqFixtureSong(id string) map[string]any {
	return map[string]any{"mid": id, "title": id, "interval": 3, "singer": []any{}}
}
func qqFixtureSongs(total int, more int, songs ...map[string]any) string {
	if songs == nil {
		songs = []map[string]any{}
	}
	raw, _ := json.Marshal(map[string]any{"code": 0, "req_0": map[string]any{"code": 0, "data": map[string]any{"total_song_num": total, "hasmore": more, "songlist": songs}}})
	return string(raw)
}
func qqSnapshotBody(t *testing.T, p *Plugin) string {
	t.Helper()
	raw, err := p.Aggregate("snapshot", map[string]any{"platform": "tx", "accountID": "700", "listID": "liked"})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 100+1 首跨页只按原始偏移读取，先完整入快照再裁出宿主请求的展示页。
func TestAggregateQQAccountLikedPagesBeforeSlicing(t *testing.T) {
	requests := 0
	p, _ := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		var body struct {
			Read struct {
				Param struct {
					Begin  int    `json:"song_begin"`
					Num    int    `json:"song_num"`
					DirID  int    `json:"dirid"`
					DissID int    `json:"disstid"`
					Owner  string `json:"enc_host_uin"`
				} `json:"param"`
			} `json:"req_0"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", err
		}
		q := body.Read.Param
		if method != "CgiGetDiss" || q.Begin != requests*100 || q.Num != 100 || q.DirID != 201 || q.DissID != 0 || q.Owner != "fixture-encrypted-uin" {
			t.Error("QQ 喜欢列表偏移或账号身份错误")
		}
		requests++
		if q.Begin == 100 {
			return qqFixtureSongs(101, 0, qqFixtureSong("Fixture100")), nil
		}
		songs := make([]map[string]any, 100)
		for i := range songs {
			songs[i] = qqFixtureSong(fmt.Sprintf("Fixture%d", i))
		}
		return qqFixtureSongs(101, 1, songs...), nil
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	page, err := p.Liked(100)
	if err != nil || page.More || len(page.Tracks) != 1 || page.Tracks[0].ID != "catalog:tx:track:Fixture100" || requests != 2 {
		t.Fatalf("QQ 完整跨页读取失败：%v", err)
	}
	var snapshot struct {
		Tracks []any `json:"tracks"`
	}
	if json.Unmarshal([]byte(qqSnapshotBody(t, p)), &snapshot) != nil || len(snapshot.Tracks) != 101 {
		t.Fatal("QQ 只把展示页写入了完整快照")
	}
}

// 缺页、身份重复、总数漂移和普通网络错误都不能覆盖完整快照或误清账号。
func TestAggregateQQAccountIncompleteReadsKeepSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bodies  []string
		network bool
	}{
		{name: "missing-total", bodies: []string{`{"code":0,"req_0":{"code":0,"data":{"hasmore":0,"songlist":[]}}}`}},
		{name: "short-final", bodies: []string{qqFixtureSongs(2, 0, qqFixtureSong("NewA"))}},
		{name: "empty-more", bodies: []string{qqFixtureSongs(2, 1)}},
		{name: "duplicate-page", bodies: []string{qqFixtureSongs(2, 1, qqFixtureSong("NewA")), qqFixtureSongs(2, 0, qqFixtureSong("NewA"))}},
		{name: "total-drift", bodies: []string{qqFixtureSongs(2, 1, qqFixtureSong("NewA")), qqFixtureSongs(3, 0, qqFixtureSong("NewB"))}},
		{name: "unknown-business-error", bodies: []string{`{"code":0,"req_0":{"code":12345,"message":"fixture-music-key"}}`}},
		{name: "network-error", network: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad, calls := false, 0
			p, _ := qqAccountTest(t, func(*http.Request, string) (string, error) {
				if !bad {
					return qqFixtureSongs(1, 0, qqFixtureSong("OldA")), nil
				}
				if tc.network {
					return "", fmt.Errorf("fixture-music-key https://example.test/private")
				}
				if calls >= len(tc.bodies) {
					t.Error("失败后继续了未知分页")
					return `{"code":500}`, nil
				}
				body := tc.bodies[calls]
				calls++
				return body, nil
			})
			if _, err := p.SignIn(); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Liked(0); err != nil {
				t.Fatal(err)
			}
			before := qqSnapshotBody(t, p)
			bad = true
			if _, err := p.Liked(0); err == nil || errors.Is(err, music.ErrSignIn) || strings.Contains(err.Error(), "fixture-music-key") || strings.Contains(err.Error(), "https://example.test") {
				t.Fatalf("读取失败分类或脱敏错误：%v", err)
			}
			if qqSnapshotBody(t, p) != before {
				t.Fatal("不完整读取覆盖了旧快照或刷新时间")
			}
			if me, err := p.Account(); err != nil || me == nil {
				t.Fatalf("普通失败误清 QQ 会话：%v", err)
			}
		})
	}
}

// 同账号旧回包不得越过主动退出或较新成功提交；较新失败则不应丢弃仍完整的旧结果。
func TestAggregateQQAccountReadWatermarks(t *testing.T) {
	for _, mode := range []string{"logout", "newer-success", "newer-failed"} {
		t.Run(mode, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			calls := 0
			p, _ := qqAccountTest(t, func(*http.Request, string) (string, error) {
				calls++
				if calls == 1 {
					close(started)
					select {
					case <-release:
					case <-time.After(3 * time.Second):
						return "", fmt.Errorf("test release timed out")
					}
					return qqFixtureSongs(1, 0, qqFixtureSong("OldA")), nil
				}
				if mode == "newer-failed" {
					return `{"code":500}`, nil
				}
				return qqFixtureSongs(1, 0, qqFixtureSong("NewA")), nil
			})
			if _, err := p.SignIn(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := p.Liked(0); done <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("旧读取未到达 HTTP 边界")
			}
			if mode == "logout" {
				if err := p.SignOut(); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := p.Liked(0)
				if (err != nil) != (mode == "newer-failed") {
					t.Fatalf("新读取结果错误：%v", err)
				}
			}
			unblock()
			var oldErr error
			select {
			case oldErr = <-done:
			case <-time.After(time.Second):
				t.Fatal("旧读取没有结束")
			}
			snapshot := qqSnapshotBody(t, p)
			switch mode {
			case "logout":
				if oldErr == nil || strings.Contains(snapshot, "OldA") {
					t.Fatal("退出后的旧回包恢复了缓存")
				}
			case "newer-success":
				if oldErr == nil || !strings.Contains(snapshot, "NewA") || strings.Contains(snapshot, "OldA") {
					t.Fatal("旧读取覆盖了较新成功快照")
				}
			case "newer-failed":
				if oldErr != nil || !strings.Contains(snapshot, "OldA") {
					t.Fatalf("较新失败错误地阻止旧完整结果：%v", oldErr)
				}
			}
		})
	}
}

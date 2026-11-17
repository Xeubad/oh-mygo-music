package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 后续页必须推进且非末页完整，否则客户端按返回条数累计 offset 会重复读取同页。
func TestAggregateQQCatalogRejectsNonAdvancingSearchPages(t *testing.T) {
	for _, scenario := range []string{"empty-more", "short-more", "repeated-page"} {
		t.Run(scenario, func(t *testing.T) {
			rows := []any{}
			count := 20
			if scenario == "empty-more" {
				count = 0
			}
			if scenario == "short-more" {
				count = 1
			}
			for i := 0; i < count; i++ {
				rows = append(rows, map[string]any{"mid": fmt.Sprintf("FixtureSong%d", i), "title": "曲目", "interval": 3})
			}
			page := 2
			if scenario == "repeated-page" {
				page = 1
			}
			encoded, err := json.Marshal(map[string]any{"code": 0, "req_0": map[string]any{"code": 0, "data": map[string]any{"code": 0, "meta": map[string]any{"curpage": page, "nextpage": 3}, "body": map[string]any{"item_song": rows}}}})
			if err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
			})
			p := loadAggregate(t)
			if err := p.SetSetting("catalogPlatform", "tx"); err != nil {
				t.Fatal(err)
			}
			got, err := p.Search("测试", music.SearchTracks, 20)
			if !errors.Is(err, music.ErrUnavailable) || got != nil {
				t.Fatal("QQ 搜索的空页、截断或重复页被继续展示")
			}
		})
	}
}

// QQ 原始歌单必须完整；统计缺失、截断、偏移或重复身份不能被回填成成功数量。
func TestAggregateQQCatalogRejectsIncompletePlaylist(t *testing.T) {
	for _, scenario := range []string{"truncated", "missing-total", "more", "offset", "duplicate", "wrong-id"} {
		t.Run(scenario, func(t *testing.T) {
			row := map[string]any{"disstid": "900", "dissname": "公开歌单", "total_song_num": 2, "songnum": 2, "cur_song_num": 2, "song_begin": 0,
				"songlist": []any{map[string]any{"mid": "FixtureSongA1", "title": "第一首", "interval": 3}, map[string]any{"mid": "FixtureSongB2", "title": "第二首", "interval": 4}}}
			switch scenario {
			case "truncated":
				row["total_song_num"] = 3
			case "missing-total":
				delete(row, "total_song_num")
			case "more":
				row["hasmore"] = true
			case "offset":
				row["song_begin"] = 1
			case "duplicate":
				row["songlist"].([]any)[1] = row["songlist"].([]any)[0]
			case "wrong-id":
				row["disstid"] = "901"
			}
			encoded, err := json.Marshal(map[string]any{"code": 0, "subcode": 0, "cdlist": []any{row}})
			if err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
			})
			p := loadAggregate(t)
			got, err := p.Playlist("catalog:tx:playlist:900")
			if !errors.Is(err, music.ErrUnavailable) || got != nil {
				t.Fatal("不完整的 QQ 歌单被当作完整结果")
			}
		})
	}
}

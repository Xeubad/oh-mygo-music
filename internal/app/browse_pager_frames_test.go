package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// pagerFramePage is a page of a source's own, whose modes show one row and
// whose discoveries page, as the qishui plugin's does.
func pagerFramePage(a *app) *music.Browse {
	names := []string{"粤语慵懒夜晚热爱", "水手精神逆风翱翔", "情歌教父黄昏暴击", "旧日金曲慢慢听", "深夜独处城市霓虹", "清晨通勤清醒提神", "雨天窗边安静阅读", "公路旅行一路向北"}
	mode := func(i int) music.Station {
		url := fmt.Sprintf("https://example.invalid/mode-%d.png", i)
		giveCover(a, url, coverImage(ui.Oklch(0.55, 0.12, float32(i)*40), ui.Oklch(0.32, 0.08, float32(i)*40+60)))
		return music.Station{ID: fmt.Sprintf("radio:%d", i), Name: names[i], ImageURL: url, Description: "点击模式持续播放"}
	}
	var modes []music.Station
	for i := range names {
		modes = append(modes, mode(i))
	}
	discover := []struct{ name, desc string }{
		{"胡彦斌", "红颜佚骨 柔情中国风"}, {"LISA", "动画主题曲 魅力摇滚"}, {"TK from 凛として時雨", "暗黑摇滚 诠释另类摇滚"},
		{"Sakabe Go", "根据你的喜好推荐歌手"}, {"陈奕迅", "粤语经典 声线醇厚"}, {"薛之谦", "情歌叙事 都市心事"},
		{"告五人", "民谣摇滚 温暖治愈"}, {"梁博", "独立摇滚 真挚表达"}, {"毛不易", "生活叙事 平淡动人"}, {"张惠妹", "华语天后 爆发力强"},
	}
	var cards []music.Station
	for i, d := range discover {
		url := fmt.Sprintf("https://example.invalid/card-%d.png", i)
		giveCover(a, url, coverImage(ui.Oklch(0.6, 0.1, float32(i)*33+15), ui.Oklch(0.28, 0.06, float32(i)*33+200)))
		cards = append(cards, music.Station{ID: fmt.Sprintf("radio:9%d", i), Name: d.name, ImageURL: url, Description: d.desc})
	}
	lurl := "https://example.invalid/list-1.png"
	giveCover(a, lurl, coverImage(ui.Oklch(0.58, 0.1, 300), ui.Oklch(0.3, 0.08, 30)))
	return &music.Browse{
		Title:    "听歌模式",
		Subtitle: "选择常用模式，或用图片发现更多音乐",
		Sections: []music.Section{
			{Title: "常用模式", OneRow: true, MorePage: "all-modes", Stations: modes},
			{Title: "探索更多新模式", Pager: true, NextPage: "listening-modes:more:1",
				Stations: cards, Playlists: []music.Playlist{{ID: "72", Name: "深夜写作", ImageURL: lurl, TrackCount: 40}}},
		},
	}
}

// With MM_FRAMES set, saves a page whose cards page, and the page it turns to,
// in the light and the dark.
func TestBrowsePagerFrames(t *testing.T) {
	dir := framesDir(t)
	for _, dark := range []bool{false, true} {
		a := newTestApp()
		without(a, "isLiked")
		page := pagerFramePage(a)
		a.library.browses = map[string]*browseData{"listening-modes": {page: page}}
		a.route = route{page: pageBrowse, src: "fake", id: "listening-modes"}
		tt := ui.NewTester(a.view, 1200, 800)
		tt.SetDark(dark)
		settled(tt)
		theme := "light"
		if dark {
			theme = "dark"
		}
		saveFrame(t, dir, "pager-"+theme+".png", tt.Image())
		if tt.FramePending() {
			t.Error("the page asks for frames while idle")
		}
		if err := tt.Click("Next page"); err != nil {
			t.Fatal(err)
		}
		settled(tt)
		saveFrame(t, dir, "pager-turned-"+theme+".png", tt.Image())
	}
}

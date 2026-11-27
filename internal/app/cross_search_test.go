package app

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// crossSource 是一个来源在跨源搜索里收到的提问。
type crossSource struct {
	source string
	searchRequest
}

// 一个来源失败只影响它自己：别的组保留结果，重试也只重发它。
func TestCrossSearchFailureStaysInItsGroup(t *testing.T) {
	a := newTestApp()
	a.settings.SearchAll = true
	asked := make(chan crossSource, 8)
	fake(a).search = func(q string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
		asked <- crossSource{source: "fake", searchRequest: searchRequest{q, kind, offset}}
		return &music.SearchResult{Tracks: []music.Track{{ID: "fake-1", Name: "Local Song"}}}, nil
	}
	crossSearchOf(t, a, "Alpha", "Alpha Song", asked)

	// Beta 第一次报错，重试后才有结果。
	beta := newFakeSource("Beta")
	attempts := 0
	beta.search = func(q string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
		asked <- crossSource{source: "Beta", searchRequest: searchRequest{q, kind, offset}}
		attempts++
		if attempts == 1 {
			return nil, errors.New("offline")
		}
		return &music.SearchResult{Tracks: []music.Track{{ID: "beta-1", Name: "Beta Song"}}}, nil
	}
	addFake(a, beta)

	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	a.search("MyGO")
	tt.Frame()
	until(t, ch, func() bool { return a.cross != nil && a.cross.settled() })
	tt.Frame()

	if !tt.HasText("Alpha Song") || !tt.HasText("Local Song") {
		t.Error("a failed source took the other groups with it")
	}
	if !tt.HasText("The search failed: offline") {
		t.Error("the failed group does not say why")
	}
	if _, ok := tt.Find("Retry"); !ok {
		t.Fatal("no Retry in the failed group")
	}
	seen := map[string]int{}
	for len(seen) < 3 {
		got := wait(t, asked, "the first round")
		seen[got.source]++
	}
	for _, id := range []string{"fake", "Alpha", "Beta"} {
		if seen[id] != 1 {
			t.Fatalf("the first round asked %s %d times: %v", id, seen[id], seen)
		}
	}
	select {
	case extra := <-asked:
		t.Fatalf("the first round asked %s once more", extra.source)
	default:
	}

	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	until(t, ch, func() bool { return a.cross.settled() })
	tt.Frame()

	got := wait(t, asked, "the retry")
	if got.source != "Beta" || got.offset != 0 {
		t.Errorf("retry asked %+v", got)
	}
	select {
	case other := <-asked:
		t.Errorf("retry also asked %s", other.source)
	default:
	}
	if !tt.HasText("Beta Song") {
		t.Error("the retried group does not show what it found")
	}
	if !tt.HasText("Alpha Song") {
		t.Error("the retry cleared another group")
	}
}

// 组内加载更多只让这个来源按自己的 offset 续页，其它组不动。
func TestCrossSearchLoadsMoreInOneGroup(t *testing.T) {
	a := newTestApp()
	a.settings.SearchAll = true
	asked := make(chan crossSource, 16)
	fake(a).search = func(q string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
		asked <- crossSource{source: "fake", searchRequest: searchRequest{q, kind, offset}}
		if offset == 0 {
			return &music.SearchResult{
				Tracks: []music.Track{{ID: "l1", Name: "Local One"}, {ID: "l2", Name: "Local Two"}},
				More:   true,
			}, nil
		}
		return &music.SearchResult{Tracks: []music.Track{{ID: "l3", Name: "Local Three"}}}, nil
	}
	crossSearchOf(t, a, "Alpha", "Alpha Song", asked)

	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	a.search("MyGO")
	tt.Frame()
	until(t, ch, func() bool { return a.cross != nil && a.cross.settled() })
	tt.Frame()

	seen := map[string]int{}
	for len(seen) < 2 {
		got := wait(t, asked, "the first round")
		seen[got.source]++
	}
	if _, ok := tt.Find("Load More"); !ok {
		t.Fatal("no Load More in the group that has a next page")
	}

	if err := tt.Click("Load More"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	until(t, ch, func() bool { return a.cross.settled() })
	tt.Frame()

	got := wait(t, asked, "the next page")
	if got.source != "fake" || got.offset != 2 {
		t.Errorf("load more asked %+v", got)
	}
	select {
	case other := <-asked:
		t.Errorf("load more also asked %s", other.source)
	default:
	}
	if !tt.HasText("Local Three") {
		t.Error("the next page is not shown")
	}
	if !tt.HasText("Alpha Song") {
		t.Error("load more changed another group")
	}
}

// 点某组的歌，队列保留它的来源，取流走原音源，当前来源不变。
func TestCrossSearchPlaysFromItsOwnSource(t *testing.T) {
	a := newTestApp()
	a.settings.SearchAll = true
	fake(a).search = func(string, music.SearchKind, int) (*music.SearchResult, error) {
		return &music.SearchResult{Tracks: []music.Track{{ID: "l-1", Name: "Local Song"}}}, nil
	}

	alpha := newFakeSource("Alpha")
	alpha.search = func(string, music.SearchKind, int) (*music.SearchResult, error) {
		return &music.SearchResult{Tracks: []music.Track{{ID: "a-1", Name: "Alpha Song"}}}, nil
	}
	streamed := make(chan string, 4)
	alpha.stream = func(id, quality string) (*music.Stream, error) {
		streamed <- id
		return nil, music.ErrUnavailable
	}
	addFake(a, alpha)

	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	a.search("MyGO")
	tt.Frame()
	until(t, ch, func() bool { return a.cross != nil && a.cross.settled() })
	tt.Frame()

	if err := tt.Click("Alpha Song"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	q := a.pl.Queue()
	if len(q) != 1 || q[0].ID != "a-1" || q[0].Source != "Alpha" {
		t.Fatalf("queued %+v", q)
	}
	if got := wait(t, streamed, "the stream"); got != "a-1" {
		t.Errorf("asked source Alpha for %q", got)
	}
	if a.library.id != "fake" || a.route.src != "fake" {
		t.Errorf("switched to %s (route %s)", a.library.id, a.route.src)
	}
}

// 来源 chip 只筛当前显示，不再向任何来源发请求。
func TestCrossSearchFiltersBySourceChip(t *testing.T) {
	a := newTestApp()
	a.settings.SearchAll = true
	asked := make(chan crossSource, 16)
	fake(a).search = func(q string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
		asked <- crossSource{source: "fake", searchRequest: searchRequest{q, kind, offset}}
		return &music.SearchResult{Tracks: []music.Track{{ID: "l1", Name: "Local Song"}}}, nil
	}
	crossSearchOf(t, a, "Alpha", "Alpha Song", asked)
	crossSearchOf(t, a, "Beta", "Beta Song", asked)

	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	a.search("MyGO")
	tt.Frame()
	until(t, ch, func() bool { return a.cross != nil && a.cross.settled() })
	tt.Frame()
	seen := map[string]int{}
	for len(seen) < 3 {
		got := wait(t, asked, "the first round")
		seen[got.source]++
	}

	if err := tt.Click("Alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Alpha Song") {
		t.Error("the chosen source is not shown")
	}
	if tt.HasText("Local Song") || tt.HasText("Beta Song") {
		t.Error("another source is still shown")
	}
	select {
	case extra := <-asked:
		t.Errorf("filtering asked %s", extra.source)
	default:
	}

	if err := tt.Click("All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Local Song") || !tt.HasText("Beta Song") {
		t.Error("choosing All does not bring the other sources back")
	}
}

// crossSearchOf 让一个来源回答一首歌，并记下它被问了什么。
func crossSearchOf(t *testing.T, a *app, id, songName string, asked chan<- crossSource) {
	t.Helper()
	src := newFakeSource(id)
	src.info.Name = id
	src.search = func(q string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
		asked <- crossSource{source: id, searchRequest: searchRequest{q, kind, offset}}
		return &music.SearchResult{Tracks: []music.Track{{ID: id + "-1", Name: songName}}}, nil
	}
	addFake(a, src)
}

// “全部来源”让每个能搜的来源各答一次，结果按来源分组显示。
func TestCrossSearchGroupsBySource(t *testing.T) {
	a := newTestApp()
	asked := make(chan crossSource, 8)
	fake(a).search = func(q string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
		asked <- crossSource{source: "fake", searchRequest: searchRequest{q, kind, offset}}
		return &music.SearchResult{Tracks: []music.Track{{ID: "fake-1", Name: "Local Song"}}}, nil
	}
	crossSearchOf(t, a, "Alpha", "Alpha Song", asked)
	crossSearchOf(t, a, "Beta", "Beta Song", asked)

	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Search"); err != nil {
		t.Fatal(err)
	}
	tt.Type("MyGO")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	// 切换前那一次只问当前来源，先把它取走，免得混进下面的三组里。
	if got := wait(t, asked, "the active source's search"); got.source != "fake" || got.offset != 0 {
		t.Fatalf("before switching, %+v", got)
	}
	if err := tt.Click("All Sources"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	until(t, ch, func() bool { return a.cross != nil && a.cross.settled() })
	tt.Frame()

	seen := map[string]int{}
	for range 3 {
		got := wait(t, asked, "a source's search")
		if got.query != "MyGO" || got.kind != music.SearchTracks || got.offset != 0 {
			t.Errorf("%s searched %+v", got.source, got.searchRequest)
		}
		seen[got.source]++
	}
	for _, id := range []string{"fake", "Alpha", "Beta"} {
		if seen[id] != 1 {
			t.Errorf("%s searched %d times, want 1", id, seen[id])
		}
	}
	for _, text := range []string{"Local Song", "Alpha Song", "Beta Song"} {
		if !tt.HasText(text) {
			t.Errorf("the page does not show %q", text)
		}
	}
}

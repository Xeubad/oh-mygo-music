package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// The menu behind a song's dots in Now Playing copies its title, with its
// artists, and the link its source gave it.
func TestSongMenuCopies(t *testing.T) {
	var copied string
	real := copyText
	copyText = func(s string) error { copied = s; return nil }
	defer func() { copyText = real }()

	a := newTestApp()
	tr := song("1859", "宝船", "DOUDOU", "小明")
	tr.URL = "https://example.com/song/1859"
	a.pl.Cue([]music.Track{tr})
	a.pl.SetLyrics(nil)
	a.nowPlaying = true
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Advance(time.Second)
	tt.Frame()

	for _, c := range []struct{ item, want string }{
		{"Copy Title", "宝船"},
		{"Copy Title and Artists", "宝船 - DOUDOU, 小明"},
		{"Copy Link", "https://example.com/song/1859"},
	} {
		if err := tt.Click("More"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(c.item); err != nil {
			t.Fatalf("%v; the menu shows %q", err, tt.Menu())
		}
		tt.Frame()
		if copied != c.want {
			t.Errorf("%s copied %q, want %q", c.item, copied, c.want)
		}
	}
}

// 已登录渠道且配好分享服务时，歌曲菜单不再直接复制原曲链接，只经分享卡片
// 发布在线听歌链接；播客仍保留自己的链接。
func TestSongMenuSharesThroughConfiguredServer(t *testing.T) {
	var copied string
	real := copyText
	copyText = func(s string) error { copied = s; return nil }
	defer func() { copyText = real }()

	a := newTestApp()
	a.settings.ShareServerURL = "https://share.example.com"
	// library.account 与 app.account 是同一个已登录的来源。
	a.library.account = &music.Account{ID: "1", Name: "小明"}
	a.account = a.library.account
	signedIn := song("1", "宝船", "DOUDOU")
	signedIn.URL = "https://example.com/song/1"
	episode := song("2", "第 1 集", "主播")
	episode.ShowID = "show"
	episode.URL = "https://example.com/episode/2"
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "飙升榜", Tracks: []music.Track{signedIn, episode}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.RightClick("宝船"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Link"); err == nil {
		t.Fatalf("a signed-in song offered its source link; the menu shows %q", tt.Menu())
	}
	tt.CloseMenu()
	tt.Frame()
	if copied != "" {
		t.Fatalf("copied %q without the share card", copied)
	}

	if err := tt.RightClick("第 1 集"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Link"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	tt.Frame()
	if copied != episode.URL {
		t.Fatalf("podcast copied %q, want %q", copied, episode.URL)
	}

	if err := tt.RightClick("宝船"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Share Card…"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	tt.Frame()
	if a.sharing == nil || !a.sharing.open || !a.sharing.cardOnline {
		t.Fatalf("share card did not open on the share service: %+v", a.sharing)
	}
	if a.sharing.track.URL != signedIn.URL {
		t.Fatalf("share card lost the song's page: %q", a.sharing.track.URL)
	}
}

// A right click on a song's row opens its menu, without playing it, and a song
// with no page at its source offers no link to copy.
func TestSongMenuOnRightClick(t *testing.T) {
	var copied string
	real := copyText
	copyText = func(s string) error { copied = s; return nil }
	defer func() { copyText = real }()

	a := newTestApp()
	linked := song("2", "宝船", "DOUDOU")
	linked.URL = "https://example.com/song/2"
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "飙升榜", Tracks: []music.Track{song("1", "一张相片", "林宥嘉"), linked}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.RightClick("一张相片"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Link"); err == nil {
		t.Error("offered the link of a song with none")
	}
	tt.CloseMenu()

	if err := tt.RightClick("宝船"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Link"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	tt.Frame()
	if copied != linked.URL {
		t.Errorf("copied %q, want %q", copied, linked.URL)
	}
	if a.pl.Current() != nil {
		t.Error("the right click played the song")
	}
}

// 右键下一首播放只插入队列，不能替换原列表或打断当前播放、暂停与进度。
func TestSongMenuQueuesNextWithoutInterruptingPlayback(t *testing.T) {
	for _, playing := range []bool{false, true} {
		t.Run(map[bool]string{false: "paused", true: "playing"}[playing], func(t *testing.T) {
			a := newTestApp()
			current := song("current", "Current song", "Artist")
			current.Source = "fake"
			later := song("later", "Later song", "Artist")
			later.Source = "fake"
			picked := song("picked", "Picked song", "Artist")
			a.pl.Cue([]music.Track{current, later})
			if playing {
				a.pl.Hold(42 * time.Second)
			}
			position := a.pl.Position()
			a.playlists["42"] = &music.Playlist{ID: "42", Name: "Choices", Tracks: []music.Track{picked}}
			a.route = route{page: pagePlaylist, src: "fake", id: "42"}
			tester := ui.NewTester(a.view, 1200, 800)
			tester.Frame()
			if err := tester.RightClick("Picked song"); err != nil {
				t.Fatal(err)
			}
			if err := tester.ChooseMenuItem("Play Next"); err != nil {
				t.Fatal(err)
			}
			tester.Frame()
			if a.pl.Current().ID != "current" || a.pl.Playing() != playing || a.pl.Position() != position {
				t.Fatalf("insertion changed current playback: %+v, playing %v, position %s", a.pl.Current(), a.pl.Playing(), a.pl.Position())
			}
			order, at := a.pl.PlayOrder()
			queue := a.pl.Queue()
			if len(order) != 3 || at != 0 || queue[order[1]].ID != "picked" || queue[order[1]].Source != "fake" || queue[order[2]].ID != "later" {
				t.Fatalf("wrong next track or lost source: queue %+v, order %v at %d", queue, order, at)
			}
			if a.playlists["42"].Tracks[0].Source != "" {
				t.Fatal("queue insertion mutated the source list")
			}
		})
	}
}

// 来源明确标记不可播放的曲目不能通过右键下一首播放绕过限制或改变当前队列。
func TestSongMenuCannotQueueUnavailableTrack(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{{ID: "current", Source: "fake", Name: "Current song"}})
	picked := song("missing", "Unavailable song", "Artist")
	picked.Unavailable = true
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Choices", Tracks: []music.Track{picked}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tester := ui.NewTester(a.view, 1200, 800)
	tester.Frame()
	if err := tester.RightClick("Unavailable song"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Play Next"); err == nil {
		t.Fatal("unavailable song could be queued")
	}
	tester.CloseMenu()
	if len(a.pl.Queue()) != 1 || a.pl.Current().ID != "current" {
		t.Fatal("disabled action changed the queue")
	}
}

// 聚合曲目的下一首播放遵守显式来源选择，失效选择不能回退到另一录音或污染旧队列。
func TestSongMenuQueuesNextWithSelectedAggregateSource(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected", true: "stale"}[stale], func(t *testing.T) {
			a := newTestApp()
			left := music.AggregateSource{Platform: "wy", AccountID: "one", TrackID: "left"}
			right := music.AggregateSource{Platform: "tx", AccountID: "two", TrackID: "right"}
			frozen, err := json.Marshal(map[string]any{"version": 1, "identity": left, "sources": []music.AggregateSource{left, right}})
			if err != nil {
				t.Fatal(err)
			}
			picked := music.Track{ID: "aggregate:" + string(frozen), Source: "fake", Name: "Selected recording", AggregateSources: []music.AggregateSource{left, right}}
			a.aggregateViews = map[*library]*aggregateView{
				a.library: {sourceChoices: map[string]string{aggregateTrackIdentity(&picked): aggregateSourceKey(right)}},
			}
			if stale {
				picked.AggregateSources = []music.AggregateSource{left}
			}
			a.pl.Cue([]music.Track{{ID: "current", Source: "fake", Name: "Current song"}})
			a.playlists["42"] = &music.Playlist{ID: "42", Name: "Choices", Tracks: []music.Track{picked}}
			a.route = route{page: pagePlaylist, src: "fake", id: "42"}
			tester := ui.NewTester(a.view, 1200, 800)
			tester.Frame()
			if err := tester.RightClick("Selected recording"); err != nil {
				t.Fatal(err)
			}
			if err := tester.ChooseMenuItem("Play Next"); err != nil {
				t.Fatal(err)
			}
			if a.pl.Current().ID != "current" {
				t.Fatal("queueing changed the current track")
			}
			if stale {
				if len(a.pl.Queue()) != 1 {
					t.Fatal("stale source choice was silently queued")
				}
				return
			}
			order, at := a.pl.PlayOrder()
			var queued struct{ Identity music.AggregateSource }
			if err := json.Unmarshal([]byte(strings.TrimPrefix(a.pl.Queue()[order[at+1]].ID, "aggregate:")), &queued); err != nil {
				t.Fatal(err)
			}
			if !sameAggregateSource(queued.Identity, right) || a.playlists["42"].Tracks[0].ID != picked.ID {
				t.Fatal("next track ignored the selected source or mutated its list")
			}
		})
	}
}

// A song's menu goes to its artist's and album's pages, as far as its source
// has them.
func TestSongMenuGoesToPages(t *testing.T) {
	a := newTestApp()
	tr := music.Track{ID: "1", Name: "宝船", Artists: []music.Artist{{ID: "12", Name: "DOUDOU"}}, Album: music.Album{ID: "8", Name: "迷跡波"}, Duration: 200000}
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "飙升榜", Tracks: []music.Track{tr}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.RightClick("宝船"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Go to Artist: DOUDOU"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	if a.route != (route{page: pageArtist, src: "fake", id: "12"}) {
		t.Fatalf("went to %+v", a.route)
	}
	tt.Key(0, ui.KeyBack)
	tt.Frame()

	without(a, "album")
	tt.Frame()
	if err := tt.RightClick("宝船"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Go to Album: 迷跡波"); err == nil {
		t.Error("offered the page of an album its source has no pages of")
	}
	tt.CloseMenu()
}

// Downloading a song from its menu saves what its source streams where the
// listener picks, suggesting a name of its credits that can't lead out of the
// folder: "AC/DC" made a folder of "AC" instead.
func TestSongMenuDownloads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("the song"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	realPick := pickDownloadFolder
	pickDownloadFolder = func(a *app) string { return dir }
	defer func() { pickDownloadFolder = realPick }()

	a := newTestApp()
	ch := posts(a)
	fake(a).stream = func(id, quality string) (*music.Stream, error) {
		return &music.Stream{URL: srv.URL, Type: "mp3"}, nil
	}
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "飙升榜", Tracks: []music.Track{song("1", "Back in Black", "AC/DC")}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.RightClick("Back in Black"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Download…"); err != nil {
		t.Fatalf("%v; the menu shows %q", err, tt.Menu())
	}
	tt.Frame()
	if !a.downloadPrompt.open || a.downloadPrompt.fileName != "AC_DC - Back in Black" {
		t.Fatalf("unexpected download prompt state: open=%v fileName=%q", a.downloadPrompt.open, a.downloadPrompt.fileName)
	}
	clickDownloadUI(t, tt, "Browse…")
	until(t, ch, func() bool { return sameDownloadPath(a.downloadPrompt.folder, dir) })
	a.downloadPrompt.fileName = "picked"
	clickDownloadUI(t, tt, "Download")
	until(t, ch, func() bool { return strings.HasPrefix(a.toast, "Downloaded") })
	if b, _ := os.ReadFile(filepath.Join(dir, "picked.mp3")); string(b) != "the song" {
		t.Errorf("saved %q", b)
	}
}

// A song that plays elsewhere, in its source's own app, offers no download.
func TestSongMenuDownloadsOnlyWhatStreams(t *testing.T) {
	a := newTestApp()
	fake(a).info.Playback = music.PlaybackRemote
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "飙升榜", Tracks: []music.Track{song("1", "宝船", "DOUDOU")}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.RightClick("宝船"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Download…"); err == nil {
		t.Error("offered to download a song that plays elsewhere")
	}
	tt.CloseMenu()
}

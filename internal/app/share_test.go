package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 预选按当前位置、长间奏与尾奏回退到有效歌词，时间轴缺失时取首句。
func TestInitialShareLineFollowsPlaybackPosition(t *testing.T) {
	lines := []lyrics.Line{
		{At: time.Second, End: 3 * time.Second, Text: "", Words: nil},
		{At: 2 * time.Second, End: 5 * time.Second, Text: "Composer: Artist"},
		{At: 4 * time.Second, End: 7 * time.Second, Text: "First"},
		{At: 10 * time.Second, End: 13 * time.Second, Text: "Second"},
	}
	for _, test := range []struct {
		name     string
		position time.Duration
		current  bool
		want     int
	}{{"other song", 20 * time.Second, false, 2}, {"current line", 4500 * time.Millisecond, true, 2}, {"interlude", 8 * time.Second, true, 3}, {"outro", 20 * time.Second, true, 3}} {
		t.Run(test.name, func(t *testing.T) {
			if got := initialShareLine(lines, test.position, test.current); got != test.want {
				t.Fatalf("initial line %d, want %d", got, test.want)
			}
		})
	}
	if got := initialShareLine([]lyrics.Line{{Text: "Ambiguous"}, {Text: "Next"}}, 9*time.Second, true); got != 0 {
		t.Fatalf("untimed lyrics start at %d, want first line", got)
	}
}

// 拖动试听边界更新待发布区间并让旧链接失效，拖动播放头只改变试听位置。
func TestShareClipTimelineDrag(t *testing.T) {
	a := newTestApp()
	draft := &shareDraft{
		open: true, track: song("shared", "Shared"), onlineMode: "clip", timingReliable: true,
		selected: map[int]bool{0: true},
		lines:    []lyrics.Line{{Text: "First", At: 40 * time.Second, End: 80 * time.Second, AtSource: true, EndSource: true}},
	}
	draft.onlineURL = "https://example.com/s/old"
	draft.onlinePublishedRevision = draft.onlineRevision
	a.sharing = draft
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.shareDialog(c)
	}, 880, 580)
	tester.Frame()
	if dir := os.Getenv("MM_FRAMES"); dir != "" {
		saveFrame(t, dir, "share-clip-timeline.png", tester.Image())
	}
	bounds, ok := tester.Find("Clip Range:")
	if !ok {
		t.Fatal("missing clip timeline")
	}
	top := bounds.Y + bounds.H/2
	tester.Press(bounds.X+bounds.W*.20, top)
	tester.Move(bounds.X+bounds.W*.10, top)
	tester.Release(bounds.X+bounds.W*.10, top)
	if draft.clipStart < 15*time.Second || draft.clipStart > 25*time.Second || draft.clipEnd != 81*time.Second {
		t.Fatalf("dragged start = %v, end = %v", draft.clipStart, draft.clipEnd)
	}
	tester.Press(bounds.X+bounds.W*.40, top)
	tester.Move(bounds.X+bounds.W*.65, top)
	tester.Release(bounds.X+bounds.W*.65, top)
	if draft.clipEnd < 125*time.Second || draft.clipEnd > 135*time.Second {
		t.Fatalf("dragged end = %v", draft.clipEnd)
	}
	if a.onlineLinkMatchesDraft(draft) {
		t.Fatal("previous link still matches changed clip bounds")
	}
	revision := draft.onlineRevision
	if err := tester.Click("Preview Playhead"); err != nil {
		t.Fatal(err)
	}
	if draft.clipHead <= draft.clipStart || draft.clipHead >= draft.clipEnd {
		t.Fatalf("playhead = %v, outside (%v, %v)", draft.clipHead, draft.clipStart, draft.clipEnd)
	}
	if draft.onlineRevision != revision {
		t.Fatal("moving the playhead invalidated the current share")
	}
	metadata, err := a.onlineShareMetadata(draft)
	if err != nil || metadata.ClipStartMs != draft.clipStart.Milliseconds() || metadata.ClipEndMs != draft.clipEnd.Milliseconds() {
		t.Fatalf("published range %d–%d differs from preview %v–%v: %v", metadata.ClipStartMs, metadata.ClipEndMs, draft.clipStart, draft.clipEnd, err)
	}
}

// 配好分享服务后，带来源页面的歌曲仍走在线链接；未配置时才链接回来源页面。
func TestShareLinkTargetFollowsShareServer(t *testing.T) {
	for _, test := range []struct {
		name   string
		server string
		online bool
	}{
		{"configured", "https://share.example.com", true},
		{"not configured", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newTestApp()
			a.settings.ShareServerURL = test.server
			track := song("shared", "Shared")
			track.URL = "https://music.example.com/song/1"
			a.openShare(track)
			draft := a.sharing
			if draft.cardOnline != test.online {
				t.Fatalf("card online = %v, want %v", draft.cardOnline, test.online)
			}
			// 在线链接发布前不能拿来源页面顶上，预览和导出必须保持同一含义。
			want := ""
			if !test.online {
				want = track.URL
			}
			if got := a.shareCardURL(draft); got != want {
				t.Fatalf("card URL %q, want %q", got, want)
			}
		})
	}
}

// 配好分享服务后，卡片链接不能再切回歌曲的来源页面。
func TestShareConfiguredServerCannotLinkToSourcePage(t *testing.T) {
	a := newTestApp()
	a.settings.ShareServerURL = "https://share.example.com"
	posted := posts(a)
	fake(a).lyrics = func(string) (*music.Lyrics, error) {
		return &music.Lyrics{LRC: "[00:01]First"}, nil
	}
	track := song("shared", "Shared")
	track.URL = "https://music.example.com/song/1"
	a.openShare(track)
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.shareDialog(c)
	}, 1200, 800)
	until(t, posted, func() bool { tester.Frame(); _, ok := tester.Find("Card Link"); return ok })
	if err := tester.Click("Card Link"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Original Song Page"); err == nil {
		t.Fatal("a configured share service must keep the card on its online link")
	}
	tester.Frame()
	if !a.sharing.cardOnline {
		t.Fatal("the card switched away from the share service")
	}
}

// 列表分享另一首歌只操作分享草稿，所选文字不借用正在播放的歌词。
func TestShareMenuKeepsPlaybackAndCopiesSelection(t *testing.T) {
	var copied string
	previous := copyText
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = previous }()
	a := newTestApp()
	posted := posts(a)
	fake(a).lyrics = func(id string) (*music.Lyrics, error) {
		if id != "shared" {
			t.Errorf("lyrics requested for %q", id)
		}
		return &music.Lyrics{LRC: "[00:01]First\n[00:05]Second\n[00:09]Third", Translation: "[00:01]One\n[00:09]Three"}, nil
	}
	playing, shared := song("playing", "Playing"), song("shared", "Shared")
	a.pl.Cue([]music.Track{playing})
	a.pl.Hold(7 * time.Second)
	wasPlaying := a.pl.Playing()
	a.playlists["list"] = &music.Playlist{ID: "list", Tracks: []music.Track{shared}}
	a.route = route{page: pagePlaylist, src: "fake", id: "list"}
	tester := ui.NewTester(a.view, 1200, 800)
	tester.Frame()
	if err := tester.RightClick("Shared"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Share Card…"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return tester.HasText("Third") })
	if err := tester.Click("Select lyric 3"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Copy Lyrics"); err != nil {
		t.Fatal(err)
	}
	if copied != "First\nOne\n…\nThird\nThree" {
		t.Fatalf("copied %q", copied)
	}
	if a.pl.Current().ID != "playing" || a.pl.Position() != 7*time.Second || a.pl.Playing() != wasPlaying {
		t.Fatal("sharing changed playback")
	}
}

// 分享列表里有译文时，即使只选了无译文的行，也应能关闭整张卡片的翻译。
func TestShareTranslationTogglesWithoutSelectingTranslatedLyrics(t *testing.T) {
	var copied string
	previous := copyText
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = previous }()
	a := newTestApp()
	posted := posts(a)
	fake(a).lyrics = func(string) (*music.Lyrics, error) {
		return &music.Lyrics{LRC: "[00:01]First\n[00:05]Second", Translation: "[00:05]Translated second"}, nil
	}
	a.openShare(song("shared", "Shared"))
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.shareDialog(c)
	}, 1200, 800)
	until(t, posted, func() bool { tester.Frame(); return tester.HasText("Translated second") })
	if !a.sharing.selected[0] || a.sharing.selected[1] {
		t.Fatal("the translated lyric must not be selected initially")
	}
	if err := tester.Click("Show Translation"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !a.settings.HideCardTranslation || tester.HasText("Translated second") {
		t.Fatal("could not hide an unselected lyric's translation")
	}
	if err := tester.Click("Lyrics Display"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Show Translation"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if a.settings.HideCardTranslation || !tester.HasText("Translated second") {
		t.Fatal("the display menu did not turn card translation back on")
	}
	if err := tester.Click("Select lyric 2"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Show Translation"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Copy Lyrics"); err != nil {
		t.Fatal(err)
	}
	if copied != "First\nSecond" || a.settings.HidePlaybackTranslation {
		t.Fatalf("the card toggle affected playback choices or copied unexpected lyrics: %q", copied)
	}
}

// 取消和保存失败不改变目录，成功保存后才记住新目录并保留草稿。
func TestShareSaveRemembersOnlySuccessfulDirectory(t *testing.T) {
	a := newTestApp()
	posted := posts(a)
	a.settings.CardDirectory = "previous-directory"
	fake(a).lyrics = func(string) (*music.Lyrics, error) { return &music.Lyrics{}, nil }
	track := music.Track{ID: "empty", Source: "fake", Name: "An instrumental"}
	destination := ""
	previous := saveCardAs
	saveCardAs = func(*app, string, string) (string, error) { return destination, nil }
	defer func() { saveCardAs = previous }()
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.songMenu(c, &track, 22, a.pal.text)
		a.shareDialog(c)
	}, 1200, 800)
	tester.Frame()
	if err := tester.Click("More"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Share Card…"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); _, ok := tester.Find("Card Preview"); return ok })
	directory := t.TempDir()
	for _, scenario := range []struct{ path, status, directory string }{
		{"", "Save canceled", "previous-directory"},
		{filepath.Join(directory, "missing", "card.png"), "Could not save image", "previous-directory"},
		{filepath.Join(directory, "card.png"), "Image saved", directory},
	} {
		destination = scenario.path
		if err := tester.Click("Save PNG"); err != nil {
			t.Fatal(err)
		}
		until(t, posted, func() bool { tester.Frame(); return tester.HasText(scenario.status) })
		if a.settings.CardDirectory != scenario.directory {
			t.Fatalf("directory %q, want %q", a.settings.CardDirectory, scenario.directory)
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, "card.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, ok := tester.Find("Card Preview"); !ok {
		t.Fatal("saving discarded the card")
	}
}

// 图片复制失败后可重试，复制的确是当前尺寸的最终 PNG，草稿内容仍可继续使用。
func TestShareImageExportRetriesWithChosenSize(t *testing.T) {
	images := make(chan []byte, 2)
	previous := copyImage
	failed := false
	copyImage = func(data []byte) error {
		images <- data
		if !failed {
			failed = true
			return errors.New("clipboard unavailable")
		}
		return nil
	}
	defer func() { copyImage = previous }()
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	posted := posts(a)
	fake(a).lyrics = func(string) (*music.Lyrics, error) {
		return &music.Lyrics{LRC: "[00:01]First", Translation: "[00:01]One"}, nil
	}
	track := music.Track{ID: "shared", Source: "fake", Name: "Shared", URL: "https://example.com/song/1"}
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.songMenu(c, &track, 22, a.pal.text)
		a.shareDialog(c)
	}, 1200, 800)
	tester.Frame()
	if err := tester.Click("More"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Share Card…"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); _, ok := tester.Find("Card Preview"); return ok })
	if err := tester.Click("Card Size"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Square (1:1)"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); _, ok := tester.Find("Card Preview"); return ok })
	if err := tester.Click("Card Template"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Vinyl"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); _, ok := tester.Find("Card Preview"); return ok })
	if err := tester.Click("Show Translation"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); _, ok := tester.Find("Card Preview"); return ok })
	if !a.settings.HideCardTranslation {
		t.Fatal("card translation choice did not turn off")
	}
	for attempt := range 2 {
		if err := tester.Click("Copy Image"); err != nil {
			t.Fatal(err)
		}
		data := wait(t, images, "copied PNG")
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Bounds().Dx() != 1200 || decoded.Bounds().Dy() != 1200 {
			t.Fatal(decoded.Bounds())
		}
		message := "Image copied"
		if attempt == 0 {
			message = "Could not copy image"
		}
		until(t, posted, func() bool { tester.Frame(); return tester.HasText(message) })
	}
	if !tester.HasText("First") {
		t.Fatal("export discarded the selected lyrics")
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	var stored settings
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.CardTemplate != "vinyl" || !stored.CardSquare || !stored.HideCardTranslation {
		t.Fatalf("card choices did not persist: %+v", stored)
	}
}

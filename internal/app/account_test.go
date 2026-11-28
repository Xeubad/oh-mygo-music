package app

import (
	"image"
	"image/draw"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// A signed-out source keeps a separate Settings entry so the account row never
// forces a login when the listener only wants to open Settings.
func TestSignedOutAccountRowKeepsSettingsEntry(t *testing.T) {
	for _, unknown := range []bool{true, false} {
		t.Run(map[bool]string{true: "account unknown", false: "signed out"}[unknown], func(t *testing.T) {
			a := newTestApp()
			lib := a.library
			lib.account = nil
			lib.accountAsked = !unknown
			tt := ui.NewTester(a.view, 1000, 700)
			tt.Frame()
			if !unknown {
				if _, ok := tt.Find("Sign in to fake"); !ok {
					t.Fatal("login entry is missing")
				}
			}
			if _, ok := tt.Find("Settings"); !ok {
				t.Fatal("separate Settings entry is missing")
			}
		})
	}
}

// Clicking the sidebar Settings entry while signed out does not start
// login or show the QR dialog.
func TestSignedOutAccountRowSettingsDoesNotSignIn(t *testing.T) {
	a := newTestApp()
	lib := a.library
	lib.account, lib.accountAsked = nil, true
	fake(a).signIn = func() (*music.Account, error) { return nil, music.ErrUnsupported }
	main := ui.NewTester(a.view, 1000, 700)
	main.Frame()
	if err := main.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	main.Frame()
	if a.signIn.open {
		t.Fatal("Settings click opened the sign-in dialog")
	}
}

// The independent Settings button does not replace the sign-in action.
func TestSignedOutAccountRowLoginEntryStillWorks(t *testing.T) {
	a := newTestApp()
	fake(a).signIn = func() (*music.Account, error) { return nil, music.ErrUnsupported }
	lib := a.library
	lib.account, lib.accountAsked = nil, true
	main := ui.NewTester(a.view, 1000, 700)
	main.Frame()
	if _, ok := main.Find("Settings"); !ok {
		t.Fatal("Settings entry disappeared before login")
	}
	if err := main.Click("Sign in to fake"); err != nil {
		t.Fatal(err)
	}
	main.Frame()
	if !a.signIn.open {
		t.Fatal("login entry did not open the sign-in dialog")
	}
}

// Clicking the account row drops its menu, which takes the focus so the
// arrows walk its items, and Escape or a second click puts it away.
func TestAccountMenu(t *testing.T) {
	a := newTestApp()
	a.account = &music.Account{ID: "1", Name: "小明"}
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()

	if err := tt.Click("小明"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.accountOpen || !tt.HasText("Sign Out") {
		t.Fatalf("menu not open: %v", a.accountOpen)
	}
	if !tt.Focused("Settings…") {
		t.Fatal("the first item has no focus")
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if !tt.Focused("Sign Out") {
		t.Fatal("Down did not move to signing out")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.accountOpen {
		t.Fatal("Escape left the menu open")
	}

	tt.Click("小明")
	tt.Frame()
	tt.Click("小明")
	tt.Frame()
	if a.accountOpen {
		t.Fatal("a second click left the menu open")
	}
}

// My Page in the account menu opens the listener's own page at the source,
// and loads it anew each time, as what they follow changes on other pages.
func TestAccountMyPage(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	a.account = &music.Account{ID: "1", Name: "小明", Page: "user:1"}
	asked := make(chan string, 4)
	fake(a).browse = func(page string) (*music.Browse, error) {
		asked <- page
		return &music.Browse{Title: "小明", ImageURL: "https://example.com/a.jpg", Description: "3 关注 · 5 粉丝\n你好",
			Sections: []music.Section{{Title: "创建的歌单", Empty: "暂无公开歌单"}}}, nil
	}
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()

	tt.Click("小明")
	tt.Frame()
	if !tt.Focused("My Page") {
		t.Fatal("My Page isn't first in the menu")
	}
	if err := tt.Click("My Page"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if want := (route{page: pageBrowse, id: "user:1", src: "fake"}); a.route != want || a.accountOpen {
		t.Fatalf("route %+v, menu open %v", a.route, a.accountOpen)
	}
	if got := wait(t, asked, "the page"); got != "user:1" {
		t.Fatalf("asked for %q", got)
	}
	runPost(t, ch)
	tt.Frame()

	a.open(route{page: pageHome})
	tt.Frame()
	tt.Click("小明")
	tt.Frame()
	tt.Click("My Page")
	wait(t, asked, "the page again")
}

// With MM_FRAMES set, saves the foot of the sidebar with the account menu
// open, light and dark.
func TestAccountMenuFrames(t *testing.T) {
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip()
	}
	for _, dark := range []bool{false, true} {
		a := newTestApp()
		a.account = &music.Account{ID: "1", Name: "小明"}
		tt := ui.NewTester(a.view, 1000, 600)
		tt.SetDark(dark)
		tt.Frame()
		tt.Click("小明")
		tt.Advance(time.Second)
		tt.Frame()
		img := tt.Image()
		s := float32(img.Bounds().Dx()) / 1000
		crop := image.Rect(0, int(400*s), int((sidebarWidth+20)*s), img.Bounds().Dy())
		out := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		draw.Draw(out, out.Bounds(), img, crop.Min, draw.Src)
		name := "account-light.png"
		if dark {
			name = "account-dark.png"
		}
		saveFrame(t, dir, name, out)
	}
}

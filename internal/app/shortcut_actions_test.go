package app

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// Volume keys work with no queued track, move by five points, and clamp at both ends.
func TestShortcutVolumeActions(t *testing.T) {
	a := newTestApp()
	for _, item := range []struct {
		start  float64
		action shortcutAction
		want   float64
	}{{0.8, shortcutUp, 0.85}, {0.8, shortcutDown, 0.75}, {0.98, shortcutUp, 1}, {0.02, shortcutDown, 0}} {
		a.volume = item.start
		a.globalShortcutAction(item.action)
		if math.Abs(a.volume-item.want) > 1e-9 {
			t.Fatalf("%s: volume=%v, want %v", item.action, a.volume, item.want)
		}
	}
	a.quitting = true
	a.volume = 0.5
	a.globalShortcutAction(shortcutUp)
	if a.volume != 0.5 {
		t.Fatal("dispatched an action while quitting")
	}
}

// A low volume remains exact after muting, writing settings, and recreating app state.
func TestShortcutMuteSurvivesRestart(t *testing.T) {
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.volume = 0.2
	a.globalShortcutAction(shortcutMute)
	if a.volume != 0 {
		t.Fatal("did not mute")
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newTestApp()
	if err := json.Unmarshal(data, &restarted.settings); err != nil {
		t.Fatal(err)
	}
	restarted.volume, restarted.mutedFrom = restarted.settings.Volume, restarted.settings.MutedFrom
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Center().Children(func() { restarted.muteButton(c, white) })
	}, 200, 100)
	tester.Frame()
	clickShortcut(t, tester, "Mute")
	if restarted.volume != 0.2 {
		t.Fatalf("low volume restored as %v instead of 0.2", restarted.volume)
	}
	for _, invalid := range []float64{0, -1, 2, math.NaN(), math.Inf(1)} {
		restarted.volume, restarted.mutedFrom = 0, invalid
		restarted.toggleMute()
		if restarted.volume != 0.5 {
			t.Fatalf("invalid history %v restored as %v", invalid, restarted.volume)
		}
	}
}

func quietLikeApp() (*app, *music.Track) {
	a := newTestApp()
	a.account = &music.Account{ID: "listener"}
	track := song("7", "Seven")
	track.Source = "fake"
	a.pl.Cue([]music.Track{track})
	a.liked = map[string]bool{"7": false}
	return a, &track
}

// Background likes silently ignore unavailable sources, accounts and episode capabilities.
func TestShortcutLikeUnavailable(t *testing.T) {
	for _, reason := range []string{"source", "signedOut", "unknownAccount", "episode", "setup", "capability"} {
		t.Run(reason, func(t *testing.T) {
			a, track := quietLikeApp()
			fake(a).like = func(string, bool) error { t.Error("sent an unavailable like"); return nil }
			switch reason {
			case "source":
				track.Source = "gone"
			case "signedOut":
				a.account = nil
			case "unknownAccount":
				a.account, a.accountAsked = nil, false
			case "episode":
				track.ShowID = "show"
				without(a, "likeEpisode")
			case "setup":
				a.needsSetup = true
			case "capability":
				without(a, "like")
			}
			a.pl.Cue([]music.Track{*track})
			a.likeCurrentQuietly()
			if a.liked[track.ID] || a.toast != "" || a.signIn.open || a.settingsWin != nil {
				t.Fatal("unavailable background action changed state or opened UI")
			}
		})
	}
}

// Background liking shares optimistic updates and rollback, without login prompts or toasts.
func TestShortcutLikeFailureQuietly(t *testing.T) {
	a, _ := quietLikeApp()
	post := posts(a)
	fake(a).like = func(string, bool) error { return music.ErrSignIn }
	a.likeCurrentQuietly()
	if !a.liked["7"] {
		t.Fatal("did not optimistically like")
	}
	runPost(t, post)
	if a.liked["7"] || a.toast != "" || a.signIn.open || a.needsSignIn {
		t.Fatal("failure did not quietly roll back")
	}
}

// An unknown like is queried first, then toggled only if the same track and listener remain.
func TestShortcutLikeQueriesUnknownState(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same track", true: "changed track"}[changed], func(t *testing.T) {
			a, track := quietLikeApp()
			post := posts(a)
			delete(a.liked, track.ID)
			fake(a).isLiked = func([]string) (map[string]bool, error) { return map[string]bool{track.ID: true}, nil }
			calls := make(chan bool, 2)
			fake(a).like = func(_ string, on bool) error { calls <- on; return nil }
			a.likeCurrentQuietly()
			a.likeCurrentQuietly()
			if changed {
				a.pl.Cue([]music.Track{{ID: "other", Source: "fake"}})
			}
			runPost(t, post)
			if changed {
				if a.shortcutLikePending != nil || !a.liked[track.ID] {
					t.Fatal("stale query affected another track")
				}
				select {
				case <-calls:
					t.Fatal("liked after changing track")
				default:
				}
			} else {
				if wait(t, calls, "the unlike") {
					t.Fatal("unknown state was blindly liked")
				}
				runPost(t, post)
				if a.liked[track.ID] {
					t.Fatal("did not unlike")
				}
			}
		})
	}
}

// Failed status queries remain unknown and can be retried explicitly by another key press.
func TestShortcutLikeQueryRetry(t *testing.T) {
	a, track := quietLikeApp()
	post := posts(a)
	delete(a.liked, track.ID)
	fake(a).isLiked = func([]string) (map[string]bool, error) { return nil, errors.New("offline") }
	a.likeCurrentQuietly()
	runPost(t, post)
	if !a.likesFailed[track.ID] || a.shortcutLikePending != nil || a.toast != "" {
		t.Fatal("failed query became a known like or displayed feedback")
	}
	calls := make(chan bool, 2)
	fake(a).isLiked = func([]string) (map[string]bool, error) { return nil, nil }
	fake(a).like = func(_ string, on bool) error { calls <- on; return nil }
	a.likeCurrentQuietly()
	runPost(t, post)
	if !wait(t, calls, "retried like") {
		t.Fatal("did not retry the intended like")
	}
	runPost(t, post)
}

// A status lookup cannot revive an old shortcut intent after disable/re-enable or recording.
func TestShortcutLikeQueryLifecycle(t *testing.T) {
	for _, boundary := range []string{"master", "item", "recording"} {
		for _, pressAgain := range []bool{false, true} {
			t.Run(boundary+map[bool]string{false: "/old intent", true: "/new press"}[pressAgain], func(t *testing.T) {
				a, track := quietLikeApp()
				a.globalShortcuts, _ = testShortcutManager()
				post := posts(a)
				delete(a.liked, track.ID)
				fake(a).isLiked = func([]string) (map[string]bool, error) { return nil, nil }
				calls := make(chan bool, 1)
				fake(a).like = func(_ string, on bool) error { calls <- on; return nil }
				a.likeCurrentQuietly()
				manager := a.globalShortcuts
				if boundary == "recording" {
					manager.pause()
					manager.resume()
				} else {
					for _, enabled := range []bool{false, true} {
						cfg := manager.config.clone()
						if boundary == "master" {
							cfg.Enabled = enabled
						} else {
							binding := cfg.Bindings[shortcutLike]
							binding.Enabled = enabled
							cfg.Bindings[shortcutLike] = binding
						}
						if err := manager.apply(cfg, true, saveShortcutsForTest); err != nil {
							t.Fatal(err)
						}
					}
				}
				if pressAgain {
					a.likeCurrentQuietly()
				}
				runPost(t, post)
				if a.liked[track.ID] != pressAgain {
					t.Fatal("lookup revived an old intent or discarded a new key press")
				}
				if pressAgain {
					if !wait(t, calls, "the new like") {
						t.Fatal("new key press did not like")
					}
					runPost(t, post)
				}
			})
		}
	}
}

// Two failed rapid toggles used to restore an optimistic value rather than the confirmed state.
func TestShortcutLikeRapidFailure(t *testing.T) {
	for _, firstFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "second fails", true: "both fail"}[firstFails], func(t *testing.T) {
			a, _ := quietLikeApp()
			post := posts(a)
			calls := make(chan bool, 2)
			results := make(chan error, 2)
			defer close(results)
			fake(a).like = func(_ string, on bool) error { calls <- on; return <-results }
			a.likeCurrentQuietly()
			if !wait(t, calls, "the first like") {
				t.Fatal("first press did not like")
			}
			a.likeCurrentQuietly()
			var firstError error
			if firstFails {
				firstError = errors.New("offline")
			}
			results <- firstError
			runPost(t, post)
			if wait(t, calls, "the queued unlike") {
				t.Fatal("second press did not unlike")
			}
			results <- errors.New("offline")
			runPost(t, post)
			if a.liked["7"] != !firstFails || a.toast != "" || a.signIn.open {
				t.Fatal("failed toggles did not return to the confirmed state quietly")
			}
		})
	}
}

// A reply received while a source is disabled must not leave future likes permanently queued.
func TestShortcutLikeAfterSourceReenable(t *testing.T) {
	for _, firstFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success while disabled", true: "failure while disabled"}[firstFails], func(t *testing.T) {
			a, _ := quietLikeApp()
			lib, provider := a.library, fake(a)
			addFake(a, newFakeSource("other"))
			post := posts(a)
			calls := make(chan bool, 2)
			results := make(chan error, 2)
			defer close(results)
			provider.like = func(_ string, on bool) error { calls <- on; return <-results }
			a.likeCurrentQuietly()
			if !wait(t, calls, "the first like") {
				t.Fatal("first press did not like")
			}
			a.setPluginEnabled(lib, false)
			if firstFails {
				results <- errors.New("offline")
			} else {
				results <- nil
			}
			until(t, post, func() bool { return len(lib.likeWrites) == 0 })
			a.setPluginEnabled(lib, true)
			a.likeCurrentQuietly()
			if wait(t, calls, "the like after re-enabling") != firstFails {
				t.Fatal("re-enabled source did not toggle its confirmed state")
			}
			results <- nil
			until(t, post, func() bool { return len(lib.likeWrites) == 0 })
			if lib.liked["7"] != firstFails || a.signIn.open {
				t.Fatal("re-enabled source did not finish the new like quietly")
			}
		})
	}
}

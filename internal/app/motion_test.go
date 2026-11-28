package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 窗格切换有入场动画，结束后不再请求帧。
func TestPaneEntranceSettles(t *testing.T) {
	a, tester, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneLyrics
	settled(tester)

	a.npPane = paneQueue
	tester.Frame()
	if !tester.FramePending() {
		t.Fatal("switching panes asked for no frame, so the pane swapped at once")
	}
	settled(tester)
	if tester.FramePending() {
		t.Fatal("the pane entrance kept asking for frames after it ended")
	}
}

// 减少动效时窗格直接到位：切过去的第一帧就是最终画面。
func TestPaneSwitchUnderReduceMotionShowsItsResultAtOnce(t *testing.T) {
	a, tester, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneLyrics
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)

	a.npPane = paneQueue
	tester.Frame()
	first := cloneRGBA(tester.Image())
	settled(tester)
	bounds := first.Bounds()
	whole := ui.Rect{X: float32(bounds.Min.X), Y: float32(bounds.Min.Y), W: float32(bounds.Dx()), H: float32(bounds.Dy())}
	if changedPixels(first, tester.Image(), whole) != 0 {
		t.Fatal("reduce motion still eased the pane into place")
	}
}

// 指针进出曲目行时颜色与姿态渐变，指针离开后回到静止。
func TestSelectionFeedbackSettles(t *testing.T) {
	a, tracks := navigationApp()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)

	row, ok := tester.Find(tracks[0].Name)
	if !ok {
		t.Fatal("the first track is not on screen")
	}
	tester.Move(row.X+row.W/2, row.Y+row.H/2)
	tester.Frame()
	mid := cloneRGBA(tester.Image())
	tester.Advance(60 * time.Millisecond)
	tester.Frame()
	if !imagesDifferWithin(mid, tester.Image(), row) {
		t.Fatal("hovering a track row changed nothing")
	}
	tester.Move(4, 4)
	settled(tester)
	if tester.FramePending() {
		t.Fatal("the row kept asking for frames after the pointer left")
	}
}

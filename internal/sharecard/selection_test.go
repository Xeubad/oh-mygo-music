package sharecard_test

import (
	"testing"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/sharecard"
)

// 逆序选择仍按原曲排列，仅在跳过正文时插入省略号，译文随复制选择显示。
func TestSelectionOrdersAndSeparatesFragments(t *testing.T) {
	lines := []lyrics.Line{{Text: "First", Translation: "One"}, {Text: ""}, {Text: "Second"}, {Text: "Third"}, {Text: "First", Translation: "Again"}}
	selected := map[int]bool{4: true, 0: true, 2: true}
	fragment := sharecard.Select(lines, selected, true)
	if text := sharecard.Text(fragment); text != "First\nOne\nSecond\n…\nFirst\nAgain" {
		t.Fatalf("selection text = %q", text)
	}
	selected[3] = true
	if text := sharecard.Text(sharecard.Select(lines, selected, false)); text != "First\nSecond\nThird\nFirst" {
		t.Fatalf("continuous original = %q", text)
	}
}

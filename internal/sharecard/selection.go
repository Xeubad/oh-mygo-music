// Package sharecard creates static song cards independently of playback and windows.
package sharecard

import (
	"strings"

	"mygo-music/internal/lyrics"
)

type Line struct {
	Text        string
	Translation string
}

// Select 按来源顺序组成片段，空白行不把连续正文拆开。
func Select(lines []lyrics.Line, selected map[int]bool, translation bool) []Line {
	var fragment []Line
	gap := false
	for index, line := range lines {
		if strings.TrimSpace(line.Text) == "" {
			continue
		}
		if !selected[index] {
			gap = len(fragment) > 0
			continue
		}
		if gap {
			fragment = append(fragment, Line{Text: "…"})
		}
		part := Line{Text: line.Text}
		if translation {
			part.Translation = line.Translation
		}
		fragment = append(fragment, part)
		gap = false
	}
	return fragment
}

// Text 复制与图片共用片段，避免省略标记或译文规则出现分歧。
func Text(fragment []Line) string {
	var lines []string
	for _, line := range fragment {
		lines = append(lines, line.Text)
		if line.Translation != "" {
			lines = append(lines, line.Translation)
		}
	}
	return strings.Join(lines, "\n")
}

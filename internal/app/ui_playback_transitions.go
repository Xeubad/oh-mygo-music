package app

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
)

func (a *app) savePlaybackTransitions() {
	a.playbackSettingsError = ""
	if err := a.settings.save(); err != nil {
		a.toast = T("Could not save playback settings")
		a.playbackSettingsError = a.toast
	}
	if a.pl != nil {
		a.pl.RefreshTransitions()
	}
	a.redraw()
}

func (a *app) playbackTransitionSettings(c *ui.Context, row settingsRow, block settingsBlock) {
	preferences := &a.settings
	settingsHeading(c, block, T("Audio transitions"), iconRepeat)
	if a.playbackSettingsError != "" {
		block(a.playbackSettingsError, func() {
			ui.Text(c, a.playbackSettingsError).FontSize(12).TextColor(ui.RGB(184, 54, 54)).Margin(8, 16, 8, 16)
		})
	}
	row(T("Crossfade"), T("Blend songs on automatic track changes; takes priority over gapless playback"), func() {
		ui.Switch(c.Key("crossfade"), &preferences.CrossfadeEnabled).Label(T("Crossfade")).OnClick(func() { a.savePlaybackTransitions() })
	})
	a.transitionDuration(c, row, "crossfade-duration", T("Crossfade duration"), &preferences.CrossfadeDurationMs, 1000, 12000, 500, !preferences.CrossfadeEnabled)
	row(T("Gapless playback"), T("Join ready songs without adding silence; keeps silence in the recording"), func() {
		ui.Switch(c.Key("gapless"), &preferences.GaplessEnabled).Label(T("Gapless playback")).OnClick(func() { a.savePlaybackTransitions() })
	})
	settingsHeading(c, block, T("Play and Pause"), iconVolumeLow)
	row(T("Fade playback and pause"), T("Fade in when starting or resuming, and fade out before pausing"), func() {
		ui.Switch(c.Key("playback-fade"), &preferences.PlaybackFadeEnabled).Label(T("Fade playback and pause")).OnClick(func() { a.savePlaybackTransitions() })
	})
	a.transitionDuration(c, row, "pause-fade-duration", T("Pause fade out"), &preferences.PauseFadeOutMs, 50, 1000, 50, !preferences.PlaybackFadeEnabled)
	a.transitionDuration(c, row, "play-fade-duration", T("Playback fade in"), &preferences.PlayFadeInMs, 50, 1000, 50, !preferences.PlaybackFadeEnabled)
}

func (a *app) transitionDuration(c *ui.Context, row settingsRow, key, label string, value *int, minimum, maximum, step int, disabled bool) {
	row(label, "", func() {
		values := []int{*value}
		if maximum == 1000 {
			for _, preset := range []int{180, 260} {
				if preset != *value {
					values = append(values, preset)
				}
			}
		}
		for duration := minimum; duration <= maximum; duration += step {
			if duration != *value {
				values = append(values, duration)
			}
		}
		slices.Sort(values)
		labels := make([]string, len(values))
		selected := ""
		for index, duration := range values {
			if maximum > 1000 {
				labels[index] = fmt.Sprintf("%.1f s", float64(duration)/1000)
			} else {
				labels[index] = fmt.Sprintf("%d ms", duration)
			}
			if duration == *value {
				selected = labels[index]
			}
		}
		// 180/260ms 作为独立选项保留，不因控件的标准步进被隐式取整。
		if ui.Select(c.Key(key), &selected, labels).Label(label).Width(170).Disabled(disabled).Changed() {
			for index, option := range labels {
				if option == selected {
					*value = values[index]
					a.savePlaybackTransitions()
					break
				}
			}
		}
	})
}

package app

import (
	"runtime"

	"github.com/egoist/mygo/ui"
)

func (a *app) autoMixSettings(c *ui.Context, row settingsRow, block settingsBlock) {
	settingsHeading(c, block, T("Smart transitions"), iconRepeat)
	row(T("Smart transitions (AutoMix)"), T("Automatically choose a suitable transition for each pair of songs; no BPM or song matching needed"), func() {
		ui.Switch(c.Key("automix"), &a.settings.AutoMixEnabled).Label(T("Smart transitions (AutoMix)")).OnClick(a.savePlaybackTransitions)
	})
	if runtime.GOOS == "windows" {
		block(T("AutoMix runs locally"), func() {
			ui.Text(c, T("AutoMix analyzes songs on this computer and chooses the phrase length. If a transition is unsafe or not ready in time, playback continues with the original songs.")).FontSize(11.5).LineHeight(1.4).Margin(8, 16, 8, 16)
		})
	}
}

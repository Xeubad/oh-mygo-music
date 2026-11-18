package app

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/automix"
)

type autoMixSettingsState struct {
	status  string
	epoch   uint64
	advisor automix.Planner
}

var getAutoMixSecret = func(key string) ([]byte, error) { return mygo.Secrets.Get(key) }
var setAutoMixSecret = func(key string, value []byte) error { return mygo.Secrets.Set(key, value) }
var deleteAutoMixSecret = func(key string) error { return mygo.Secrets.Delete(key) }

func autoMixCredentialKey(endpoint string) string {
	normalized, err := automix.ChatEndpoint(endpoint)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256([]byte(normalized))
	return "automix-advisor-" + hex.EncodeToString(hash[:16])
}

func (a *app) configureAutoMixAdvisor() {
	if a.pl != nil {
		a.pl.CancelAutoMixEnhancement()
	}
	a.autoMix.epoch++
	epoch := a.autoMix.epoch
	a.autoMix.advisor = nil
	a.autoMix.status = ""
	if !a.settings.AutoMixAIEnabled || !a.settings.AutoMixEnabled {
		return
	}
	provider, exists := a.aiProvider(a.settings.AutoMixProviderID)
	key := aiCredentialKey(provider)
	if !exists || provider.Validate() != nil || key == "" || strings.TrimSpace(provider.Model) == "" {
		a.autoMix.status = T("Configure a compatible API address and model; local AutoMix remains available")
		return
	}
	temporary := a.aiTemporary(key)
	config := provider.Config()
	go func() {
		credential, err := readAICredential(key, temporary)
		config.APIKey = credential
		var advisor *automix.Advisor
		if err == nil {
			advisor, err = automix.NewAdvisor(config)
		}
		a.post(func() {
			if epoch != a.autoMix.epoch {
				return
			}
			if err != nil || advisor == nil {
				a.autoMix.status = T("AI is not configured; AutoMix uses local analysis")
				return
			}
			a.autoMix.advisor = advisor
			a.autoMix.status = T("AI enhancement ready; only music feature JSON is sent")
		})
	}()
}

func (a *app) autoMixSettings(c *ui.Context, row settingsRow, block settingsBlock) {
	settingsHeading(c, block, T("Smart transitions"), iconRepeat)
	row(T("Smart transitions (AutoMix)"), T("Automatically choose a suitable transition for each pair of songs; no BPM or song matching needed"), func() {
		ui.Switch(c.Key("automix"), &a.settings.AutoMixEnabled).Label(T("Smart transitions (AutoMix)")).OnClick(func() {
			a.configureAutoMixAdvisor()
			a.savePlaybackTransitions()
		})
	})
	if runtime.GOOS == "windows" {
		block(T("AutoMix runs locally"), func() {
			ui.Text(c, T("AutoMix analyzes songs on this computer and chooses the phrase length. If a transition is unsafe or not ready in time, playback continues with the original songs.")).FontSize(11.5).LineHeight(1.4).Margin(8, 16, 8, 16)
		})
	}
	row(T("Optional AI enhancement"), T("Send music feature JSON to your model; original audio stays on this computer"), func() {
		ui.Switch(c.Key("automix-ai"), &a.settings.AutoMixAIEnabled).Label(T("Optional AI enhancement")).Disabled(!a.settings.AutoMixEnabled).OnClick(func() {
			a.configureAutoMixAdvisor()
			a.savePlaybackTransitions()
		})
	})
	row(T("AI provider"), T("Manage providers, models and connection tests in AI and Models"), func() {
		ui.Column(c).Width(260).Gap(6).Children(func() {
			if a.settings.AutoMixAIEnabled {
				a.aiProviderChoice(c, "automix-provider")
			}
			if ui.Button(c.Key("manage-ai"), T("AI and Models")).Clicked() {
				a.settingsPage, a.settingsQuery = "ai", ""
			}
		})
	})
	if a.autoMix.status != "" {
		block(a.autoMix.status, func() {
			ui.Text(c, a.autoMix.status).FontSize(11.5).LineHeight(1.4).Margin(8, 16, 8, 16)
		})
	}
}

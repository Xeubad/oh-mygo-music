package app

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/automix"
)

func (a *app) aiProviderChoice(c *ui.Context, key string) {
	labels, identifiers := []string{T("Not selected")}, []string{""}
	selected := labels[0]
	for _, provider := range a.settings.AIProviders {
		labels = append(labels, provider.Name)
		identifiers = append(identifiers, provider.ID)
		if provider.ID == a.settings.AutoMixProviderID {
			selected = provider.Name
		}
	}
	if ui.Select(c.Key(key), &selected, labels).Label(T("AI provider")).Width(260).Changed() {
		for index, label := range labels {
			if label == selected {
				a.selectAIProvider(identifiers[index])
				break
			}
		}
	}
}

func (a *app) aiSettings(c *ui.Context, row settingsRow, block settingsBlock) {
	if a.ai.editing {
		a.aiProviderEditor(c, row, block)
		return
	}
	settingsHeading(c, block, T("AI usage"), iconAI)
	row(T("AutoMix provider"), T("AI is optional. Playback still uses local analysis when no provider is selected."), func() {
		a.aiProviderChoice(c, "ai-active-provider")
	})
	settingsHeading(c, block, T("Providers"), iconPlug)
	if len(a.settings.AIProviders) == 0 {
		block(T("No AI providers yet"), func() {
			ui.Text(c, T("Add a provider to choose a model and test the connection.")).FontSize(12).Margin(14, 16)
		})
	}
	for _, provider := range a.settings.AIProviders {
		item := provider
		host := item.Endpoint
		if parsed, err := url.Parse(host); err == nil {
			host = parsed.Host
		}
		description := host + " · " + item.Model
		if item.Model == "" {
			description = host + " · " + T("No model selected")
		}
		row(item.Name, description, func() {
			ui.Row(c.Key(item.ID)).Gap(6).Children(func() {
				label := T("Use for AutoMix")
				selected := item.ID == a.settings.AutoMixProviderID
				if selected {
					label = T("Selected")
				}
				ui.Button(c.Key("use"), label).Disabled(selected || a.ai.saving).OnClick(func() { a.selectAIProvider(item.ID) })
				ui.Button(c.Key("edit"), T("Edit")).Disabled(a.ai.saving).OnClick(func() { a.beginAIProvider(item.ID) })
			})
		})
	}
	row(T("Add provider"), T("Presets, local gateways and custom API addresses"), func() {
		ui.Button(c.Key("ai-add-provider"), T("Add provider")).Disabled(a.ai.saving).OnClick(func() { a.beginAIProvider("") })
	})
	a.aiStatus(c, block)
}

func (a *app) aiStatus(c *ui.Context, block settingsBlock) {
	if a.ai.status != "" {
		block(a.ai.status, func() {
			ui.Text(c, a.ai.status).FontSize(12).LineHeight(1.45).Margin(12, 16)
		})
	}
}

func (a *app) aiProviderEditor(c *ui.Context, row settingsRow, block settingsBlock) {
	draft := &a.ai.draft
	block(T("Provider editor"), func() {
		ui.Row(c).Padding(10, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Button(c.Key("ai-back"), T("Back to providers")).Disabled(a.ai.saving).OnClick(a.closeAIEditor)
			ui.Spacer(c)
			ui.Text(c, T("Changes apply after saving")).FontSize(11)
		})
	})
	settingsHeading(c, block, T("Connection"), iconPlug)
	row(T("Provider preset"), T("Choose a service or enter a custom address"), func() {
		labels := make([]string, len(automix.Presets))
		selected := 0
		for index, preset := range automix.Presets {
			labels[index] = preset.Name
			if preset.ID == draft.Preset {
				selected = index
			}
		}
		if index, changed := choice(c, "ai-preset", T("Provider preset"), labels, selected); changed {
			preset := automix.Presets[index]
			a.cancelAIProbe()
			draft.Name, draft.Preset, draft.Endpoint, draft.Protocol = T(preset.Name), preset.ID, preset.Endpoint, preset.Protocol
			draft.Model, draft.Effort, draft.Temperature = "", "", nil
			a.ai.keyDraft, a.ai.keyStatus, a.ai.models = "", "", nil
		}
	})
	row(T("Provider name"), T("A name to distinguish your accounts and gateways"), func() {
		if ui.TextInput(c.Key("ai-provider-name"), &draft.Name).Label(T("Provider name")).Width(260).FontSize(12).Disabled(a.ai.saving).Changed() {
			a.cancelAIProbe()
		}
	})
	row(T("API protocol"), T("Match the API supported by this address"), func() {
		protocols := []string{automix.ProtocolChat, automix.ProtocolResponses, automix.ProtocolMessages, automix.ProtocolGemini}
		labels := []string{"Chat Completions", "Responses", "Anthropic Messages", "Gemini generateContent"}
		if index, changed := choice(c, "ai-protocol", T("API protocol"), labels, max(0, slices.Index(protocols, draft.Protocol))); changed {
			a.cancelAIProbe()
			draft.Protocol, draft.Effort = protocols[index], ""
			a.ai.models = nil
		}
	})
	row(T("API base address"), T("Base URL or complete endpoint; local HTTP is supported"), func() {
		if ui.TextInput(c.Key("ai-base-url"), &draft.Endpoint).Label(T("API base address")).Width(260).FontSize(12).Disabled(a.ai.saving).Changed() {
			a.cancelAIProbe()
			a.ai.keyStatus, a.ai.models, a.ai.keyDraft = T("Address changed; enter a key for this address"), nil, ""
		}
	})
	row(T("AI API key"), T("Saved in the system credential store. Blank keeps the existing key."), func() {
		ui.Column(c).Width(260).Gap(6).Children(func() {
			input := ui.TextInput(c.Key("ai-credential"), &a.ai.keyDraft).Label(T("AI API key")).FontSize(12).Disabled(a.ai.saving)
			if !a.ai.showKey {
				input.Password()
			}
			if input.Changed() {
				a.cancelAIProbe()
			}
			ui.Row(c).Gap(6).Children(func() {
				label := T("Show key")
				if a.ai.showKey {
					label = T("Hide key")
				}
				ui.Button(c.Key("ai-show-key"), label).OnClick(func() { a.ai.showKey = !a.ai.showKey })
				ui.Button(c.Key("ai-remove-key"), T("Remove API key")).Disabled(a.ai.saving).OnClick(a.removeAIKey)
			})
			if a.ai.keyStatus != "" {
				ui.Text(c, a.ai.keyStatus).FontSize(11).LineHeight(1.4)
			}
		})
	})
	settingsHeading(c, block, T("Model and diagnostics"), iconAI)
	row(T("AI model"), T("Fetch models or enter the exact model ID manually"), func() {
		ui.Column(c).Width(260).Gap(6).Children(func() {
			if ui.TextInput(c.Key("ai-model-id"), &draft.Model).Label(T("AI model")).FontSize(12).Disabled(a.ai.saving).Changed() {
				a.cancelAIProbe()
			}
			ui.Row(c).Gap(6).Children(func() {
				ui.Button(c.Key("ai-fetch-models"), T("Fetch models")).Disabled(a.ai.saving || a.ai.job != "").OnClick(func() { a.probeAIProvider("models") })
				ui.Button(c.Key("ai-test-connection"), T("Test connection")).Disabled(a.ai.saving || a.ai.job != "" || strings.TrimSpace(draft.Model) == "").OnClick(func() { a.probeAIProvider("test") })
			})
			if a.ai.job != "" {
				ui.Button(c.Key("ai-cancel-request"), T("Cancel request")).OnClick(func() {
					a.cancelAIProbe()
					a.ai.status = T("Request canceled")
				})
			}
		})
	})
	if len(a.ai.models) > 0 {
		row(T("Available models"), T("Filter the list; manual entry remains available"), func() {
			ui.Column(c).Width(260).Gap(6).Children(func() {
				ui.TextInput(c.Key("ai-model-filter"), &a.ai.modelFilter).Label(T("Filter models")).Placeholder(T("Filter models")).FontSize(12)
				options := []string{T("Choose a model")}
				for _, model := range a.ai.models {
					if strings.Contains(strings.ToLower(model.ID+" "+model.Name), strings.ToLower(a.ai.modelFilter)) {
						options = append(options, model.ID)
					}
					if len(options) >= 101 {
						break
					}
				}
				selected := options[0]
				if ui.Select(c.Key("ai-model-picker"), &selected, options).Label(T("Available models")).Changed() && selected != options[0] {
					a.cancelAIProbe()
					draft.Model = selected
				}
			})
		})
	}
	block(T("Connection test"), func() {
		ui.Text(c, T("Testing sends a synthetic JSON decision, never your music.")).FontSize(11).LineHeight(1.4).Margin(8, 16)
	})
	a.aiStatus(c, block)
	row(T("Advanced parameters"), T("Optional parameters must be supported by the selected model"), func() {
		ui.Switch(c.Key("ai-advanced"), &a.ai.advanced).Label(T("Advanced parameters"))
	})
	if a.ai.advanced {
		a.aiAdvancedSettings(c, row, block)
	}
	settingsHeading(c, block, T("Save provider"), iconDatabase)
	row(T("Save provider"), T("Stores this configuration without turning on AI enhancement"), func() {
		ui.Button(c.Key("ai-save-provider"), T("Save provider")).Disabled(a.ai.saving).OnClick(a.saveAIProvider)
	})
	if _, exists := a.aiProvider(draft.ID); exists {
		row(T("Delete provider"), T("Removes this configuration and its key; AutoMix continues locally"), func() {
			if !a.ai.confirmDelete {
				ui.Button(c.Key("ai-delete-provider"), T("Delete provider")).Disabled(a.ai.saving).OnClick(func() { a.ai.confirmDelete = true })
			} else {
				ui.Row(c).Gap(6).Children(func() {
					ui.Button(c.Key("ai-confirm-delete"), T("Confirm delete")).OnClick(a.deleteAIProvider)
					ui.Button(c.Key("ai-cancel-delete"), T("Cancel")).OnClick(func() { a.ai.confirmDelete = false })
				})
			}
		})
	}
}

func (a *app) aiAdvancedSettings(c *ui.Context, row settingsRow, block settingsBlock) {
	draft := &a.ai.draft
	settingsHeading(c, block, T("Request parameters"), iconSettings)
	row(T("Request timeout"), T("Maximum AI wait; late results are ignored while local playback continues"), func() {
		values := []int{100, 300, 600, 1200, 2000, 3000, 5000}
		if !slices.Contains(values, draft.TimeoutMs) {
			values = append(values, draft.TimeoutMs)
			slices.Sort(values)
		}
		labels := make([]string, len(values))
		for index, value := range values {
			labels[index] = strconv.Itoa(value) + " ms"
		}
		if index, changed := choice(c, "ai-timeout", T("Request timeout"), labels, slices.Index(values, draft.TimeoutMs)); changed {
			a.cancelAIProbe()
			draft.TimeoutMs = values[index]
		}
	})
	row(T("Output token limit"), T("Automatic leaves optional limits out of compatible requests"), func() {
		values := []int{0, 128, 256, 512, 1024, 2048, 4096, 8192}
		if !slices.Contains(values, draft.MaxTokens) {
			values = append(values, draft.MaxTokens)
			slices.Sort(values)
		}
		labels := make([]string, len(values))
		for index, value := range values {
			labels[index] = strconv.Itoa(value)
		}
		labels[0] = T("Automatic")
		if index, changed := choice(c, "ai-max-tokens", T("Output token limit"), labels, slices.Index(values, draft.MaxTokens)); changed {
			a.cancelAIProbe()
			draft.MaxTokens = values[index]
		}
	})
	row(T("Custom temperature"), T("Leave off for models that do not support sampling parameters"), func() {
		on := draft.Temperature != nil
		ui.Switch(c.Key("ai-custom-temperature"), &on).Label(T("Custom temperature")).OnClick(func() {
			a.cancelAIProbe()
			draft.Temperature = nil
			if on {
				value := .2
				draft.Temperature = &value
			}
		})
	})
	if draft.Protocol == automix.ProtocolChat {
		row(T("Token limit field"), T("Use the field supported by your compatible provider"), func() {
			values := []string{"", "max_tokens", "max_completion_tokens"}
			if index, changed := choice(c, "ai-token-field", T("Token limit field"), []string{T("Automatic"), "max_tokens", "max_completion_tokens"}, max(0, slices.Index(values, draft.TokenLimitField))); changed {
				a.cancelAIProbe()
				draft.TokenLimitField = values[index]
			}
		})
	}
	if draft.Temperature != nil {
		row(T("Temperature"), "", func() {
			labels := []string{"0.0", "0.1", "0.2", "0.5", "0.7", "1.0", "1.5", "2.0"}
			current := fmt.Sprintf("%.1f", *draft.Temperature)
			if !slices.Contains(labels, current) {
				labels = append(labels, current)
			}
			if index, changed := choice(c, "ai-temperature", T("Temperature"), labels, slices.Index(labels, current)); changed {
				a.cancelAIProbe()
				value, _ := strconv.ParseFloat(labels[index], 64)
				draft.Temperature = &value
			}
		})
	}
	if draft.Protocol == automix.ProtocolChat || draft.Protocol == automix.ProtocolResponses {
		row(T("Reasoning effort"), T("Send only when the model explicitly supports this value"), func() {
			values := []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}
			labels := slices.Clone(values)
			labels[0] = T("Automatic")
			if index, changed := choice(c, "ai-effort", T("Reasoning effort"), labels, max(0, slices.Index(values, draft.Effort))); changed {
				a.cancelAIProbe()
				draft.Effort = values[index]
			}
		})
	}
}

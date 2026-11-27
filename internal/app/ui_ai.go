package app

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/ai"
	"mygo-music/internal/music"
)

const aiSecretKey = "ai-api-key"

var (
	getAISecret = func(key string) ([]byte, error) { return mygo.Secrets.Get(key) }
	setAISecret = func(key string, value []byte) error { return mygo.Secrets.Set(key, value) }
	newAIClient = ai.NewClient
)

type aiSuggestionState struct {
	open   bool
	busy   bool
	failed bool
	track  music.Track
	lib    *library
	pick   ai.Suggestion
	name   string
}

// openAISuggestion asks the configured model which own playlist fits tr.
func (a *app) openAISuggestion(tr *music.Track) {
	lib := a.trackLib(tr)
	if lib == nil || !lib.can("addToPlaylist") {
		return
	}
	cfg := a.aiConfig(a.settings.AIWebSearch)
	a.aiSuggestion = aiSuggestionState{open: true, busy: true, track: *tr, lib: lib}
	go func() {
		candidates := a.aiCandidates(lib, tr, 100)
		if len(candidates) == 0 {
			a.post(func() {
				a.aiSuggestion.busy = false
				a.aiSuggestion.failed = true
			})
			return
		}
		// 联网的 reasoning 模型可能接近一分钟，手动入口给它更长的余地。
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		pick, err := ai.Suggest(ctx, newAIClient(cfg), ai.SuggestInput{
			Song:       ai.Song{Name: tr.Name, Artists: tr.ArtistNames(), Album: tr.Album.Name},
			Candidates: candidates,
		})
		a.post(func() {
			d := &a.aiSuggestion
			d.busy = false
			if err != nil || pick.PlaylistID == "" {
				d.failed = true
				return
			}
			d.pick = pick
			for _, c := range candidates {
				if c.ID == pick.PlaylistID {
					d.name = c.Name
					break
				}
			}
		})
	}()
}

func (a *app) aiConfig(web bool) ai.Config {
	key := ""
	if b, err := getAISecret(aiSecretKey); err == nil {
		key = string(b)
	}
	tool := "web_search"
	if strings.Contains(strings.ToLower(a.settings.AIBaseURL), "api.deepseek.com") {
		tool = "deepseek_web_search"
	}
	return ai.Config{
		BaseURL: a.settings.AIBaseURL, APIKey: key, Model: a.settings.AIModel,
		WebSearch: web, WebSearchTool: tool,
	}
}

func (a *app) aiSettings(c *ui.Context, p palette, s *settings, row settingsRow, block settingsBlock) {
	settingsHeading(c, block, T("AI Playlist Suggestions"), iconSparkles)
	block(T("AI Playlist Suggestions"), func() {
		row(T("Enable AI Suggestions"), T("Let a chat completion service choose one of your own playlists for a song."), func() {
			if ui.Switch(c.Key("ai-enabled"), &s.AIEnabled).Label(T("Enable AI Suggestions")).Changed() {
				a.changed()
			}
		})
		row(T("Base URL"), T("Any endpoint that answers the OpenAI chat completions protocol."), func() {
			in := ui.TextInput(c.Key("ai-base-url"), &s.AIBaseURL).Label(T("Base URL")).FontSize(13)
			if in.Changed() {
				a.changed()
			}
		})
		row(T("Model"), "", func() {
			in := ui.TextInput(c.Key("ai-model"), &s.AIModel).Label(T("Model")).FontSize(13)
			if in.Changed() {
				a.changed()
			}
		})
		row(T("API Key"), T("Stored in the operating system credential store, not settings.json."), func() {
			ui.Column(c).Width(320).MaxWidthPercent(100).Gap(6).Children(func() {
				ui.TextInput(c.Key("ai-key"), &a.aiKeyDraft).Label(T("API Key")).Password().FontSize(13)
				if ui.Button(c.Key("ai-save-key"), T("Save Key")).Disabled(strings.TrimSpace(a.aiKeyDraft) == "").Clicked() {
					a.saveAIKey()
				}
				if a.aiStatus != "" {
					ui.Text(c, a.aiStatus).FontSize(11.5).TextColor(p.muted)
				}
			})
		})
		row(T("Test Connection"), "", func() {
			if ui.Button(c.Key("ai-test"), T("Test Connection")).
				Disabled(a.aiTesting || s.AIBaseURL == "" || s.AIModel == "").Clicked() {
				a.testAIConnection()
			}
		})
		row(T("Web Search"), T("Use the service's own web search when it has one. Off unless you turn it on."), func() {
			if ui.Switch(c.Key("ai-web-search"), &s.AIWebSearch).Label(T("Web Search")).Changed() {
				a.changed()
			}
		})
		row(T("Suggest After Like"), T("After a like, ask for a playlist in the background and show it only when the model is confident."), func() {
			on := !s.AIAutoSuggestDisabled
			if ui.Switch(c.Key("ai-auto-suggest"), &on).Label(T("Suggest After Like")).Changed() {
				s.AIAutoSuggestDisabled = !on
				a.changed()
			}
		})
		row(T("Add When Confident"), T("Add the song to the suggested playlist at once when the model is at least 85 percent sure. You can undo for 10 seconds."), func() {
			if ui.Switch(c.Key("ai-auto-add"), &s.AIAutoAdd).Label(T("Add When Confident")).Changed() {
				a.changed()
			}
		})
	})
}

func (a *app) saveAIKey() {
	key := strings.TrimSpace(a.aiKeyDraft)
	if key == "" {
		return
	}
	if err := setAISecret(aiSecretKey, []byte(key)); err != nil {
		a.aiStatus = T("Couldn't save the API key")
		return
	}
	a.aiKeyDraft = ""
	a.aiStatus = T("API key saved")
	a.redraw()
}

func (a *app) testAIConnection() {
	a.aiTesting = true
	a.aiStatus = T("Testing…")
	cfg := a.aiConfig(false)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := newAIClient(cfg).Complete(ctx, []ai.Message{{Role: "user", Content: "Reply with OK."}})
		a.post(func() {
			a.aiTesting = false
			if err != nil {
				a.aiStatus = Tf("Connection failed: %s", err)
			} else {
				a.aiStatus = T("Connection works")
			}
			a.redraw()
		})
	}()
}

// autoSuggestPlaylist asks for a suggestion after the source confirmed a like,
// using only the playlists' metadata so the loudness of liking stays quick.
func (a *app) autoSuggestPlaylist(lib *library, tr music.Track) {
	if !a.settings.AIEnabled || a.settings.AIAutoSuggestDisabled ||
		a.settings.AIBaseURL == "" || a.settings.AIModel == "" {
		return
	}
	if a.aiAsked == nil {
		a.aiAsked = map[string]bool{}
	}
	if a.aiAsked[tr.ID] {
		return
	}
	a.aiAsked[tr.ID] = true
	cfg := a.aiConfig(false)
	go func() {
		candidates := a.aiCandidates(lib, &tr, 0)
		if len(candidates) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pick, err := ai.Suggest(ctx, newAIClient(cfg), ai.SuggestInput{
			Song:       ai.Song{Name: tr.Name, Artists: tr.ArtistNames(), Album: tr.Album.Name},
			Candidates: candidates,
		})
		if err != nil {
			a.post(func() { delete(a.aiAsked, tr.ID) })
			return
		}
		if pick.PlaylistID == "" || pick.Confidence < 0.85 {
			return
		}
		name := pick.PlaylistID
		for _, c := range candidates {
			if c.ID == pick.PlaylistID {
				name = c.Name
				break
			}
		}
		a.post(func() {
			if a.settings.AIAutoAdd && lib.can("removeFromPlaylist") {
				a.addToThen(lib, pick.PlaylistID, &tr, func(err error) {
					if err != nil {
						return
					}
					a.aiToast = &ui.Toast{
						ID:       "ai-added-" + tr.ID,
						Title:    Tf("Added to %s", name),
						Action:   T("Undo"),
						OnAction: func() { a.removeFrom(lib, pick.PlaylistID, &tr) },
						Timeout:  10 * time.Second,
					}
					a.redraw()
				})
				return
			}
			a.aiToast = &ui.Toast{
				ID:          "ai-playlist-" + tr.ID,
				Title:       Tf("AI suggests %s", name),
				Description: pick.Reason,
				Action:      T("Add"),
				OnAction:    func() { a.addTo(lib, pick.PlaylistID, &tr) },
				Timeout:     10 * time.Second,
			}
			a.redraw()
		})
	}()
}

// aiCandidates are the listener's own playlists for the song's source, with
// at most samples of each. The model never sees saved or fixed playlists.
func (a *app) aiCandidates(lib *library, tr *music.Track, samples int) []ai.Candidate {
	var out []ai.Candidate
	for _, pl := range lib.mine {
		if len(out) >= 20 {
			break
		}
		if !pl.Mine || pl.Fixed {
			continue
		}
		c := ai.Candidate{ID: pl.ID, Name: pl.Name, Description: pl.Description, TrackCount: pl.TrackCount}
		if samples > 0 {
			if full, err := lib.src.Playlist(pl.ID); err == nil && full != nil {
				for _, t := range full.Tracks {
					if len(c.Samples) >= samples {
						break
					}
					line := t.Name
					if names := t.ArtistNames(); names != "" {
						line += " - " + names
					}
					c.Samples = append(c.Samples, line)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func (a *app) aiSuggestionDialog(c *ui.Context) {
	p := a.pal
	d := &a.aiSuggestion
	ui.DialogBase(c.Key("ai-suggestion"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(420).Height(240).Padding(24).Radius(18).Gap(14).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})
		panel.Children(func() {
			ui.Column(c).Gap(14).Children(func() {
				ui.Text(c, T("AI Playlist Suggestion")).FontSize(17).FontWeight(700)
				switch {
				case d.busy:
					ui.Text(c, T("Thinking…")).FontSize(13).TextColor(p.muted)
				case d.failed:
					ui.Text(c, T("Couldn't guess a playlist. Choose one yourself.")).FontSize(13).TextColor(p.muted)
				default:
					ui.Text(c, d.name).FontSize(15).FontWeight(600)
					ui.Text(c, d.pick.Reason).FontSize(13).TextColor(p.muted)
					ui.Text(c, Tf("%d percent confident", int(d.pick.Confidence*100))).FontSize(11).TextColor(p.muted)
				}
				ui.Row(c).Width(372).Height(36).Gap(10).Justify(ui.End).Children(func() {
					if pillButton(c.Key("ai-cancel"), nil, T("Cancel"), p.hover.Over(p.bg), p.text).Clicked() {
						d.open = false
					}
					if !d.busy && !d.failed {
						if pillButton(c.Key("ai-add"), nil, T("Add"), p.accent, p.onAccent).Clicked() {
							d.open = false
							a.addTo(d.lib, d.pick.PlaylistID, &d.track)
						}
					}
				})
			})
		})
	})
}

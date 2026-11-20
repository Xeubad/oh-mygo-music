package app

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
	"mygo-music/internal/sharecard"
)

var cardTemplates = []struct{ id, label string }{
	{"", N("Minimal")}, {"cinema", N("Cinematic")}, {"vinyl", N("Vinyl")}, {"polaroid", N("Polaroid")}, {"ticket", N("Ticket")},
}

func (a *app) shareDialog(c *ui.Context) {
	draft := a.sharing
	if draft == nil {
		return
	}
	width, height := c.Size()
	palette := a.pal
	busy := draft.onlinePublishing || draft.pendingOutput != shareOutputNone || draft.exporting
	// DialogBase 会在 Escape 或点击遮罩时改写 open；忙碌时只改局部值，保持模态与任务存活。
	open := draft.open
	ui.DialogBase(c.Key("share-card"), &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.5))
		if busy {
			panel.Width(min(420, width-48)).Padding(28).Radius(20).Gap(14).AlignItems(ui.Center).
				Background(palette.bg).Border(1, palette.hairline)
			ui.Spinner(c).FontSize(32).TextColor(palette.accent).Label(T("Preparing share"))
			status := draft.status
			if status == "" {
				status = T("Preparing share…")
			}
			ui.Text(c, status).FontSize(15).FontWeight(600).TextColor(palette.text)
			ui.Text(c, T("Please wait until processing finishes")).FontSize(13).TextColor(palette.muted)
			ui.Text(c, draft.track.Name+" · "+draft.track.ArtistNames()).FontSize(12).TextColor(palette.muted).SingleLine()
			return
		}
		panel.Width(min(1040, width-48)).Padding(24).Radius(20).Gap(18).Background(palette.bg).Border(1, palette.hairline)
		ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Text(c, T("Share Card")).FontSize(20).FontWeight(700)
				ui.Text(c, draft.track.Name+" · "+draft.track.ArtistNames()).FontSize(13).TextColor(palette.muted).SingleLine()
			})
			if iconButton(c.Key("close-share"), iconX, 20, palette.muted, T("Close Share Card")).Clicked() {
				if draft.onlineCancel != nil {
					draft.onlineCancel()
				}
				draft.open = false
			}
		})
		a.shareControls(c, draft)
		a.shareLinkControls(c, draft)
		linesHeight := height - 310
		if draft.onlineMode == "clip" {
			if start, end, ok := draft.clipRange(); ok {
				a.shareClipControls(c, draft, start, end)
				linesHeight -= 88
			}
		}
		ui.Row(c).Height(max(128, min(470, linesHeight))).Gap(24).AlignItems(ui.Stretch).Children(func() {
			ui.Scroll(c.Key("share-lines")).Grow(1).Basis(0).MinWidth(0).Children(func() {
				ui.Column(c).Gap(6).Children(func() {
					switch {
					case draft.loading:
						ui.Text(c, T("Loading lyrics…")).TextColor(palette.muted)
					case draft.loadError != nil:
						ui.Text(c, T("Could not load lyrics")).TextColor(palette.muted)
						if ui.Button(c, T("Retry Lyrics")).Clicked() {
							a.loadShareLyrics(draft)
						}
					case len(draft.lines) == 0:
						ui.Text(c, T("Share the song without lyrics")).TextColor(palette.muted)
					default:
						for index, line := range a.shareLines(draft) {
							row := ui.ButtonBase(c.Key(index)).Column().AlignItems(ui.Start).Padding(12, 14).Gap(6).Radius(10).
								Disabled(draft.onlinePublishing || draft.exporting || draft.pendingOutput != shareOutputNone).Label(Tf("Select lyric %d", index+1))
							if draft.selected[index] {
								row.Background(palette.accent.Alpha(0.12)).Border(1, palette.accent.Alpha(0.35))
							}
							row.Children(func() {
								ui.Text(c, line.Text).FontSize(16).FontWeight(500).TextColor(palette.text)
								if line.Translation != "" {
									ui.Text(c, line.Translation).FontSize(13).TextColor(palette.muted)
								}
							})
							if row.Clicked() {
								a.stopSharePreview(draft)
								draft.clipStart, draft.clipEnd, draft.clipHead = 0, 0, 0
								if c.Modifiers()&ui.Shift != 0 && draft.hasSelectionAnchor {
									for selected := min(draft.selectionAnchor, index); selected <= max(draft.selectionAnchor, index); selected++ {
										draft.selected[selected] = true
									}
								} else {
									draft.selected[index] = !draft.selected[index]
									draft.selectionAnchor, draft.hasSelectionAnchor = index, true
								}
								draft.onlineRevision++
								a.requestShareRender()
							}
						}
					}
				})
			})
			ui.Column(c.Key("preview")).Grow(1).Basis(0).MinWidth(0).Center().Gap(10).Background(palette.hover).Radius(12).Padding(16).Children(func() {
				switch {
				case draft.preview != nil:
					ui.Image(c, draft.preview).Grow(1).FillWidth().Fit(ui.Contain).Label(T("Card Preview"))
				case errors.Is(draft.renderError, sharecard.ErrCapacity):
					ui.Text(c, T("Too much text. Select fewer lines or change the template or size.")).FontSize(14).TextColor(palette.muted)
				case draft.renderError != nil:
					ui.Text(c, T("Could not create the card")).TextColor(palette.muted)
					if ui.Button(c, T("Retry Preview")).Clicked() {
						a.requestShareRender()
					}
				default:
					ui.Text(c, T("Preparing preview…")).TextColor(palette.muted)
				}
				if draft.coverError != nil {
					ui.Text(c, T("Cover unavailable; using a plain background")).FontSize(11).TextColor(palette.muted)
					if ui.Button(c, T("Retry Cover")).Clicked() {
						a.loadShareCover(draft)
						a.requestShareRender()
					}
				}
				if !draft.cardOnline && sharecard.PageURL(draft.track.URL) == "" {
					ui.Text(c, T("No song link; QR code omitted")).FontSize(11).TextColor(palette.muted)
				}
				if draft.cardOnline && a.onlineLinkMatchesDraft(draft) {
					ui.Text(c, T("QR code uses the online listening link")).FontSize(11).TextColor(palette.muted)
				} else if draft.cardOnline && draft.onlineURL != "" {
					ui.Text(c, T("Online link is for a previous snapshot")).FontSize(11).TextColor(palette.muted)
				}
			})
		})
		ui.Row(c).Justify(ui.End).Gap(10).Children(func() {
			if draft.status != "" {
				ui.Text(c, draft.status).FontSize(12).TextColor(palette.muted)
			}
			ui.Spacer(c)
			if ui.Button(c.Key("copy-online-link"), T("Copy Listen Link")).
				Disabled(draft.onlinePublishing || draft.loading || draft.coverLoading || draft.exporting || draft.pendingOutput != shareOutputNone).Clicked() {
				a.copyOnlineShareLink(draft)
			}
			if ui.PrimaryButton(c.Key("create-online-link"), T("Create Listen Link")).
				Disabled(draft.onlinePublishing || draft.rendering || draft.loading || draft.coverLoading || draft.exporting || draft.pendingOutput != shareOutputNone).
				Clicked() {
				a.publishOnlineShare(draft)
			}
			if ui.Button(c, T("Copy Lyrics")).Disabled(len(a.shareFragment(draft)) == 0).Clicked() {
				a.copyShareLyrics(draft)
			}
			if ui.Button(c, T("Copy Image")).Disabled(len(draft.png) == 0 || draft.exporting || draft.onlinePublishing || draft.pendingOutput != shareOutputNone).Clicked() {
				a.copyShareImage(draft)
			}
			if ui.PrimaryButton(c, T("Save PNG")).Disabled(len(draft.png) == 0 || draft.exporting || draft.onlinePublishing || draft.pendingOutput != shareOutputNone).Clicked() {
				a.saveShareImage(draft)
			}
		})
	})
	if !busy && !open {
		draft.open = false
	}
	if !draft.open {
		a.stopSharePreview(draft)
		if draft.onlineCancel != nil {
			draft.onlineCancel()
		}
		a.sharing = nil
	}
}

func (a *app) shareClipControls(c *ui.Context, draft *shareDraft, start, end time.Duration) {
	if draft.clipPreviewActive {
		if draft.clipPreview.Playing() {
			draft.clipHead = draft.clipPreview.Position()
			c.After(50 * time.Millisecond)
		} else {
			draft.clipPreviewActive, draft.clipHead = false, end
		}
	}
	duration := draft.track.Length()
	low, high := start.Seconds(), end.Seconds()
	head := min(max(draft.clipHead, start), end).Seconds()
	ui.Column(c.Key("clip-timeline")).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, fmt.Sprintf("%s %s – %s", T("Clip Range:"), shareClipTime(start), shareClipTime(end))).FontSize(12).TextColor(a.pal.muted)
			ui.Spacer(c)
			if draft.clipPreviewLoading || draft.clipPreviewActive {
				ui.Button(c.Key("stop-clip-preview"), T("Stop Preview")).OnClick(func() {
					if draft.clipPreviewLoading {
						a.stopSharePreview(draft)
					} else {
						draft.clipPreview.Pause()
						draft.clipPreviewActive, draft.clipHead = false, start
						draft.clipPreview.Seek(start)
					}
				})
			} else {
				ui.Button(c.Key("preview-clip"), T("Preview Clip")).OnClick(func() { a.previewShareClip(draft) })
			}
		})
		ui.RangeSlider(c.Key("clip-range"), &low, &high, 0, duration.Seconds(), 0).
			FillWidth().Label(T("Clip Range:")).OnChange(func() {
			newStart, newEnd := clipSliderTime(low), clipSliderTime(high)
			// 两端相遇时仍给被拖动的一端留出可试听的区间。
			minLength := min(100*time.Millisecond, end-start)
			if newStart != start {
				newStart = min(newStart, newEnd-minLength)
			} else {
				newEnd = max(newEnd, newStart+minLength)
			}
			if newStart == start && newEnd == end {
				return
			}
			draft.clipStart, draft.clipEnd = newStart, newEnd
			if draft.clipHead < newStart || draft.clipHead >= newEnd {
				draft.clipHead = newStart
			}
			if draft.clipPreview != nil {
				draft.clipPreview.SetRange(newStart, newEnd, draft.clipHead)
			}
			draft.onlineRevision++
			if draft.cardOnline {
				a.requestShareRender()
			}
		})
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, T("Preview Playhead")).FontSize(12).TextColor(a.pal.muted)
			ui.Slider(c.Key("clip-playhead"), &head, start.Seconds(), end.Seconds()).Grow(1).Label(T("Preview Playhead")).
				OnChange(func() {
					draft.clipHead = clipSliderTime(head)
					if draft.clipPreview != nil {
						draft.clipPreview.Seek(draft.clipHead)
					}
				})
			ui.Text(c, shareClipTime(secs(head))).FontSize(12).TextColor(a.pal.muted)
		})
	})
}

func clipSliderTime(seconds float64) time.Duration {
	return time.Duration(math.Round(seconds*1000)) * time.Millisecond
}

func shareClipTime(at time.Duration) string {
	seconds := int(at / time.Second)
	return fmt.Sprintf("%d:%02d.%03d", seconds/60, seconds%60, at.Milliseconds()%1000)
}

func (a *app) shareControls(c *ui.Context, draft *shareDraft) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Disabled(draft.onlinePublishing || draft.exporting || draft.pendingOutput != shareOutputNone).Children(func() {
		if draft.aggregateView != nil && len(uniqueAggregateSources(draft.track.AggregateSources)) > 1 {
			source := aggregateViewSource(c, draft.aggregateView, &draft.track)
			if queued, err := aggregateQueueTracks([]music.Track{draft.track}, draft.aggregateView.sourceChoices); err != nil {
				draft.status = err.Error()
			} else if queued[0].ID != draft.track.ID {
				if draft.pendingAggregateTrackID != queued[0].ID {
					draft.pendingAggregateTrackID = queued[0].ID
					selectedTrack, selectedSource := queued[0], source
					a.post(func() {
						if a.sharing != draft || !draft.open {
							return
						}
						draft.track = selectedTrack
						a.stopSharePreview(draft)
						draft.clipStart, draft.clipEnd, draft.clipHead = 0, 0, 0
						draft.recordingSource = &selectedSource
						draft.pendingAggregateTrackID = ""
						draft.lines = nil
						draft.selected = make(map[int]bool)
						draft.hasSelectionAnchor = false
						draft.timingReliable = false
						draft.display = lyricDisplayCache{}
						draft.onlineRevision++
						a.loadShareLyrics(draft)
						a.requestShareRender()
					})
				}
			} else {
				draft.recordingSource = &source
			}
		}
		modeLabel := T("Full Song")
		if draft.onlineMode == "clip" {
			modeLabel = T("Selected Clip")
		}
		mode := ui.Button(c.Key("online-mode"), modeLabel).Label(T("Listen Mode")).Disabled(draft.onlinePublishing).
			Menu(func(menu *ui.Menu) {
				if menu.Item(T("Full Song")).Radio(draft.onlineMode == "full").Chosen() {
					a.stopSharePreview(draft)
					draft.onlineMode = "full"
					draft.onlineError = nil
					draft.onlineRevision++
					a.requestShareRender()
				}
				_, _, clipAvailable := shareClipRange(draft.lines, draft.selected, draft.track.Length(), draft.timingReliable)
				if menu.Item(T("Selected Clip")).Radio(draft.onlineMode == "clip").
					Disabled(!clipAvailable || draft.onlinePublishing).Chosen() {
					draft.onlineMode = "clip"
					draft.onlineError = nil
					draft.onlineRevision++
					a.requestShareRender()
				}
			})
		if _, _, available := shareClipRange(draft.lines, draft.selected, draft.track.Length(), draft.timingReliable); !available {
			mode.Tooltip(T("Clip mode requires selected lyrics with reliable timing"))
		}
		qualityLabel := qualityLevels[1].label
		for _, quality := range qualityLevels {
			if quality.level == draft.onlineQuality {
				qualityLabel = quality.label
			}
		}
		ui.Button(c.Key("online-quality"), T(qualityLabel)).Label(T("Audio Quality")).
			Disabled(draft.onlinePublishing).Menu(func(menu *ui.Menu) {
			for _, quality := range qualityLevels {
				if menu.Item(T(quality.label)).Radio(draft.onlineQuality == quality.level).Chosen() {
					a.stopSharePreview(draft)
					draft.onlineQuality = quality.level
					draft.onlineRevision++
					a.requestShareRender()
				}
			}
		})
		label := cardTemplates[0].label
		for _, template := range cardTemplates {
			if template.id == a.settings.CardTemplate {
				label = template.label
			}
		}
		ui.Button(c.Key("card-template"), T(label)).Label(T("Card Template")).Disabled(draft.onlinePublishing).Menu(func(menu *ui.Menu) {
			for _, template := range cardTemplates {
				if menu.Item(T(template.label)).Radio(a.settings.CardTemplate == template.id).Chosen() {
					a.settings.CardTemplate = template.id
					draft.onlineRevision++
					a.saveLyricChoices()
				}
			}
		})
		size := T("Portrait (3:4)")
		if a.settings.CardSquare {
			size = T("Square (1:1)")
		}
		ui.Button(c.Key("card-size"), size).Label(T("Card Size")).Disabled(draft.onlinePublishing).Menu(func(menu *ui.Menu) {
			if menu.Item(T("Portrait (3:4)")).Radio(!a.settings.CardSquare).Chosen() {
				a.settings.CardSquare = false
				draft.onlineRevision++
				a.saveLyricChoices()
			}
			if menu.Item(T("Square (1:1)")).Radio(a.settings.CardSquare).Chosen() {
				a.settings.CardSquare = true
				draft.onlineRevision++
				a.saveLyricChoices()
			}
		})
		available := false
		for index, line := range draft.lines {
			if draft.selected[index] && line.Translation != "" {
				available = true
			}
		}
		translationLabel := "Translation On"
		if a.settings.HideCardTranslation {
			translationLabel = "Translation Off"
		}
		translation := ui.Button(c.Key("card-translation"), T(translationLabel)).Label(T("Show Translation")).Disabled(!available || draft.onlinePublishing)
		if !a.settings.HideCardTranslation {
			translation.Background(a.pal.accent.Alpha(0.14))
		}
		if !available {
			translation.Tooltip(T("No translation available"))
		}
		if translation.Clicked() {
			a.settings.HideCardTranslation = !a.settings.HideCardTranslation
			draft.onlineRevision++
			a.saveLyricChoices()
		}
		ui.Button(c.Key("card-language"), T("Lyrics Display")).Disabled(draft.onlinePublishing).Menu(func(menu *ui.Menu) {
			script := a.settings.LyricsScript
			original := a.settings.ChineseLyrics[a.lyricKey(&draft.track)]
			a.lyricScriptItems(menu, &draft.track)
			if script != a.settings.LyricsScript || original != a.settings.ChineseLyrics[a.lyricKey(&draft.track)] {
				draft.onlineRevision++
				a.requestShareRender()
			}
		})
	})
}

func (a *app) shareLinkControls(c *ui.Context, draft *shareDraft) {
	label := "Original Song Page"
	hint := "QR code links to the original song page"
	if draft.cardOnline {
		label = "Online Listening Link"
		hint = "Copying or saving reuses an active share or creates a listening link"
		if a.onlineLinkMatchesDraft(draft) {
			hint = "QR code uses the online listening link"
		}
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Button(c.Key("card-link-target"), T("Card Link")+": "+T(label)).Label(T("Card Link")).
			Disabled(draft.onlinePublishing || draft.exporting || draft.pendingOutput != shareOutputNone).
			Menu(func(menu *ui.Menu) {
				if menu.Item(T("Online Listening Link")).Radio(draft.cardOnline).Chosen() && !draft.cardOnline {
					draft.cardOnline = true
					a.requestShareRender()
				}
				if menu.Item(T("Original Song Page")).Radio(!draft.cardOnline).Chosen() && draft.cardOnline {
					draft.cardOnline = false
					a.requestShareRender()
				}
			})
		ui.Text(c, T(hint)).FontSize(11.5).TextColor(a.pal.muted).Shrink(1).MinWidth(0)
	})
}

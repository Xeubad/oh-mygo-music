package app

import (
	"errors"

	"github.com/egoist/mygo/ui"
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
	ui.DialogBase(c.Key("share-card"), &draft.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.5))
		panel.Width(min(1040, width-48)).Padding(24).Radius(20).Gap(18).Background(palette.bg).Border(1, palette.hairline)
		ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Text(c, T("Share Card")).FontSize(20).FontWeight(700)
				ui.Text(c, draft.track.Name+" · "+draft.track.ArtistNames()).FontSize(13).TextColor(palette.muted).SingleLine()
			})
			if iconButton(c.Key("close-share"), iconX, 20, palette.muted, T("Close Share Card")).Clicked() {
				draft.open = false
			}
		})
		a.shareControls(c, draft)
		ui.Row(c).Height(max(180, min(470, height-310))).Gap(24).AlignItems(ui.Stretch).Children(func() {
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
							row := ui.ButtonBase(c.Key(index)).Column().AlignItems(ui.Start).Padding(12, 14).Gap(6).Radius(10).Label(Tf("Select lyric %d", index+1))
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
								draft.selected[index] = !draft.selected[index]
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
				if sharecard.PageURL(draft.track.URL) == "" {
					ui.Text(c, T("No song link; QR code omitted")).FontSize(11).TextColor(palette.muted)
				}
			})
		})
		ui.Row(c).Justify(ui.End).Gap(10).Children(func() {
			if draft.status != "" {
				ui.Text(c, draft.status).FontSize(12).TextColor(palette.muted)
			}
			ui.Spacer(c)
			if ui.Button(c, T("Copy Lyrics")).Disabled(len(a.shareFragment(draft)) == 0).Clicked() {
				a.copyShareLyrics(draft)
			}
			if ui.Button(c, T("Copy Image")).Disabled(len(draft.png) == 0 || draft.exporting).Clicked() {
				a.copyShareImage(draft)
			}
			if ui.PrimaryButton(c, T("Save PNG")).Disabled(len(draft.png) == 0 || draft.exporting).Clicked() {
				a.saveShareImage(draft)
			}
		})
	})
	if !draft.open {
		a.sharing = nil
	}
}

func (a *app) shareControls(c *ui.Context, draft *shareDraft) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		label := cardTemplates[0].label
		for _, template := range cardTemplates {
			if template.id == a.settings.CardTemplate {
				label = template.label
			}
		}
		ui.Button(c.Key("card-template"), T(label)).Label(T("Card Template")).Menu(func(menu *ui.Menu) {
			for _, template := range cardTemplates {
				if menu.Item(T(template.label)).Radio(a.settings.CardTemplate == template.id).Chosen() {
					a.settings.CardTemplate = template.id
					a.saveLyricChoices()
				}
			}
		})
		size := T("Portrait (3:4)")
		if a.settings.CardSquare {
			size = T("Square (1:1)")
		}
		ui.Button(c.Key("card-size"), size).Label(T("Card Size")).Menu(func(menu *ui.Menu) {
			if menu.Item(T("Portrait (3:4)")).Radio(!a.settings.CardSquare).Chosen() {
				a.settings.CardSquare = false
				a.saveLyricChoices()
			}
			if menu.Item(T("Square (1:1)")).Radio(a.settings.CardSquare).Chosen() {
				a.settings.CardSquare = true
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
		translation := ui.Button(c.Key("card-translation"), T(translationLabel)).Label(T("Show Translation")).Disabled(!available)
		if !a.settings.HideCardTranslation {
			translation.Background(a.pal.accent.Alpha(0.14))
		}
		if !available {
			translation.Tooltip(T("No translation available"))
		}
		if translation.Clicked() {
			a.settings.HideCardTranslation = !a.settings.HideCardTranslation
			a.saveLyricChoices()
		}
		ui.Button(c.Key("card-language"), T("Lyrics Display")).Menu(func(menu *ui.Menu) { a.lyricScriptItems(menu, &draft.track) })
	})
}

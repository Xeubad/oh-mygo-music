package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *app) downloadSourceName(id string) string {
	if lib := a.libOf(id); lib != nil {
		return lib.name()
	}
	for _, lib := range a.allLibs {
		if lib.id == id {
			return lib.name()
		}
	}
	return id
}

func (a *app) downloadPage(c *ui.Context) {
	p := a.pal
	top, bottom := a.insets(c)

	var running, succeeded, failed, finished int
	for _, d := range a.downloads.tasks {
		switch d.card.state {
		case backgroundTaskRunning:
			running++
		case backgroundTaskSucceeded:
			succeeded++
			finished++
		case backgroundTaskFailed:
			failed++
			finished++
		case backgroundTaskCanceled:
			finished++
		}
	}

	sub := T("No download tasks")
	if len(a.downloads.tasks) > 0 {
		sub = Tf("%d active · %d completed · %d failed", running, succeeded, failed)
	}

	ui.Scroll(c.Key(a.route)).TrackScroll(&a.pageScroll).Grow(1).
		Padding(top+pageGap, pagePad, bottom+32, pagePad).ScrollbarInsets(top, 0, bottom, 0).Children(func() {
		a.startMark(c)
		defer a.endMark(c)

		a.heading(c, T("Downloads"), sub)

		ui.Row(c).Wrap().Gap(10).AlignItems(ui.Center).Margin(0, 0, 20, 0).Children(func() {
			segmented(c.Key("download-filter"), &a.downloads.tab, T("All"), T("In progress"), T("Completed"), T("Failed"))
			ui.Spacer(c)
			if running > 0 {
				ui.Button(c.Key("cancel-all"), T("Cancel All")).OnClick(a.cancelAllDownloads)
			}
			if failed > 0 {
				ui.Button(c.Key("retry-failed"), T("Retry Failed")).OnClick(a.retryFailedDownloads)
			}
			if finished > 0 {
				ui.Button(c.Key("clear-finished"), T("Clear Finished")).OnClick(a.clearFinishedDownloads)
			}
		})

		if len(a.downloads.tasks) == 0 {
			a.empty(c, T("No download tasks"))
			return
		}

		// 按最新加入顺序从上到下展示，方便用户立即看到刚入队的单曲或批量任务。
		var shown []*downloadTask
		for i := len(a.downloads.tasks) - 1; i >= 0; i-- {
			d := a.downloads.tasks[i]
			if d.card.removed {
				continue
			}
			switch a.downloads.tab {
			case 1:
				if d.card.state != backgroundTaskRunning {
					continue
				}
			case 2:
				if d.card.state != backgroundTaskSucceeded {
					continue
				}
			case 3:
				if d.card.state != backgroundTaskFailed && d.card.state != backgroundTaskCanceled {
					continue
				}
			}
			shown = append(shown, d)
		}

		if len(shown) == 0 {
			a.empty(c, T("No matching download tasks"))
			return
		}

		ui.Column(c).Gap(10).Children(func() {
			for _, d := range shown {
				a.downloadDetailCard(c, d, p)
			}
		})
	})
}

func (a *app) downloadDetailCard(c *ui.Context, d *downloadTask, p palette) {
	task := d.card
	ui.Column(c.Key(fmt.Sprintf("download-detail-%d", task.id))).
		Padding(14, 16).Gap(8).Radius(10).Background(p.sidebar).Border(1, p.hairline).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Box(c).Size(36, 36).Radius(10).Background(p.hover).Center().Shrink(0).Children(func() {
				switch {
				case task.waiting:
					ui.Icon(c, iconQueue).Size(17, 17).TextColor(p.muted)
				case task.state == backgroundTaskRunning:
					ui.Spinner(c).FontSize(16).TextColor(p.accent).Label(task.status)
				case task.state == backgroundTaskSucceeded:
					ui.Icon(c, iconCheck).Size(17, 17).TextColor(p.accent)
				case task.state == backgroundTaskFailed:
					ui.Icon(c, iconX).Size(16, 16).TextColor(p.accent)
				default:
					ui.Icon(c, iconX).Size(16, 16).TextColor(p.muted)
				}
			})

			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, d.track.Name).FontSize(14).FontWeight(600).TextColor(p.text).SingleLine()
				if d.artists != "" {
					ui.Text(c, d.artists).FontSize(12.5).TextColor(p.muted).SingleLine()
				}
			})

			ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
				if task.state == backgroundTaskRunning && task.cancel != nil {
					ui.Button(c.Key("cancel"), T("Cancel")).
						Label(Tf("Cancel download: %s", d.track.Name)).
						FontSize(12).Padding(5, 10).OnClick(task.cancel)
				}
				if task.state == backgroundTaskSucceeded {
					ui.Button(c.Key("show"), T("Show in Folder")).
						Label(Tf("Show in Folder: %s", d.track.Name)).
						FontSize(12).Padding(5, 10).OnClick(func() {
						if d.path == "" {
							task.status = T("Downloaded file is missing")
							return
						}
						if _, err := os.Stat(d.path); err != nil {
							task.status = T("Downloaded file is missing")
							return
						}
						showDownloadedFile(d.path)
					})
					iconButton(c.Key("remove"), iconTrash, 15, p.muted, Tf("Remove download: %s", d.track.Name)).
						Tooltip(T("Remove")).OnClick(func() { a.removeBackgroundTask(task) })
				}
				if task.state == backgroundTaskFailed || task.state == backgroundTaskCanceled {
					if task.state == backgroundTaskFailed && task.failureAction != nil {
						disabled := task.failureAction.disabled != nil && task.failureAction.disabled()
						ui.Button(c.Key("guide"), task.failureAction.label).
							FontSize(12).Padding(5, 10).Disabled(disabled).OnClick(task.failureAction.run)
					}
					if task.retry != nil {
						ui.Button(c.Key("retry"), T("Retry")).
							Label(Tf("Retry download: %s", d.track.Name)).
							FontSize(12).Padding(5, 10).OnClick(task.retry)
					}
					iconButton(c.Key("remove"), iconTrash, 15, p.muted, Tf("Remove download: %s", d.track.Name)).
						Tooltip(T("Remove")).OnClick(func() { a.removeBackgroundTask(task) })
				}
			})
		})

		ui.Column(c).Padding(0, 0, 0, 48).Gap(6).Children(func() {
			var meta []string
			if src := a.downloadSourceName(d.track.Source); src != "" {
				meta = append(meta, src)
			}
			if d.quality != "" {
				meta = append(meta, d.quality)
			}
			if d.fileType != "" {
				meta = append(meta, strings.ToUpper(d.fileType))
			}
			switch {
			case d.path != "":
				meta = append(meta, filepath.Base(d.path), filepath.Dir(d.path))
			case d.dir != "":
				meta = append(meta, Tf("Folder: %s", d.dir))
			}
			if len(meta) > 0 {
				ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(p.muted).MaxLines(2)
			}

			statusColor := p.muted
			if task.state == backgroundTaskFailed {
				statusColor = p.accent
			}
			ui.Text(c, task.status).FontSize(12.5).TextColor(statusColor)

			if task.progress != nil && task.state == backgroundTaskRunning {
				ui.Progress(c, max(0, min(1, *task.progress))).Label(task.status)
			}
		})
	})
}

func (a *app) downloadPromptDialog(c *ui.Context) {
	p := a.pal
	d := &a.downloadPrompt
	width, _ := c.Size()
	panelWidth := float32(440)
	if width > 0 && width-32 < panelWidth {
		panelWidth = max(280, width-32)
	}
	ui.DialogBase(c.Key("download-prompt"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(panelWidth).Padding(24).Radius(18).Gap(14).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})

		title := T("Download Song")
		sub := ""
		if d.batch {
			title = T("Batch Download")
			if d.skipped > 0 {
				sub = Tf("%d songs ready to download (%d unavailable skipped)", len(d.items), d.skipped)
			} else {
				sub = Tf("%d songs ready to download", len(d.items))
			}
		} else if len(d.items) == 1 {
			sub = d.items[0].track.Name
			if artists := d.items[0].track.ArtistNames(); artists != "" {
				sub += " · " + artists
			}
		}

		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(700).TextColor(p.text)
			if sub != "" {
				ui.Text(c, sub).FontSize(13).TextColor(p.muted).SingleLine()
			}
		})

		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, T("Download Quality")).FontSize(13).TextColor(p.text).Grow(1)
			labels := make([]string, len(downloadQualityLevels))
			at := 0
			for i, q := range downloadQualityLevels {
				labels[i] = q.label
				if q.level == d.quality {
					at = i
				}
			}
			if i, ok := choice(c, "prompt-download-quality", T("Download Quality"), labels, at); ok {
				d.quality = downloadQualityLevels[i].level
			}
		})

		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, T("Save to Folder")).FontSize(12.5).FontWeight(600).TextColor(p.text)
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Box(c).Grow(1).MinWidth(0).Padding(8, 10).Radius(8).
					Background(p.sidebar).Border(1, p.hairline).Children(func() {
					ui.Text(c, d.folder).FontSize(12.5).TextColor(p.text).SingleLine()
				})
				if ui.Button(c.Key("browse-download-folder"), T("Browse…")).Clicked() {
					a.browseDownloadPromptFolder()
				}
			})
		})

		if !d.batch {
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, T("File Name")).FontSize(12.5).FontWeight(600).TextColor(p.text)
				placeholder := ""
				if len(d.items) == 1 {
					placeholder = songFileBaseName(&d.items[0].track)
				}
				in := ui.TextInput(c.Key("download-filename"), &d.fileName).
					Placeholder(placeholder).Label(T("File Name")).FontSize(13.5).AutoFocus()
				if in.Submitted() {
					a.confirmDownloadPrompt()
				}
			})
		}

		ui.Column(c).Gap(8).AlignItems(ui.Start).Children(func() {
			a.downloadPromptCheckbox(c, "save-default", T("Save folder and quality as default"), &d.saveAsDefault, p)
			a.downloadPromptCheckbox(c, "dont-ask-again", T("Don't ask again before downloading"), &d.dontAskAgain, p)
		})

		if d.errorText != "" {
			ui.Text(c.Key("download-prompt-error"), d.errorText).Role(ui.RoleAlert).FontSize(12.5).TextColor(p.accent)
		}

		ui.Row(c).Gap(10).Justify(ui.End).Margin(4, 0, 0, 0).Children(func() {
			if pillButton(c.Key("cancel"), nil, T("Cancel"), p.hover.Over(p.bg), p.text).Clicked() {
				d.open = false
			}
			if pillButton(c.Key("confirm"), nil, T("Download"), p.accent, p.onAccent).Clicked() {
				a.confirmDownloadPrompt()
			}
		})
	})
}

func (a *app) downloadPromptCheckbox(c *ui.Context, key, label string, checked *bool, p palette) {
	btn := ui.ButtonBase(c.Key(key)).Row().AlignItems(ui.Center).Justify(ui.Start).Gap(8).Padding(2, 0).
		FocusRing(false).Label(label)
	btn.Children(func() {
		box := ui.Box(c).Size(16, 16).Radius(4).Center().Shrink(0)
		if *checked {
			box.Background(p.accent).Children(func() {
				ui.Icon(c, iconCheck).Size(11, 11).TextColor(p.onAccent)
			})
		} else {
			box.Border(1.5, p.muted.Alpha(0.55))
		}
		ui.Text(c, label).FontSize(12.5).TextColor(p.muted)
	})
	if btn.Clicked() {
		*checked = !*checked
	}
}

package app

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

// playbackStatusText 只根据脱敏阶段和失败分类生成安全提示，绝不直接输出原始插件错误。
func (a *app) playbackStatusText(fb player.PlaybackFeedback) (text string, isErr bool) {
	if fb.Failure != "" {
		return a.playbackFailureTitle(fb.Failure), true
	}
	switch fb.Stage {
	case "resolving":
		if fb.Tried > 0 {
			return Tf("Finding source (%d tried)…", fb.Tried), false
		}
		return T("Finding source…"), false
	case "recovering":
		if fb.Tried > 0 {
			return Tf("Recovering playback (%d tried)…", fb.Tried), false
		}
		return T("Recovering playback…"), false
	case "remote":
		return T("Connecting to player…"), false
	case "buffering":
		return T("Buffering…"), false
	}
	if a.pl != nil && a.pl.Loading() {
		return T("Buffering…"), false
	}
	return "", false
}

func (a *app) playbackFailureTitle(category string) string {
	switch category {
	case "sign-in":
		return T("Sign in required")
	case "setup":
		return T("Setup required")
	case "source-unavailable":
		return T("Source unavailable")
	case "unavailable":
		return T("Song unavailable")
	case "timeout":
		return T("Playback timed out")
	case "remote-elsewhere":
		return T("Playing elsewhere")
	default:
		return T("Couldn't play this song")
	}
}

func (a *app) playbackFailureDetail(fb player.PlaybackFeedback) string {
	name := a.sourceDisplayName(fb.Source)
	switch fb.Failure {
	case "sign-in":
		if name != "" {
			return Tf("Sign in to %s to play this song.", name)
		}
		return T("Sign in to this source to play this song.")
	case "setup":
		if name != "" {
			return Tf("%s needs setup or permission before playing.", name)
		}
		return T("This source needs setup or permission before playing.")
	case "source-unavailable":
		return T("This song's source is disabled or missing. Enable it in Source Settings.")
	case "unavailable":
		return T("This song is unavailable on the current source.")
	case "timeout":
		return T("Playback timed out before audio was ready.")
	case "remote-elsewhere":
		return T("This source is playing something else on another device.")
	default:
		return T("Playback failed before audio could finish.")
	}
}

func (a *app) sourceDisplayName(id string) string {
	if id == "" {
		return ""
	}
	for _, lib := range a.allLibs {
		if lib.id == id {
			return lib.name()
		}
	}
	return sanitizeToken(id)
}

func (a *app) actualPlaybackSourceName(tr *music.Track, st *music.Stream, fb player.PlaybackFeedback) string {
	if st != nil && st.Platform != "" {
		return st.Platform
	}
	if fb.Source != "" {
		return a.sourceDisplayName(fb.Source)
	}
	if tr != nil && tr.Source != "" {
		return a.sourceDisplayName(tr.Source)
	}
	return ""
}

func (a *app) qualityDisplayName(quality string) string {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "":
		return T("Quality unknown")
	case "lossless", "flac", "wav":
		return T("Lossless")
	case "high":
		return T("High")
	case "standard":
		return T("Standard")
	default:
		return sanitizeToken(quality)
	}
}

func (a *app) streamQualityText(st *music.Stream) string {
	if st == nil {
		return ""
	}
	if st.Quality != "" {
		return a.qualityDisplayName(st.Quality)
	}
	// 未知音质必须保持明确未知，不能拿编码格式或请求音质代替。
	return T("Quality unknown")
}

// playbackOutcomeText 汇总成功交接后的实际来源和可确认音质，供播放页显示。
func (a *app) playbackOutcomeText(tr *music.Track, st *music.Stream, fb player.PlaybackFeedback) string {
	if st == nil {
		return ""
	}
	source := a.actualPlaybackSourceName(tr, st, fb)
	quality := a.streamQualityText(st)
	if source != "" && quality != "" {
		return source + " · " + quality
	}
	if quality != "" {
		return quality
	}
	return source
}

// playbackDiagnostics 严格按白名单字段输出脱敏诊断，禁止放入原始错误、URL、请求头或令牌。
func (a *app) playbackDiagnostics(fb player.PlaybackFeedback, tr *music.Track) string {
	sourceID := fb.Source
	if sourceID == "" && tr != nil {
		sourceID = tr.Source
	}
	stage := fb.Stage
	if stage == "" {
		switch {
		case fb.Failure != "":
			stage = "stopped"
		case a.pl != nil && a.pl.Playing():
			stage = "playing"
		default:
			stage = "idle"
		}
	}
	category := fb.Failure
	if category == "" {
		category = "none"
	}
	st := (*music.Stream)(nil)
	if a.pl != nil {
		st = a.pl.Stream()
	}
	actualQuality := "none"
	platform := "none"
	if st != nil {
		if st.Quality != "" {
			actualQuality = sanitizeToken(st.Quality)
		} else {
			actualQuality = "unknown"
		}
		if st.Platform != "" {
			platform = sanitizeToken(st.Platform)
		}
	}
	enabled := false
	mode := "unknown"
	accountState := "unsupported"
	processPerm := "none"
	lib := a.libOf(sourceID)
	if lib != nil {
		enabled = true
	} else {
		for _, candidate := range a.allLibs {
			if candidate.id == sourceID {
				lib = candidate
				break
			}
		}
	}
	if lib != nil {
		if lib.src.Info().Playback == music.PlaybackRemote {
			mode = "remote"
		} else if lib.can("stream") {
			mode = "stream"
		} else {
			mode = "browse"
		}
		if lib.can("account") {
			switch {
			case lib.accountUnknown():
				accountState = "unknown"
			case lib.account != nil:
				accountState = "signed-in"
			default:
				accountState = "signed-out"
			}
		}
		if g, ok := lib.src.(guarded); ok && len(g.Permissions().Process) > 0 {
			if g.Granted() {
				processPerm = "granted"
			} else {
				processPerm = "not-granted"
			}
		}
	}
	return fmt.Sprintf(
		"source=%s platform=%s mode=%s enabled=%t account=%s permission=%s stage=%s category=%s tried=%d requested_quality=%s actual_quality=%s",
		orNone(sanitizeToken(sourceID)),
		platform,
		mode,
		enabled,
		accountState,
		processPerm,
		sanitizeToken(stage),
		sanitizeToken(category),
		fb.Tried,
		orNone(sanitizeToken(a.settings.Quality)),
		actualQuality,
	)
}

func sanitizeToken(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func (a *app) syncPlaybackDetailsState(tr *music.Track, fb player.PlaybackFeedback) {
	if !a.playbackDetailsOpen {
		return
	}
	if tr == nil || (fb.Failure == "" && fb.Stage == "" && (a.pl == nil || a.pl.Stream() == nil)) {
		a.playbackDetailsOpen = false
		return
	}
	// 切歌或来源失效换代后立即关闭旧详情，防止过期操作继续展示。
	if tr.ID != a.playbackDetailsTrackID || tr.Source != a.playbackDetailsSource || (a.playbackDetailsFailure != "" && fb.Failure != a.playbackDetailsFailure) {
		a.playbackDetailsOpen = false
	}
}

func (a *app) playbackDetailsTrigger(c *ui.Context, kind string, tr *music.Track, fb player.PlaybackFeedback, fg, bg ui.Color, label string, compact, dark, centerAnchor bool) {
	a.syncPlaybackDetailsState(tr, fb)
	var btn ui.Element
	if compact {
		btn = ui.ButtonBase(c.Key("playback-details")).Height(24).PaddingX(8).Gap(5).Radius(999).
			AlignItems(ui.Center).TextColor(fg).Background(bg).Label(T("Playback details")).Tooltip(T("Playback details"))
		btn.Children(func() {
			if fb.Failure != "" {
				ui.Icon(c, iconX).Size(12, 12)
			} else {
				ui.Icon(c, iconQueue).Size(12, 12)
			}
			if label != "" {
				ui.Text(c, label).FontSize(11).FontWeight(600).SingleLine()
			}
		})
	} else {
		btn = ui.ButtonBase(c.Key("playback-details")).Height(16).PaddingX(6).Gap(4).Radius(4).
			AlignItems(ui.Center).TextColor(fg).Background(bg).Label(T("Playback details")).Tooltip(T("Playback details"))
		btn.Children(func() {
			if label != "" {
				ui.Text(c, label).FontSize(10).FontWeight(700).SingleLine()
			}
		})
	}
	if btn.Clicked() {
		if a.playbackDetailsOpen && a.playbackDetailsAnchor == kind {
			a.playbackDetailsOpen = false
		} else if tr != nil {
			a.playbackDetailsOpen = true
			a.playbackDetailsAnchor = kind
			a.playbackDetailsTrackID = tr.ID
			a.playbackDetailsSource = tr.Source
			a.playbackDetailsFailure = fb.Failure
		}
	}
	open := a.playbackDetailsOpen && a.playbackDetailsAnchor == kind
	wasOpen := open
	ui.PopoverBase(c.Key("playback-details-popover"), btn, &open, func(panel ui.Element) {
		a.playbackDetailsPopover(c, panel, btn, tr, fb, dark, centerAnchor)
	})
	if wasOpen && !open {
		a.playbackDetailsOpen = false
	}
}

func (a *app) playbackDetailsPopover(c *ui.Context, panel, anchor ui.Element, tr *music.Track, fb player.PlaybackFeedback, dark, centerAnchor bool) {
	th := a.sleepTheme(dark)
	pop, ink, muted, hover := th.pop, th.ink, th.muted, th.hover
	if centerAnchor {
		panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom)
	} else {
		panel.AttachTo(anchor, ui.AnchorTopStart, ui.AnchorBottomStart)
	}
	w, _ := c.Size()
	panelWidth := min(336, max(220, w-32))
	panel.Margin(0, 0, 10, 0).Width(panelWidth).Padding(14).Gap(10).Radius(14).
		Background(pop.fill).Border(0.5, pop.edge).
		Shadow(0, 8, 28, -4, ui.RGBA(0, 0, 0, 0.24)).
		TextColor(ink).Label(T("Playback details"))

	title := T("Playback details")
	if fb.Failure != "" {
		title = a.playbackFailureTitle(fb.Failure)
	}
	ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, title).FontSize(13.5).FontWeight(700).TextColor(ink).Grow(1).Shrink(1).MinWidth(0)
		if tr != nil {
			ui.Text(c, tr.Name).FontSize(11.5).TextColor(muted).SingleLine().Shrink(1).MinWidth(0)
		}
	})

	if fb.Failure != "" {
		ui.Text(c, a.playbackFailureDetail(fb)).FontSize(12).TextColor(muted).LineHeight(1.4)
	}

	st := (*music.Stream)(nil)
	if a.pl != nil {
		st = a.pl.Stream()
	}
	ui.Column(c).Padding(8, 10).Gap(4).Radius(8).Background(hover).Children(func() {
		srcName := a.actualPlaybackSourceName(tr, st, fb)
		if srcName != "" {
			ui.Text(c, Tf("Source: %s", srcName)).FontSize(11.5).TextColor(ink)
		}
		if stageText, _ := a.playbackStatusText(fb); stageText != "" {
			ui.Text(c, Tf("Status: %s", stageText)).FontSize(11.5).TextColor(muted)
		}
		ui.Text(c, Tf("Requested quality: %s", a.qualityDisplayName(a.settings.Quality))).FontSize(11.5).TextColor(muted)
		if st != nil {
			ui.Text(c, Tf("Actual quality: %s", a.streamQualityText(st))).FontSize(11.5).TextColor(ink)
		}
	})

	loading := a.pl != nil && a.pl.Loading()
	attempt := fb.Attempt
	lib := a.libOf(fb.Source)
	ui.Row(c).Wrap().Gap(6).Children(func() {
		if fb.Failure != "" && !loading && attempt > 0 {
			switch fb.Failure {
			case "sign-in":
				if lib != nil && lib.can("signIn") {
					ui.PrimaryButton(c.Key("sign-in"), T("Sign In")).FontSize(12).Padding(6, 10).OnClick(func() {
						a.playbackDetailsOpen = false
						a.promptSignIn(lib)
					})
				} else {
					ui.PrimaryButton(c.Key("open-settings"), T("Open Plugins")).FontSize(12).Padding(6, 10).OnClick(func() {
						a.playbackDetailsOpen = false
						a.openPlugins()
					})
				}
			case "setup", "source-unavailable":
				ui.PrimaryButton(c.Key("open-settings"), T("Open Plugins")).FontSize(12).Padding(6, 10).OnClick(func() {
					a.playbackDetailsOpen = false
					a.openPlugins()
				})
			default:
				if lib != nil {
					ui.PrimaryButton(c.Key("retry"), T("Retry playback")).FontSize(12).Padding(6, 10).OnClick(func() {
						a.playbackDetailsOpen = false
						a.pl.RetryCurrent(attempt)
					})
				}
			}
			// 授权/登录后也允许直接原地重试，同时隔离过期尝试号。
			if lib != nil && (fb.Failure == "sign-in" || fb.Failure == "setup") {
				ui.Button(c.Key("retry-secondary"), T("Retry playback")).FontSize(12).Padding(6, 10).OnClick(func() {
					a.playbackDetailsOpen = false
					a.pl.RetryCurrent(attempt)
				})
			}
		}
		ui.Button(c.Key("copy-diagnostics"), T("Copy playback diagnostics")).FontSize(12).Padding(6, 10).OnClick(func() {
			copyText(a.playbackDiagnostics(fb, tr))
			a.playbackDetailsOpen = false
			a.toast = T("Copied playback diagnostics")
		})
	})
	_ = time.Second
}

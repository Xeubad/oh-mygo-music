package app

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// aggregateLibrary keeps filtering and manual recording links on the existing Home route.
func (a *app) aggregateLibrary(c *ui.Context, lib *library, p palette, _ bool) {
	if lib == nil || lib.home == nil {
		return
	}
	src, supported := lib.src.(aggregateSource)
	if !supported {
		return
	}
	if a.aggregateViews == nil {
		a.aggregateViews = map[*library]*aggregateView{}
	}
	view := a.aggregateViews[lib]
	if view == nil || view.owner != nil && (view.owner != src || view.page != a.route) {
		view = &aggregateView{}
		a.aggregateViews[lib] = view
	}
	view.owner, view.page = src, a.route
	if view.writeLikedHome != lib.home {
		a.aggregateWriteSyncLiked(view, lib.home)
	}
	a.aggregateLibraryPoll(c, lib, src, view)
	a.aggregateWriteLoadCapabilities(lib, src, view)
	a.aggregateWriteLoadGenerations(lib, src, view)
	platforms, lists := aggregateFilterOptions(lib.home)
	ui.Column(c.Key(view)).OnUnmount(func() {
		// 页面消失后不再续订；旧请求即使稍后回执，也不能写入重新打开的视图。
		if a.aggregateViews[lib] == view {
			delete(a.aggregateViews, lib)
		}
	}).Gap(10).Children(func() {
		ui.Text(c, T("Oh MyGo Music Aggregate")).FontSize(22).FontWeight(700)
		ui.Text(c, T("Choose the account platform in plugin settings before refreshing. NetEase uses its own resolver key; QQ uses the general key.")).FontSize(12).TextColor(p.muted)
		ui.Row(c.Key("aggregate-controls")).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Select(c.Key("aggregate-kind"), &view.kind, []string{"All", "Liked", "Playlists"}).Label(T("Content")).Width(140).Disabled(view.busy)
			ui.Select(c.Key("aggregate-platform"), &view.platform, append([]string{"All"}, platforms...)).Label(T("Platform")).Width(120).Disabled(view.busy)
			ui.Select(c.Key("aggregate-list"), &view.list, append([]string{"All"}, lists...)).Label(T("Source List")).Width(180).Disabled(view.busy)
			ui.TextInput(c.Key("aggregate-query"), &view.query).Label(T("Filter Tracks")).Width(220).Disabled(view.busy)

		})

		ui.Row(c.Key("aggregate-refresh-controls")).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("refresh-cloud-lists"), T("Refresh Cloud Lists")).Disabled(view.busy).Clicked() {
				a.aggregateLibraryFetch(lib, src, true)
			}
			if ui.Button(c.Key("reload-local-cache"), T("Reload Cached Data")).Disabled(view.busy).Clicked() {
				a.aggregateLibraryReload(lib, src)
			}
			if accounts := aggregateWriteAccounts(view); len(accounts) > 0 {
				button := ui.Button(c.Key("create-playlist"), T("Create Playlist")).Disabled(view.busy)
				button.Menu(func(m *ui.Menu) {
					for _, account := range accounts {
						account := account
						label := aggregatePlatformName(account.platform) + " · " + account.accountID
						if m.Item(label).Chosen() {
							a.aggregateWriteStartCreate(src, view, account)
						}
					}
				})
			}
		})
		if view.busy {
			ui.Text(c, T("Updating aggregate data…")).FontSize(12).TextColor(p.muted)
		}
		if message := aggregateRefreshMessage(view.refresh.State); message != "" {
			ui.Text(c, message).FontSize(12).TextColor(p.muted).LineHeight(1.4)
		}
		if view.refreshErr != "" {
			ui.Text(c, view.refreshErr).FontSize(12).TextColor(p.accent).LineHeight(1.4)
		}
		if view.err != "" {
			ui.Text(c, view.err).FontSize(12).TextColor(p.accent).LineHeight(1.4)
		}
		if view.writeGenerationErr != "" {
			ui.Text(c, view.writeGenerationErr).FontSize(12).TextColor(p.accent).LineHeight(1.4)
		}
		if view.loadingLists {
			ui.Text(c, T("Loading cached lists…")).FontSize(12).TextColor(p.muted)
		}
		for _, cached := range view.lists {
			cached := cached
			if cached.ListID != "liked" {
				title := T("Include Cached List")
				if cached.Selected {
					title = T("Exclude Cached List")
				}
				if ui.Button(c.Key("cached-list-"+cached.Platform+cached.AccountID+cached.ListID), title+" "+cached.ListName).Disabled(view.busy || view.loadingLists).Clicked() {
					a.aggregateLibrarySelectList(lib, src, view, cached, !cached.Selected)
				}
				operations := view.writeCaps[cached.Platform]
				account := aggregateWriteAccount{platform: cached.Platform, accountID: cached.AccountID}
				if aggregateWriteGenerationKnown(view, account) &&
					(operations["playlistRename"] || operations["playlistDelete"]) {
					button := ui.Button(c.Key("playlist-actions-"+cached.Platform+cached.AccountID+cached.ListID), T("Playlist Actions")+" "+cached.ListName).Disabled(view.busy)
					button.Menu(func(m *ui.Menu) {
						if operations["playlistRename"] && m.Item(T("Rename")).Chosen() {
							a.aggregateWriteStartList(src, view, cached, "playlistRename")
						}
						if operations["playlistDelete"] && m.Item(T("Delete")).Chosen() {
							a.aggregateWriteStartList(src, view, cached, "playlistDelete")
						}
					})
				}
			}
			// 失败和刷新中不覆盖成功时间；喜欢列表也必须能看见自己的缓存新旧。
			label := cached.Platform + " · " + cached.ListName
			if cached.RefreshedAt > 0 {
				label += " · " + T("Last successful refresh") + ": " + time.UnixMilli(cached.RefreshedAt).Local().Format("2006-01-02 15:04")
			} else {
				label += " · " + T("No successful cache yet")
			}
			ui.Text(c, label).FontSize(11).TextColor(p.muted)
			if message := aggregateRefreshMessage(cached.RefreshState); message != "" {
				ui.Text(c, message).FontSize(11).TextColor(p.muted)
			}
		}
		if len(view.selected) > 0 {
			ui.Row(c.Key("aggregate-link-selection")).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, T("Select a second track to confirm the same recording.")).FontSize(12).TextColor(p.muted)
				if ui.Button(c.Key("cancel-link-selection"), T("Cancel Selection")).Disabled(view.busy).Clicked() {
					view.selected = nil
				}
			})
		}
		shown := 0
		for sectionIndex := range lib.home.Sections {
			section := &lib.home.Sections[sectionIndex]
			filtered := make([]int, 0, len(section.Tracks))
			for trackIndex, track := range section.Tracks {
				if aggregateTrackMatches(track, *view) {
					filtered = append(filtered, trackIndex)
				}
			}
			if len(filtered) == 0 {
				continue
			}
			shown += len(filtered)
			ui.Column(c.Key("aggregate-section-" + section.Title)).Gap(6).Children(func() {
				ui.Text(c, section.Title).FontSize(16).FontWeight(700).TextColor(p.text)
				for _, trackIndex := range filtered {
					track := &section.Tracks[trackIndex]
					ui.Column(c.Key([2]int{sectionIndex, trackIndex})).Gap(2).Children(func() {
						// 播放器收到原 section 的完整切片，筛选只隐藏行，不改队列候选及顺序。
						a.trackRowPlay(c, section.Tracks, trackIndex, false, "", func() {
							if view.busy {
								return
							}
							queue, err := aggregateQueueTracks(section.Tracks, view.sourceChoices)
							if err != nil {
								view.err = err.Error()
								return
							}
							view.err = ""
							a.pl.PlayTrack(queue, trackIndex)
						})
						ui.Row(c.Key("aggregate-source-" + track.ID)).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, aggregateTrackSources(track)).FontSize(11).TextColor(p.muted).SingleLine().Grow(1)
							if track.AggregateManual {
								ui.Text(c, T("Manually linked")).FontSize(11).TextColor(p.accent)
								for _, link := range track.AggregateLinks {
									left, right := link.Left, link.Right
									if ui.Button(c.Key("revoke-"+track.ID+left.Platform+left.TrackID+right.Platform+right.TrackID), T("Revoke Link")+" "+left.Platform+" + "+right.Platform).Disabled(view.busy).Clicked() {
										a.aggregateLibraryAction(lib, src, view, "revokeLink", map[string]any{"left": left, "right": right})
									}
								}
							}
							if view.busy {
								return
							}
							source := aggregateViewSource(c, view, track)
							if source.TrackID == "" {
								return
							}
							a.aggregateWriteButton(c, src, view, track, source)
							selected := len(view.selected) == 1 && sameAggregateSource(view.selected[0], source)
							if selected {
								ui.Text(c, T("Selected as first track")).FontSize(11).TextColor(p.accent)
								return
							}
							if len(view.selected) == 0 {
								if ui.Button(c.Key("select-link-"+track.ID), T("Select for Link")+" "+source.Platform).Clicked() {
									view.selected = []music.AggregateSource{source}
								}
								return
							}
							left := view.selected[0]
							if sameAggregateSource(left, source) {
								return
							}
							if ui.Button(c.Key("confirm-link-"+track.ID), T("Link Same Recording")+" "+left.Platform+" + "+source.Platform).Clicked() {
								a.aggregateLibraryAction(lib, src, view, "confirmLink", map[string]any{"left": left, "right": source})
							}
						})
					})
				}
			})
		}
		if shown == 0 {
			ui.Text(c, T("No tracks match these filters.")).FontSize(13).TextColor(p.muted)
		}
	})
}

func aggregateRefreshMessage(state string) string {
	switch state {
	case "refreshing":
		return T("Refreshing in the background; showing the last successful cache.")
	case "error":
		return T("Refresh failed; cached data has not been updated.")
	case "signIn":
		return T("Sign in again; showing the last successful cache.")
	case "success":
		return T("Cached data is up to date.")
	default:
		return ""
	}
}

func aggregateFilterOptions(page *music.Browse) (platforms, lists []string) {
	platformSet, listSet := map[string]bool{}, map[string]bool{}
	if page != nil {
		for _, section := range page.Sections {
			for _, track := range section.Tracks {
				for _, source := range track.AggregateSources {
					platformSet[source.Platform] = true
					name := source.ListName
					if name == "" {
						name = source.ListID
					}
					listSet[name] = true
				}
			}
		}
	}
	for value := range platformSet {
		platforms = append(platforms, value)
	}
	for value := range listSet {
		lists = append(lists, value)
	}
	return
}

func aggregateTrackMatches(track music.Track, view aggregateView) bool {
	if view.query != "" && !strings.Contains(strings.ToLower(track.Name+" "+track.ArtistNames()), strings.ToLower(view.query)) {
		return false
	}
	for _, source := range track.AggregateSources {
		if view.platform != "" && view.platform != "All" && source.Platform != view.platform {
			continue
		}
		if view.list != "" && view.list != "All" && source.ListName != view.list && source.ListID != view.list {
			continue
		}
		if view.kind == "Liked" && source.ListID != "liked" {
			continue
		}
		if view.kind == "Playlists" && source.ListID == "liked" {
			continue
		}
		return true
	}
	return false
}

func aggregateTrackSources(track *music.Track) string {
	parts := make([]string, 0, len(track.AggregateSources))
	for _, source := range track.AggregateSources {
		list := source.ListName
		if list == "" {
			list = source.ListID
		}
		parts = append(parts, source.Platform+" · "+list)
	}
	return strings.Join(parts, "  |  ")
}

func sameAggregateSource(left, right music.AggregateSource) bool {
	return left.Platform == right.Platform && left.AccountID == right.AccountID && left.TrackID == right.TrackID
}

func aggregateViewSource(c *ui.Context, view *aggregateView, track *music.Track) music.AggregateSource {
	sources := uniqueAggregateSources(track.AggregateSources)
	if len(sources) == 0 {
		return music.AggregateSource{}
	}
	if len(sources) == 1 {
		return sources[0]
	}
	if view.sourceChoices == nil {
		view.sourceChoices = map[string]string{}
	}
	identity := aggregateTrackIdentity(track)
	selected := view.sourceChoices[identity]
	if selected == "" || !containsAggregateSource(sources, selected) {
		selected = aggregateSourceKey(sources[0])
		// 默认显示与入队锚点一致，否则点选当前显示项会被误判为未作选择。
		var frozen struct {
			Version  int                   `json:"version"`
			Identity music.AggregateSource `json:"identity"`
		}
		if strings.HasPrefix(track.ID, "aggregate:") && json.Unmarshal([]byte(strings.TrimPrefix(track.ID, "aggregate:")), &frozen) == nil && frozen.Version == 1 && containsAggregateSource(sources, aggregateSourceKey(frozen.Identity)) {
			selected = aggregateSourceKey(frozen.Identity)
		}
	}
	labels := make([]string, 0, len(sources))
	byLabel := make(map[string]music.AggregateSource, len(sources))
	for _, source := range sources {
		label := aggregateIdentityLabel(source)
		labels = append(labels, label)
		byLabel[label] = source
	}
	current := aggregateIdentityLabel(byKeyAggregateSource(sources, selected))
	ui.Select(c.Key("aggregate-source-choice-"+identity), &current, labels).Label(T("Recording Source")).Width(260)
	if source, ok := byLabel[current]; ok {
		// 默认展示不算显式选择，不能用来源排列覆盖插件保存的稳定锚点。
		if current != aggregateIdentityLabel(byKeyAggregateSource(sources, selected)) {
			view.sourceChoices[identity] = aggregateSourceKey(source)
		}
		return source
	}
	return sources[0]
}

// 只改新队列副本的锚点；选择器不能把当前页面之外的来源注入冻结候选。
func aggregateQueueTracks(tracks []music.Track, choices map[string]string) ([]music.Track, error) {
	queue := append([]music.Track(nil), tracks...)
	for i := range queue {
		track := &queue[i]
		choice := choices[aggregateTrackIdentity(track)]
		if choice == "" {
			continue
		}
		var frozen struct {
			Version  int                     `json:"version"`
			Identity music.AggregateSource   `json:"identity"`
			Sources  []music.AggregateSource `json:"sources"`
		}
		if !strings.HasPrefix(track.ID, "aggregate:") || json.Unmarshal([]byte(strings.TrimPrefix(track.ID, "aggregate:")), &frozen) != nil || frozen.Version != 1 || !containsAggregateSource(frozen.Sources, choice) || !containsAggregateSource(track.AggregateSources, choice) {
			return nil, errors.New(T("The selected recording source is no longer available. Select the track again."))
		}
		frozen.Identity = byKeyAggregateSource(frozen.Sources, choice)
		encoded, err := json.Marshal(frozen)
		if err != nil {
			return nil, err
		}
		track.ID = "aggregate:" + string(encoded)
		// 选择版本只是入队意图，不能冒充该位置已经成功播放。
		track.PlaybackID = ""
	}
	return queue, nil
}

func uniqueAggregateSources(sources []music.AggregateSource) []music.AggregateSource {
	unique := make([]music.AggregateSource, 0, len(sources))
	for _, source := range sources {
		seen := false
		for _, existing := range unique {
			if sameAggregateSource(source, existing) {
				seen = true
				break
			}
		}
		if !seen {
			unique = append(unique, source)
		}
	}
	return unique
}

func aggregateSourceKey(source music.AggregateSource) string {
	encoded, _ := json.Marshal([3]string{source.Platform, source.AccountID, source.TrackID})
	return string(encoded)
}

func aggregateTrackIdentity(track *music.Track) string {
	if track.ID != "" {
		return track.ID
	}
	encoded, _ := json.Marshal(track.AggregateSources)
	return string(encoded)
}

func containsAggregateSource(sources []music.AggregateSource, key string) bool {
	for _, source := range sources {
		if aggregateSourceKey(source) == key {
			return true
		}
	}
	return false
}

func byKeyAggregateSource(sources []music.AggregateSource, key string) music.AggregateSource {
	for _, source := range sources {
		if aggregateSourceKey(source) == key {
			return source
		}
	}
	return music.AggregateSource{}
}

func aggregateIdentityLabel(source music.AggregateSource) string {
	// Quote user-supplied IDs so visible labels stay readable and separator-safe.
	account, _ := json.Marshal(source.AccountID)
	track, _ := json.Marshal(source.TrackID)
	return source.Platform + " · " + string(account) + " · " + string(track)
}

// 只由可见页的帧续订，无 ticker 或常驻 goroutine；状态接口本身不访问云端。
func (a *app) aggregateLibraryPoll(c *ui.Context, lib *library, src aggregateSource, view *aggregateView) {
	if view.pollUnsupported {
		return
	}
	now := c.Now()
	if !now.Before(view.nextPoll) {
		view.nextPoll = now.Add(3 * time.Second)
		if !view.polling && !view.busy && !view.loadingLists {
			a.aggregateLibraryReadRefresh(lib, src, view)
		}
	}
	c.After(view.nextPoll.Sub(now))
}

func (a *app) aggregateLibraryCurrent(lib *library, view *aggregateView) bool {
	return a.aggregateViews[lib] == view && (view.owner == nil ||
		(a.library == lib && any(lib.src) == any(view.owner) && a.route == view.page))
}

func (a *app) aggregateLibraryReadRefresh(lib *library, src aggregateSource, view *aggregateView) {
	view.polling = true
	loadLists := !view.listsLoaded
	view.loadingLists = loadLists
	version, revision := view.refreshVersion, view.revision
	go func() {
		raw, statusErr := src.Aggregate("refreshStatus", nil)
		var refresh aggregateRefresh
		unsupported := errors.Is(statusErr, music.ErrUnsupported)
		if statusErr == nil {
			// 老插件可能返回 [] / {ok:true}，不能把缺失版本当作不断变化的版本。
			statusText := strings.TrimSpace(string(raw))
			unsupported = len(statusText) == 0 || statusText[0] != '{'
			if !unsupported {
				statusErr = json.Unmarshal(raw, &refresh)
				unsupported = statusErr == nil && refresh.Version == nil
			}
		} else if strings.Contains(strings.ToLower(statusErr.Error()), "unknown action") || strings.Contains(strings.ToLower(statusErr.Error()), "unsupported") {
			unsupported = true
		}
		changed := statusErr == nil && !unsupported && (version == nil || *version != *refresh.Version)
		var home *music.Browse
		var lists []aggregateList
		var dataErr error
		if changed {
			home, dataErr = src.Home()
			if dataErr == nil && home == nil {
				dataErr = errors.New(T("Aggregate cache is unavailable; showing previous data."))
			}
		}
		if (loadLists || changed) && dataErr == nil {
			raw, dataErr = src.Aggregate("lists", nil)
			if dataErr == nil {
				dataErr = json.Unmarshal(raw, &lists)
			}
		}
		a.post(func() {
			if !a.aggregateLibraryCurrent(lib, view) {
				return
			}
			view.polling, view.loadingLists = false, false
			// 手动重载可能已提交更新结果；只结束旧探测，不再倒写其数据或状态。
			if view.revision != revision {
				return
			}
			view.pollUnsupported = unsupported
			view.refreshErr = ""
			if statusErr != nil && !unsupported {
				view.refreshErr = T("Unable to read refresh status; showing cached data: ") + statusErr.Error()
			} else if !unsupported {
				view.refresh = refresh
			}
			if dataErr != nil {
				view.refreshErr = T("Cached data has not been updated: ") + dataErr.Error()
				return
			}
			if loadLists || changed {
				view.lists, view.listsLoaded = lists, true
				aggregateWriteResetGenerations(view)
			}
			if changed {
				// 只替换浏览快照；播放器已经冻结的队列、位置与播放状态都不触碰。
				lib.home, view.refreshVersion = home, refresh.Version
			}
		})
	}()
}

func (a *app) aggregateLibraryLoadLists(lib *library, src aggregateSource, view *aggregateView) {
	view.loadingLists = true
	go func() {
		raw, err := src.Aggregate("lists", nil)
		var lists []aggregateList
		if err == nil {
			err = json.Unmarshal(raw, &lists)
		}
		a.post(func() {
			// 离页、换来源或重建视图后，旧任务不能再提交浏览快照。
			if !a.aggregateLibraryCurrent(lib, view) {
				return
			}
			view.loadingLists = false
			if err != nil {
				view.err = err.Error()
				return
			}
			view.lists, view.listsLoaded = lists, true
			aggregateWriteResetGenerations(view)
		})
	}()
}

func (a *app) aggregateLibrarySelectList(lib *library, src aggregateSource, view *aggregateView, cached aggregateList, selected bool) {
	if view.busy {
		return
	}
	view.busy, view.err = true, ""
	view.revision++
	go func() {
		_, err := src.Aggregate("selectList", map[string]any{"platform": cached.Platform, "accountID": cached.AccountID, "listID": cached.ListID, "selected": selected})
		var raw json.RawMessage
		var lists []aggregateList
		var home *music.Browse
		if err == nil {
			raw, err = src.Aggregate("lists", nil)
		}
		if err == nil {
			err = json.Unmarshal(raw, &lists)
		}
		if err == nil {
			home, err = src.Home()
		}
		a.post(func() {
			// 离页、换来源或重建视图后，旧任务不能再提交浏览快照。
			if !a.aggregateLibraryCurrent(lib, view) {
				return
			}
			view.busy = false
			if err != nil {
				view.err = err.Error()
				return
			}
			view.lists, view.listsLoaded, lib.home = lists, true, home
			aggregateWriteResetGenerations(view)
		})
	}()
}

func (a *app) aggregateLibraryReload(lib *library, src aggregateSource) {
	a.aggregateLibraryFetch(lib, src, false)
}

func (a *app) aggregateLibraryFetch(lib *library, src aggregateSource, cloud bool) {
	if a.aggregateViews == nil {
		a.aggregateViews = map[*library]*aggregateView{}
	}
	view := a.aggregateViews[lib]
	if view == nil {
		view = &aggregateView{}
		a.aggregateViews[lib] = view
	}
	if view.busy {
		return
	}
	view.busy, view.err = true, ""
	view.revision++
	go func() {
		var refreshErr error
		if cloud {
			// 每张列表独立提交，部分失败后仍读取已成功更新的本机快照。
			_, refreshErr = src.Aggregate("refresh", nil)
		}
		home, err := src.Home()
		var raw json.RawMessage
		var lists []aggregateList
		if err == nil {
			raw, err = src.Aggregate("lists", nil)
		}
		if err == nil {
			err = json.Unmarshal(raw, &lists)
		}
		a.post(func() {
			// 离页、换来源或重建视图后，旧任务不能再提交浏览快照。
			if !a.aggregateLibraryCurrent(lib, view) {
				return
			}
			view.busy = false
			if err != nil {
				view.err = err.Error()
				return
			}
			view.lists, view.listsLoaded, lib.home = lists, true, home
			aggregateWriteResetGenerations(view)
			if refreshErr != nil {
				view.err = refreshErr.Error()
			}
		})
	}()
}

func (a *app) aggregateLibraryAction(lib *library, src aggregateSource, view *aggregateView, action string, input map[string]any) {
	if view.busy {
		return
	}
	view.busy, view.err = true, ""
	view.revision++
	view.selected = nil
	go func() {
		_, err := src.Aggregate(action, input)
		var home *music.Browse
		if err == nil {
			home, err = src.Home()
		}
		a.post(func() {
			// 离页、换来源或重建视图后，旧任务不能再提交浏览快照。
			if !a.aggregateLibraryCurrent(lib, view) {
				return
			}
			view.busy = false
			if err != nil {
				view.err = err.Error()
				return
			}
			lib.home = home
		})
	}()
}

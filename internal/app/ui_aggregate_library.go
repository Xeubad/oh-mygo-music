package app

import (
	"encoding/json"
	"strings"

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
	if view == nil {
		view = &aggregateView{}
		a.aggregateViews[lib] = view
	}
	platforms, lists := aggregateFilterOptions(lib.home)
	if !view.listsLoaded && !view.loadingLists {
		a.aggregateLibraryLoadLists(lib, src, view)
	}
	ui.Column(c.Key("aggregate-library")).Gap(10).Children(func() {
		ui.Text(c, T("MyGo Music Aggregate")).FontSize(22).FontWeight(700)
		ui.Text(c, T("Cloud account access is not configured; local snapshots only.")).FontSize(12).TextColor(p.muted)
		ui.Row(c.Key("aggregate-controls")).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Select(c.Key("aggregate-kind"), &view.kind, []string{"All", "Liked", "Playlists"}).Label(T("Content")).Width(140).Disabled(view.busy)
			ui.Select(c.Key("aggregate-platform"), &view.platform, append([]string{"All"}, platforms...)).Label(T("Platform")).Width(120).Disabled(view.busy)
			ui.Select(c.Key("aggregate-list"), &view.list, append([]string{"All"}, lists...)).Label(T("Source List")).Width(180).Disabled(view.busy)
			ui.TextInput(c.Key("aggregate-query"), &view.query).Label(T("Filter Tracks")).Width(220).Disabled(view.busy)
			if ui.Button(c.Key("reload-local-cache"), T("Reload Cached Data")).Disabled(view.busy).Clicked() {
				a.aggregateLibraryReload(lib, src)
			}
		})
		if view.busy {
			ui.Text(c, T("Updating recording links…")).FontSize(12).TextColor(p.muted)
		}
		if view.err != "" {
			ui.Text(c, view.err).FontSize(12).TextColor(p.accent).LineHeight(1.4)
		}
		if view.loadingLists {
			ui.Text(c, T("Loading cached lists…")).FontSize(12).TextColor(p.muted)
		}
		for _, cached := range view.lists {
			if cached.ListID == "liked" {
				continue
			}
			label := cached.Platform + " · " + cached.ListName
			title := T("Include Cached List")
			if cached.Selected {
				title = T("Exclude Cached List")
			}
			if ui.Button(c.Key("cached-list-"+cached.Platform+cached.AccountID+cached.ListID), title+" "+cached.ListName).Disabled(view.busy || view.loadingLists).Clicked() {
				a.aggregateLibrarySelectList(lib, src, view, cached, !cached.Selected)
			}
			ui.Text(c, label).FontSize(11).TextColor(p.muted)
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
						a.trackRow(c, section.Tracks, trackIndex, false, "")
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
		view.sourceChoices[identity] = selected
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
		view.sourceChoices[identity] = aggregateSourceKey(source)
		return source
	}
	return sources[0]
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

func (a *app) aggregateLibraryLoadLists(lib *library, src aggregateSource, view *aggregateView) {
	view.loadingLists = true
	go func() {
		raw, err := src.Aggregate("lists", nil)
		var lists []aggregateList
		if err == nil {
			err = json.Unmarshal(raw, &lists)
		}
		a.post(func() {
			view.loadingLists = false
			if err != nil {
				view.err = err.Error()
				return
			}
			view.lists, view.listsLoaded = lists, true
		})
	}()
}

func (a *app) aggregateLibrarySelectList(lib *library, src aggregateSource, view *aggregateView, cached aggregateList, selected bool) {
	if view.busy {
		return
	}
	view.busy, view.err = true, ""
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
			view.busy = false
			if err != nil {
				view.err = err.Error()
				return
			}
			view.lists, view.listsLoaded, lib.home = lists, true, home
		})
	}()
}

func (a *app) aggregateLibraryReload(lib *library, src aggregateSource) {
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
	go func() {
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
			view.busy = false
			if err != nil {
				view.err = err.Error()
				return
			}
			view.lists, view.listsLoaded, lib.home = lists, true, home
		})
	}()
}

func (a *app) aggregateLibraryAction(lib *library, src aggregateSource, view *aggregateView, action string, input map[string]any) {
	if view.busy {
		return
	}
	view.busy, view.err = true, ""
	view.selected = nil
	go func() {
		_, err := src.Aggregate(action, input)
		var home *music.Browse
		if err == nil {
			home, err = lib.src.Home()
		}
		a.post(func() {
			view.busy = false
			if err != nil {
				view.err = err.Error()
				return
			}
			lib.home = home
		})
	}()
}

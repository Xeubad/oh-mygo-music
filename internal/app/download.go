package app

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo"

	"mygo-music/internal/local"
	"mygo-music/internal/music"
)

// pickDownloadFolder 询问目标目录，测试可替换该函数以跳过原生对话框。
var pickDownloadFolder = func(a *app) string {
	dir := a.downloadFolder()
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:            a.win,
		Title:             T("Choose Download Folder"),
		DefaultPath:       dir,
		ButtonLabel:       T("Download"),
		Directory:         true,
		Multiple:          false,
		CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// trackSelectState 只绑定在当前浏览路由上，切页后自动退出，避免跨列表误操作。
type trackSelectState struct {
	active   bool
	route    route
	selected map[string]bool
}

func (a *app) trackSelectionKey(tr *music.Track) string {
	src := tr.Source
	if src == "" {
		if lib := a.trackLib(tr); lib != nil {
			src = lib.id
		}
	}
	return src + "\x00" + tr.ID
}

func (a *app) trackSelectActive() bool {
	return a.trackSelect.active && a.trackSelect.route == a.route
}

func (a *app) enterTrackSelect() {
	a.trackSelect = trackSelectState{
		active:   true,
		route:    a.route,
		selected: make(map[string]bool),
	}
}

func (a *app) exitTrackSelect() {
	a.trackSelect = trackSelectState{}
}

func (a *app) isTrackSelected(tr *music.Track) bool {
	if !a.trackSelectActive() || a.trackSelect.selected == nil {
		return false
	}
	return a.trackSelect.selected[a.trackSelectionKey(tr)]
}

func (a *app) toggleTrackSelected(tr *music.Track) {
	if !a.canDownload(tr) {
		return
	}
	if !a.trackSelectActive() {
		a.enterTrackSelect()
	}
	key := a.trackSelectionKey(tr)
	if a.trackSelect.selected[key] {
		delete(a.trackSelect.selected, key)
	} else {
		a.trackSelect.selected[key] = true
	}
}

func (a *app) selectAllDownloadable(tracks []music.Track) {
	if !a.trackSelectActive() {
		a.enterTrackSelect()
	}
	for i := range tracks {
		if a.canDownload(&tracks[i]) {
			a.trackSelect.selected[a.trackSelectionKey(&tracks[i])] = true
		}
	}
}

func (a *app) clearTrackSelection() {
	if a.trackSelectActive() {
		a.trackSelect.selected = make(map[string]bool)
	}
}

func (a *app) selectedDownloadableTracks(tracks []music.Track) []music.Track {
	if !a.trackSelectActive() {
		return nil
	}
	var out []music.Track
	seen := make(map[string]bool)
	for i := range tracks {
		tr := &tracks[i]
		key := a.trackSelectionKey(tr)
		if a.trackSelect.selected[key] && !seen[key] && a.canDownload(tr) {
			seen[key] = true
			out = append(out, *tr)
		}
	}
	return out
}

func (a *app) countDownloadable(tracks []music.Track) int {
	n := 0
	for i := range tracks {
		if a.canDownload(&tracks[i]) {
			n++
		}
	}
	return n
}

// canDownload reports whether tr can be saved to a file of the listener's: a
// song its source streams for the app to play, rather than one already on disk
// or one that plays elsewhere.
func (a *app) canDownload(tr *music.Track) bool {
	lib := a.trackLib(tr)
	return lib != nil && lib.id != local.ID && lib.can("stream") &&
		lib.src.Info().Playback == music.PlaybackStream && !tr.Unavailable
}

type downloadPromptItem struct {
	track     music.Track
	lib       *library
	epoch     uint64
	accountID string
}

// downloadPromptState 保存单曲或批量下载前确认弹窗的临时状态。
type downloadPromptState struct {
	open          bool
	generation    uint64
	batch         bool
	items         []downloadPromptItem
	skipped       int
	folder        string
	fileName      string
	quality       string
	saveAsDefault bool
	dontAskAgain  bool
	errorText     string
}

func (a *app) collectDownloadPromptItems(tracks []music.Track) ([]downloadPromptItem, int) {
	var items []downloadPromptItem
	skipped := 0
	for _, tr := range tracks {
		lib := a.trackLib(&tr)
		if lib == nil || (tr.Source != "" && tr.Source != lib.id) || !a.canDownload(&tr) {
			skipped++
			continue
		}
		tr.Source = lib.id
		accountID := ""
		if lib.account != nil {
			accountID = lib.account.ID
		}
		items = append(items, downloadPromptItem{
			track:     tr,
			lib:       lib,
			epoch:     a.downloads.epochs[lib.id],
			accountID: accountID,
		})
	}
	return items, skipped
}

func (a *app) resolveAndEnsureDownloadDir(rawDir string, allowCreateDefault bool) (string, error) {
	rawDir = strings.TrimSpace(rawDir)
	if rawDir == "" {
		return "", errDownloadLocation
	}
	if allowCreateDefault && rawDir == defaultSystemDownloadDir() {
		if err := os.MkdirAll(rawDir, 0o755); err != nil {
			return "", err
		}
	}
	return downloadDir(rawDir)
}

func (a *app) openDownloadPrompt(items []downloadPromptItem, skipped int, batch bool) {
	fileName := ""
	if !batch && len(items) == 1 {
		fileName = songFileBaseName(&items[0].track)
	}
	a.downloadPrompt = downloadPromptState{
		open:       true,
		generation: a.downloadPrompt.generation + 1,
		batch:      batch,
		items:      items,
		skipped:    skipped,
		folder:     a.downloadFolder(),
		fileName:   fileName,
		quality:    a.downloadQualitySelection(),
	}
}

func (a *app) browseDownloadPromptFolder() {
	if !a.downloadPrompt.open || a.quitting {
		return
	}
	gen := a.downloadPrompt.generation
	go func() {
		picked := pickDownloadFolder(a)
		if picked == "" {
			return
		}
		dir, err := downloadDir(picked)
		a.post(func() {
			if a.quitting || !a.downloadPrompt.open || a.downloadPrompt.generation != gen {
				return
			}
			if err != nil {
				a.downloadPrompt.errorText = T("Check the download location")
				return
			}
			a.downloadPrompt.folder = dir
			a.downloadPrompt.errorText = ""
		})
	}()
}

func (a *app) confirmDownloadPrompt() {
	if !a.downloadPrompt.open || a.quitting {
		return
	}
	p := a.downloadPrompt
	dir, err := a.resolveAndEnsureDownloadDir(p.folder, strings.TrimSpace(p.folder) == defaultSystemDownloadDir())
	if err != nil {
		a.downloadPrompt.errorText = T("Check the download location")
		return
	}
	// 仅接收受支持的音质档位（含空字符串表示跟随播放音质）。
	qualitySelection := ""
	for _, q := range downloadQualityLevels {
		if q.level == p.quality {
			qualitySelection = q.level
			break
		}
	}
	quality := a.resolveDownloadQuality(qualitySelection)
	if p.saveAsDefault || p.dontAskAgain {
		if p.saveAsDefault {
			a.settings.DownloadDirectory = dir
			a.settings.DownloadQuality = qualitySelection
		}
		if p.dontAskAgain {
			a.settings.PromptDownload = false
		}
		a.changed()
	}
	customName := ""
	if !p.batch && strings.TrimSpace(p.fileName) != "" {
		customName = sanitizeDownloadBaseName(p.fileName)
	}
	a.downloadPrompt.open = false
	a.enqueuePromptItems(p.items, p.skipped, p.batch, dir, quality, customName)
}

func (a *app) enqueuePromptItems(items []downloadPromptItem, skipped int, batch bool, dir, quality, customName string) {
	added := 0
	changed := false
	duplicate := false
	for _, item := range items {
		// 跨弹窗或目录选择期间账号或音源可能变化，逐项核对当时的代次再入队。
		if a.libOf(item.lib.id) != item.lib || a.downloads.epochs[item.lib.id] != item.epoch {
			changed = true
			continue
		}
		name := ""
		if !batch {
			name = customName
		}
		switch a.addDirDownload(item.track, item.lib, quality, dir, name, item.accountID) {
		case batchAddQueued:
			added++
		case batchAddDuplicate:
			duplicate = true
		}
	}
	switch {
	case batch && added > 0 && skipped > 0:
		a.exitTrackSelect()
		a.toast = Tf("Queued %d songs (%d skipped)", added, skipped)
	case batch && added > 0:
		a.exitTrackSelect()
		a.toast = Tf("Queued %d songs for download", added)
	case changed && added == 0:
		a.toast = T("Download source changed; choose Download again")
	case duplicate && added == 0:
		a.toast = T("Another download is using this file")
	}
}

// download 按设置决定弹出确认框或直接按默认目录和音质开始单曲下载。
func (a *app) download(tr music.Track) {
	if a.quitting {
		return
	}
	items, _ := a.collectDownloadPromptItems([]music.Track{tr})
	if len(items) == 0 {
		a.toast = T("Download source is unavailable")
		return
	}
	if a.settings.PromptDownload {
		a.openDownloadPrompt(items, 0, false)
		return
	}
	dir, err := a.resolveAndEnsureDownloadDir(a.downloadFolder(), strings.TrimSpace(a.settings.DownloadDirectory) == "")
	if err != nil {
		a.toast = T("Check the download location")
		return
	}
	a.enqueuePromptItems(items, 0, false, dir, a.downloadQuality(), "")
}

// downloadBatch 过滤可下载曲目，按设置弹出确认框或直接使用默认目录与音质批量入队。
func (a *app) downloadBatch(tracks []music.Track) {
	if a.quitting {
		return
	}
	items, skipped := a.collectDownloadPromptItems(tracks)
	if len(items) == 0 {
		a.toast = T("No downloadable songs")
		return
	}
	if a.settings.PromptDownload {
		a.openDownloadPrompt(items, skipped, true)
		return
	}
	dir, err := a.resolveAndEnsureDownloadDir(a.downloadFolder(), strings.TrimSpace(a.settings.DownloadDirectory) == "")
	if err != nil {
		a.toast = T("Check the download location")
		return
	}
	a.enqueuePromptItems(items, skipped, true, dir, a.downloadQuality(), "")
}

// sanitizeDownloadBaseName 清洗用户或元数据提供的基础文件名，移除路径分隔符与非法字符。
func sanitizeDownloadBaseName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	// Most systems take 255 bytes, with room left for the extension.
	for len(name) > 200 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	// Windows drops a trailing dot or space, and a leading dot hides the file.
	name = strings.Trim(name, ". ")
	if name == "" {
		name = T("Song")
	}
	return name
}

func songFileBaseName(tr *music.Track) string {
	name := tr.Name
	if artists := tr.ArtistNames(); artists != "" {
		name = artists + " - " + name
	}
	return sanitizeDownloadBaseName(name)
}

// songFileName names the file of a song of type ext as its credits read,
// "Artists - Title.ext", with what no file name may hold taken out.
func songFileName(tr *music.Track, ext string) string {
	return songFileBaseName(tr) + "." + ext
}

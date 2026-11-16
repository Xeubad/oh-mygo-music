package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
	"mygo-music/internal/plugin"
)

func aggregateApp(t *testing.T) (*app, *plugin.Plugin, *ui.Tester, chan func()) {
	t.Helper()
	dir := os.Getenv("MM_AGGREGATE_PLUGIN")
	if dir == "" {
		dir = "F:/github/mygo-aggregate-plugin"
		if _, err := os.Stat(dir); err != nil {
			t.Skip("设置 MM_AGGREGATE_PLUGIN 后运行真实聚合界面验收")
		}
	}
	src, err := plugin.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)
	a := newTestApp()
	a.library = newLibrary(src)
	a.allLibs = []*library{a.library}
	a.libs = a.allLibs
	if !a.library.can("aggregate") {
		t.Fatalf("聚合插件能力未被探测：exports aggregate=%v", src.Can("aggregate"))
	}
	post := posts(a)
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, a.library, paletteFor(c.Theme())) }, 760, 1200)
	return a, src, tt, post
}

func aggregateFixture(t *testing.T) (*app, *plugin.Plugin, *ui.Tester, chan func()) {
	t.Helper()
	a, src, _, post := aggregateApp(t)
	var page func(*ui.Context)
	page = func(c *ui.Context) {
		if a.library.home == nil {
			browse, err := src.Home()
			if err != nil {
				t.Fatal(err)
			}
			a.library.home = browse
		}
		a.library.started = true
		a.library.homeAt = time.Now()
		a.route = route{page: pageHome, src: a.library.id}
		a.library = a.libOf(a.route.src)
		a.homePage(c)
	}
	return a, src, ui.NewTester(page, 760, 1200), post
}

// 聚合筛选仅隐藏页面行；点击一行时仍将该来源 section 的完整顺序交给播放器。
func TestAggregateFilteredTrackPlaysFullSectionQueue(t *testing.T) {
	a, src, tt, _ := aggregateFixture(t)
	for _, item := range []struct{ platform, account, id, name string }{{"wy", "a", "raw-01", "Wy title"}, {"tx", "b", "01", "Tx title"}} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{"platform": item.platform, "accountID": item.account, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": item.id, "name": item.name}}}); err != nil {
			t.Fatal(err)
		}
	}
	browse, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = browse
	tt.Frame()
	a.aggregateViews[a.library].platform = "wy"
	tt.Frame()
	if err := tt.Click("Wy title"); err != nil {
		t.Fatal(err)
	}
	queue := a.pl.Queue()
	want := browse.Sections[0].Tracks
	if len(queue) != len(want) {
		t.Fatalf("筛选后的行替换了播放器的完整来源队列: got %+v, want %+v", queue, want)
	}
	for i := range want {
		if queue[i].ID != want[i].ID {
			t.Fatalf("筛选后的行改变了来源队列顺序: got %+v, want %+v", queue, want)
		}
	}
}

func aggregateTarget(t *testing.T, src *plugin.Plugin) string {
	t.Helper()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Settings struct{ TargetQuality string }
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Settings.TargetQuality
}

// 设置界面的目标和最低音质一次联合提交，校验失败时插件保留上一份有效设置。
func TestAggregateSettingsSubmitAsOneValidatedChange(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	tt.Frame()
	runPost(t, post)
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit.loaded })
	tt.Frame()
	clickSettingRole(t, tt, ui.RolePopUpButton, "Target Quality")
	if err := tt.Click("320k"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	if got := aggregateTarget(t, src); got != "320k" {
		t.Fatalf("界面没有保存目标: %q", got)
	}
	clickSettingRole(t, tt, ui.RolePopUpButton, "Minimum Quality")
	if err := tt.Click("flac"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	if got := aggregateTarget(t, src); got != "320k" {
		t.Fatalf("非法修改破坏上一份设置: %q", got)
	}
	_ = a
}

// 标题相同不构成录音证据；用户在实际列表中明确选两首曲目后才能关联。
func TestAggregateUIConfirmsAndRevokesManualLink(t *testing.T) {
	a, src, tt, post := aggregateFixture(t)
	for _, item := range []struct{ platform, account, id string }{{"wy", "a", "raw-01"}, {"tx", "b", "01"}} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{"platform": item.platform, "accountID": item.account, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": item.id, "name": "Same title"}}}); err != nil {
			t.Fatal(err)
		}
	}
	browse, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = browse
	tt.Frame()
	if err := tt.Click("Select for Link wy"); err != nil {
		t.Fatalf("找不到第一个来源选择按钮；当前文本：%q；首页曲目：%+v；错误：%v", tt.Texts(), browse.Sections, err)
	}
	tt.Frame()
	if err := tt.Click("Link Same Recording wy + tx"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	view := a.aggregateViews[a.library]
	if view == nil || !view.busy {
		t.Fatal("关联操作没有显示处理中状态")
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || !page.Sections[0].Tracks[0].AggregateManual {
		t.Fatalf("界面操作没有持久化关联: %+v", page.Sections)
	}
	tt.Frame()
	if err := tt.Click("Revoke Link tx + wy"); err != nil {
		t.Fatalf("缺少撤销控件；UI文本=%q；Home=%+v；错误=%v", tt.Texts(), page.Sections, err)
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	page, err = src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("界面撤销没有拆分录音: %+v", page.Sections)
	}
}

// 多个列表中的同一平台曲目仍是一个身份，可与另一平台曲目明确建立人工关联。
func TestAggregateUICanLinkTracksPresentInMultipleLists(t *testing.T) {
	a, src, tt, post := aggregateFixture(t)
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": "wy-one", "name": "Repeated"}}},
		{"platform": "tx", "accountID": "b", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": "tx-one", "name": "Repeated"}}},
		{"platform": "tx", "accountID": "b", "listID": "playlist-one", "selected": true, "complete": true, "refreshedAt": 2, "tracks": []any{map[string]any{"id": "tx-one", "name": "Repeated"}}},
	} {
		if _, err := src.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = page
	tt.Frame()
	if err := tt.Click("Select for Link wy"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Link Same Recording wy + tx"); err != nil {
		t.Fatalf("同源多列表重复项隐藏了另一来源的关联操作：%q；%v", tt.Texts(), err)
	}
	view := a.aggregateViews[a.library]
	until(t, post, func() bool { return !view.busy })
	joined, err := src.Home()
	if err != nil || len(joined.Sections[0].Tracks) != 1 || !joined.Sections[0].Tracks[0].AggregateManual {
		t.Fatalf("按唯一原始曲目身份关联失败：%+v %v", joined.Sections, err)
	}
}

// 已合并曲目含多个来源时，关联操作仍须由用户明确选择具体原始身份。
func TestAggregateUISelectsExactSourceFromMergedTrack(t *testing.T) {
	a, src, tt, post := aggregateFixture(t)
	kg := music.AggregateSource{Platform: "kg", AccountID: "account · a", TrackID: "b · c"}
	wy := music.AggregateSource{Platform: "kg", AccountID: "account", TrackID: "a · b · c"}
	tx := music.AggregateSource{Platform: "tx", AccountID: "account-tx", TrackID: "tx-version"}
	for _, source := range []music.AggregateSource{kg, wy, tx} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "name": source.TrackID}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.Aggregate("confirmLink", map[string]any{"left": kg, "right": wy}); err != nil {
		t.Fatal(err)
	}
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = page
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RolePopUpButton, "Recording Source"); !ok {
		t.Fatalf("合并曲目没有具体来源选择器：%q", tt.Texts())
	}
	clickSettingRole(t, tt, ui.RolePopUpButton, "Recording Source")
	if err := tt.Click(`kg · "account" · "a · b · c"`); err != nil {
		t.Fatalf("不能选择第二个原始来源：%v", err)
	}
	if err := tt.Click("Select for Link kg"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Link Same Recording kg + tx"); err != nil {
		t.Fatalf("不能把明确选择的来源与另一曲目关联：%q；%v", tt.Texts(), err)
	}
	view := a.aggregateViews[a.library]
	until(t, post, func() bool { return !view.busy })
	linked, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(linked.Sections[0].Tracks) != 1 {
		t.Fatalf("具体来源关联后未合入同录音组：%+v", linked.Sections)
	}
	var exactLink bool
	for _, link := range linked.Sections[0].Tracks[0].AggregateLinks {
		if sameAggregateSource(link.Left, wy) && sameAggregateSource(link.Right, tx) || sameAggregateSource(link.Right, wy) && sameAggregateSource(link.Left, tx) {
			exactLink = true
		}
	}
	if !exactLink {
		t.Fatalf("关联使用了未选中的另一来源：%+v", linked.Sections[0].Tracks[0].AggregateLinks)
	}
}

// 已声明的高级目标可从现有设置保存；最低音质仍采用可核验的普通档位。
func TestAggregateSettingsCanChooseDeclaredAdvancedTarget(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	if _, err := src.Aggregate("commitSnapshot", map[string]any{
		"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 1,
		"tracks": []any{map[string]any{"id": "one", "qualities": []string{"immersive", "flac", "128k"}}},
	}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit != nil && edit.loaded })
	tt.Frame()
	if err := tt.Click("Reload aggregate status"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	clickSettingRole(t, tt, ui.RolePopUpButton, "Target Quality")
	if err := tt.Click("immersive"); err != nil {
		t.Fatalf("已声明的高级目标不能选择：%v", err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	if target := aggregateTarget(t, src); target != "immersive" {
		t.Fatalf("界面未保存选定高级目标：%q，错误：%s", target, edit.err)
	}
}

// 候选平台顺序可由用户在现有设置行中调整并持久化。
func TestAggregateSettingsCanReorderPlatforms(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit != nil && edit.loaded })
	tt.Frame()
	if err := tt.Click("Move Up tx"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Settings struct{ PlatformOrder []string }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Settings.PlatformOrder) != 6 || got.Settings.PlatformOrder[0] != "kg" || got.Settings.PlatformOrder[3] != "tx" {
		t.Fatalf("平台排序没有按设置保存: %v", got.Settings.PlatformOrder)
	}
}

// 本机完整缓存歌单可纳入或移出聚合，喜欢快照始终保留；未读取的列表不可选。
func TestAggregateCachedPlaylistCanBeSelectedFromHome(t *testing.T) {
	a, src, _, post := aggregateApp(t)
	emptyHome, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = emptyHome
	a.library.started = true
	a.route = route{page: pageHome, src: a.library.id}
	a.library = a.libOf(a.route.src)
	tt := ui.NewTester(func(c *ui.Context) { a.homePage(c) }, 760, 1200)
	tt.Frame()
	var view *aggregateView
	until(t, post, func() bool {
		view = a.aggregateViews[a.library]
		return view != nil && !view.loadingLists && view.listsLoaded
	})
	if len(view.lists) != 0 {
		t.Fatalf("空测试起点意外有缓存列表：%+v", view.lists)
	}
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "account-a", "listID": "liked", "listName": "Liked", "complete": true, "refreshedAt": 100, "selected": true, "tracks": []any{map[string]any{"id": "song-1", "name": "Liked song"}}},
		{"platform": "wy", "accountID": "account-a", "listID": "playlist-1", "listName": "Road songs", "complete": true, "refreshedAt": 200, "tracks": []any{map[string]any{"id": "song-2", "name": "Road song"}}},
	} {
		if _, err := src.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("Reload Cached Data"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	if len(view.lists) != 2 {
		t.Fatalf("重载后没有更新缓存列表目录：%+v", view.lists)
	}
	if err := tt.Click("Include Cached List Road songs"); err != nil {
		t.Fatalf("完整的未选歌单不可管理：%q；%v", tt.Texts(), err)
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("纳入歌单后未出现曲目：%+v", page.Sections)
	}
	if err := tt.Click("Exclude Cached List Road songs"); err != nil {
		t.Fatalf("已纳入歌单不可移出：%q；%v", tt.Texts(), err)
	}
	until(t, post, func() bool { return !view.busy })
	page, err = src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].Name != "Liked song" {
		t.Fatalf("移出歌单损坏了喜欢快照：%+v", page.Sections)
	}
	if _, err := src.Aggregate("selectList", map[string]any{"platform": "wy", "accountID": "account-a", "listID": "missing", "selected": true}); err == nil {
		t.Fatal("尚未完整缓存的列表可以纳入")
	}
}

// 安全保存后，重载设置页只显示凭据状态，不回填可复制的秘密。
func TestAggregateSettingsNeverPrefillSavedResolverKey(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	const secret = "synthetic-key-that-must-not-appear"
	// 测试只用当前插件 VM 中的临时值，不向开发机的系统凭据库写入。
	if _, err := src.Aggregate("setResolverKey", map[string]any{"value": secret, "temporary": true}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit != nil && edit.loaded })
	tt.Frame()
	n, ok := tt.AccessByRole(ui.RoleTextField, "Resolver Key")
	if !ok {
		t.Fatal("缺少解析密钥输入")
	}
	if n.Value != "" || strings.Contains(n.Value, secret) {
		t.Fatalf("设置页回填了系统凭据: %q", n.Value)
	}
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("状态接口泄露解析密钥")
	}
}

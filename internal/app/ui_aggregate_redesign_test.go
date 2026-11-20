package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// 添加链接只在明确展开时持有草稿，取消后不能把旧链接带入下一次添加。
func TestAggregateInlineSourceCanCancel(t *testing.T) {
	_, tt, _ := customSourcesSettings(t)
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Source Link"); ok {
		t.Fatal("链接输入不应常驻首屏")
	}
	if err := tt.Click("Add Source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Source Link")
	tt.Type("https://fixture.invalid/source?key=synthetic-only")
	if err := tt.Click("Cancel Source Input"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Add Source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	button, ok := tt.AccessByRole(ui.RoleButton, "Confirm Add Source")
	if !ok || !button.Disabled {
		t.Fatal("取消添加后复用了敏感草稿")
	}
}

type platformSettingsFixture struct {
	*customSourcesUIFixture
	accountsMu     sync.Mutex
	accounts       map[string]*music.Account
	in, out        []string
	configureError bool
	configured     map[string]any
}

func (s *platformSettingsFixture) AccountPlatform(p string) (*music.Account, error) {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	return s.accounts[p], nil
}
func (s *platformSettingsFixture) SignInPlatform(p string) (*music.Account, error) {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	s.in = append(s.in, p)
	s.accounts[p] = &music.Account{ID: p, Name: "Signed " + p}
	return s.accounts[p], nil
}
func (s *platformSettingsFixture) SignOutPlatform(p string) error {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	s.out = append(s.out, p)
	delete(s.accounts, p)
	return nil
}
func (s *platformSettingsFixture) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	if action == "configure" {
		if s.configureError {
			return nil, errors.New("fixture rejected settings")
		}
		s.configured = input
		return json.RawMessage(`{"ok":true}`), nil
	}
	raw, err := s.customSourcesUIFixture.Aggregate(action, input)
	if action == "status" && err == nil {
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		data["accountPlatforms"] = []string{"wy", "tx", "qs"}
		return json.Marshal(data)
	}
	return raw, err
}
func platformSettings(t *testing.T) (*app, *platformSettingsFixture, *ui.Tester, chan func()) {
	t.Helper()
	a := newTestApp()
	post := posts(a)
	src := &platformSettingsFixture{customSourcesUIFixture: &customSourcesUIFixture{fakeSource: fake(a)}, accounts: map[string]*music.Account{"wy": {ID: "wy", Name: "网易测试账号"}, "tx": {ID: "tx", Name: "QQ测试账号"}}}
	a.library.src = src
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, a.library, paletteFor(c.Theme())) }, 900, 1200)
	tt.Frame()
	until(t, post, func() bool { e := a.aggregateEdits[a.library]; return e != nil && e.loaded && !a.library.accountBusy })
	tt.Frame()
	e := a.aggregateEdits[a.library]
	until(t, post, func() bool {
		for _, p := range e.accountPlatforms {
			if e.accounts[p] == nil || e.accounts[p].busy {
				return false
			}
		}
		return true
	})
	tt.Frame()
	t.Cleanup(func() {
		until(t, post, func() bool {
			for _, r := range e.accounts {
				if r.busy {
					return false
				}
			}
			return !a.library.accountBusy && !e.busy && !a.signIn.waiting
		})
	})
	return a, src, tt, post
}

// 账号行退出和登录都传明确平台，其他行与默认个人账号不被回执覆盖。
func TestAggregatePlatformRowsRouteIndependently(t *testing.T) {
	a, src, tt, post := platformSettings(t)
	e := a.aggregateEdits[a.library]
	if err := tt.Click("Sign Out QQ Music"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !e.accounts["tx"].busy && !a.library.accountBusy })
	tt.Frame()
	if e.accounts["wy"].account == nil || e.accounts["tx"].account != nil || len(src.out) != 1 || src.out[0] != "tx" {
		t.Fatal("退出了错误平台")
	}
	if err := tt.Click("Sign In Qishui Music"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.signIn.platform != "qs" {
		t.Fatal("登录目标不明确")
	}
	a.startSignIn()
	until(t, post, func() bool { return !a.signIn.waiting })
	until(t, post, func() bool { return !e.accounts["qs"].busy && !a.library.accountBusy })
	tt.Frame()
	if len(src.in) != 1 || src.in[0] != "qs" || e.accounts["qs"].account == nil || a.library.account != nil {
		t.Fatal("平台登录污染默认账号")
	}
	if src.Setting("accountPlatform") != "" || src.Setting("catalogPlatform") != "" {
		t.Fatal("操作账号偷偷修改了全局平台")
	}
}

// 窄窗换行和反复缩放后，音源菜单、开关及账号按钮仍操作各自的稳定身份。
func TestAggregateCompactSettingsActionsAfterResize(t *testing.T) {
	a, src, _, post := platformSettings(t)
	src.mu.Lock()
	src.active = true
	src.items = []map[string]any{
		{"id": "one", "name": "First", "platforms": []string{"wy"}, "enabled": true, "state": "ready"},
		{"id": "two", "name": "Second", "platforms": []string{"tx", "qs"}, "enabled": true, "state": "ready"},
	}
	src.mu.Unlock()
	e := a.aggregateEdits[a.library]
	a.aggregateCommand(a.library, e, "status", nil)
	until(t, post, func() bool { return !e.busy })
	a.settingsPage = "plugins"
	tt := ui.NewTester(a.settingsView, 1200, 760)
	settled(tt)
	tt.SetSize(640, 760)
	settled(tt)
	if err := tt.Click("Source Actions Second"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Move Source Up"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !e.busy })
	settled(tt)
	clickSettingRole(t, tt, ui.RoleSwitch, "Enable Source Second")
	until(t, post, func() bool { return !e.busy })
	if len(e.sources.Items) != 2 || e.sources.Items[0].ID != "two" || e.sources.Items[0].Enabled || !e.sources.Items[1].Enabled {
		t.Fatal("窄屏操作没有只移动并停用目标音源")
	}
	for _, width := range []int{780, 640} {
		tt.SetSize(width, 760)
		settled(tt)
	}
	if err := tt.Click("Sign Out QQ Music"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !e.accounts["tx"].busy && !a.library.accountBusy })
	if e.accounts["wy"].account == nil || e.accounts["tx"].account != nil || len(src.out) != 1 || src.out[0] != "tx" {
		t.Fatal("缩放后退出了错误的平台账号")
	}
	settled(tt)
	tt.Scroll(600, 600, 0, 560)
	settled(tt)
	if err := tt.Click("Move Up QQ Music"); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !e.busy })
	if src.configured == nil || src.configured["platformOrder"].([]string)[0] != "tx" {
		t.Fatal("窄屏排序操作没有保存到正确的平台顺序")
	}
}

// 保存失败保留联合草稿，取消恢复原设置，不以重载状态覆盖用户输入。
func TestAggregatePlaybackDraftFailureAndDiscard(t *testing.T) {
	a, src, tt, post := platformSettings(t)
	e := a.aggregateEdits[a.library]
	clickSettingRole(t, tt, ui.RolePopUpButton, "Target Quality")
	if err := tt.Click("320k"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	src.configureError = true
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !e.busy })
	tt.Frame()
	if e.target != "320k" || !e.dirty() || e.err == "" {
		t.Fatal("保存失败丢失草稿")
	}
	if err := tt.Click("Discard changes"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.target != "flac" || e.dirty() || e.err != "" || src.configured != nil {
		t.Fatal("取消没有恢复原设置")
	}
}

// 搜索默认值与真正路由一致，选择搜索平台不切换账号操作平台。
func TestAggregateSearchUsesEffectiveDefault(t *testing.T) {
	a, src, tt, post := platformSettings(t)
	n, ok := tt.AccessByRole(ui.RolePopUpButton, "Search Platform")
	if !ok || n.Value != "NetEase Music" {
		t.Fatalf("生效搜索平台未显示：%+v", n)
	}
	clickSettingRole(t, tt, ui.RolePopUpButton, "Search Platform")
	if err := tt.Click("QQ Music"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return len(a.settingPending) == 0 && !a.library.accountBusy })
	tt.Frame()
	if src.Setting("catalogPlatform") != "tx" || src.Setting("accountPlatform") != "" {
		t.Fatal("搜索变更切换了账号")
	}
}

// 独立账号行曾隐藏个人内容选择；切换后歌单与喜欢须读取所选平台，不能连带切换搜索。
func TestAggregatePersonalContentPlatformRoutesReads(t *testing.T) {
	a, src, tt, post := platformSettings(t)
	// 模拟插件既有按 accountPlatform 读取个人内容的契约，不把显式账号行当成内容选择。
	selected := func() string {
		if value := src.Setting("accountPlatform"); value != "" {
			return value
		}
		return "wy"
	}
	src.playlists = func() ([]music.Playlist, error) {
		return []music.Playlist{{ID: selected() + ":list", Mine: true}}, nil
	}
	src.liked = func(int) (*music.Page, error) {
		return &music.Page{Tracks: []music.Track{{ID: selected() + ":liked"}}}, nil
	}
	for _, choice := range []struct{ name, platform string }{{"QQ Music", "tx"}, {"Qishui Music", "qs"}} {
		clickSettingRole(t, tt, ui.RolePopUpButton, "Personal Content Platform")
		if err := tt.Click(choice.name); err != nil {
			t.Fatal(err)
		}
		until(t, post, func() bool {
			return len(a.settingPending) == 0 && !a.library.accountBusy && len(a.library.mine) == 1 && a.library.mine[0].ID == choice.platform+":list"
		})
		tt.Frame()
		liked, err := src.Liked(0)
		if err != nil || len(liked.Tracks) != 1 || liked.Tracks[0].ID != choice.platform+":liked" {
			t.Fatalf("喜欢没有读取所选平台：%+v, %v", liked, err)
		}
		if src.Setting("accountPlatform") != choice.platform || src.Setting("catalogPlatform") != "" || len(src.in) != 0 || len(src.out) != 0 {
			t.Fatal("个人内容选择修改了搜索或账号登录状态")
		}
	}
}

// 真实原生控件绘制的本地夹具截图，不访问真实账号或网络。
func TestAggregateSettingsRedesignFrames(t *testing.T) {
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip("设置 MM_FRAMES 保存原生设置截图")
	}
	a, src, _, post := platformSettings(t)
	src.items = []map[string]any{{"id": "one", "name": "日常音源", "platforms": []string{"wy", "tx"}, "enabled": true, "state": "ready"}, {"id": "two", "name": "备用音源", "platforms": []string{"tx", "qs"}, "enabled": true, "state": "blocked", "reason": "服务凭据已过期，请替换链接或重试。"}}
	src.active = true
	a.aggregateCommand(a.library, a.aggregateEdits[a.library], "status", nil)
	until(t, post, func() bool { return !a.aggregateEdits[a.library].busy })
	lang := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(lang)
	src.info.Name = "聚合音乐"
	src.info.Version = "0.1.0-dev"
	a.settingsPage = "plugins"
	tt := ui.NewTester(a.settingsView, 1200, 1000)
	settled(tt)
	saveFrame(t, dir, "aggregate-settings-redesign.png", tt.Image())
	if err := tt.Click(T("Add Source")); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	saveFrame(t, dir, "aggregate-settings-add.png", tt.Image())
	if err := tt.Click(T("Cancel Source Input")); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{780, 640} {
		for _, dark := range []bool{false, true} {
			compact := ui.NewTester(a.settingsView, width, 760)
			compact.SetDark(dark)
			settled(compact)
			saveFrame(t, dir, fmt.Sprintf("aggregate-settings-%d-dark-%v.png", width, dark), compact.Image())
			if width == 640 {
				compact.Scroll(600, 600, 0, 560)
				settled(compact)
				saveFrame(t, dir, fmt.Sprintf("aggregate-settings-640-lower-dark-%v.png", dark), compact.Image())
			}
		}
	}
	// 长文字另存一组，保留短名称截图用于与修复前直接比较。
	e := a.aggregateEdits[a.library]
	e.sources.Items[0].Name = "家庭共享 — 客厅音箱专用的日常高音质播放音源"
	e.sources.Items[0].Platforms = []string{"wy", "tx", "qs", "kg", "kw", "mg"}
	e.accounts["wy"].account = &music.Account{ID: "wy", Name: "周末家庭共享音乐账号与客厅播放清单"}
	for _, dark := range []bool{false, true} {
		compact := ui.NewTester(a.settingsView, 640, 760)
		compact.SetDark(dark)
		settled(compact)
		saveFrame(t, dir, fmt.Sprintf("aggregate-settings-640-long-dark-%v.png", dark), compact.Image())
	}
}

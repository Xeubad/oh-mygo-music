package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 汽水密钥只提交到 qs 范围，提交后清空密码输入且保留其他服务草稿，公开状态不返回密钥。
func TestAggregateSettingsQishuiKeyStaysSeparate(t *testing.T) {
	a, src, _, post := aggregateApp(t)
	before, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 外层真实页面负责滚动；局部画布加高以覆盖新增的汽水字段行。
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, a.library, paletteFor(c.Theme())) }, 760, 1700)
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit.loaded })
	tt.Frame()
	for _, draft := range []struct{ label, value string }{
		{"Resolver Key", "draft-general-key"},
		{"NetEase Resolver Key", "draft-wy-key"},
		{"QQ Resolver Key", "draft-qq-key"},
		{"Qishui Resolver Key", "fixture-qs-ui-key"},
	} {
		clickSettingRole(t, tt, ui.RoleTextField, draft.label)
		tt.Type(draft.value)
	}
	field, ok := tt.AccessByRole(ui.RoleTextField, "Qishui Resolver Key")
	if !ok || !field.Password {
		t.Fatal("汽水密钥未使用密码控件")
	}
	if err := tt.Click("Use Qishui Key Once"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	field, ok = tt.AccessByRole(ui.RoleTextField, "Qishui Resolver Key")
	if !ok || !field.Password || field.Value != "" {
		t.Fatal("汽水密钥提交后未清空或不是密码控件")
	}
	for _, label := range []string{"Use Once", "Use NetEase Key Once", "Use QQ Key Once"} {
		button, found := tt.AccessByRole(ui.RoleButton, label)
		if !found || button.Disabled {
			t.Fatal("汽水操作清除了其他服务草稿")
		}
	}
	for _, label := range []string{"Save Qishui Key Securely", "Use Qishui Key Once"} {
		button, found := tt.AccessByRole(ui.RoleButton, label)
		if !found || !button.Disabled {
			t.Fatal("汽水密钥提交后仍留有可再次发送的输入")
		}
	}
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		ResolverCredential  struct{ State string }
		ResolverCredentials map[string]struct{ State string }
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	baseline := state
	baseline.ResolverCredentials = nil // JSON 解码会复用 map，基线不能倒写刚读到的状态。
	if err := json.Unmarshal(before, &baseline); err != nil {
		t.Fatal(err)
	}
	// 系统凭据跨临时数据目录共享；保护原状态，不假定本机其他范围从未配置。
	if state.ResolverCredentials["qs"].State != "temporary" || state.ResolverCredentials["wy"].State != baseline.ResolverCredentials["wy"].State || state.ResolverCredentials["tx"].State != baseline.ResolverCredentials["tx"].State || state.ResolverCredential.State != baseline.ResolverCredential.State {
		t.Fatal("汽水密钥没有独立提交或修改了其他服务状态")
	}
	if strings.Contains(string(raw), "fixture-qs-ui-key") {
		t.Fatal("公开状态泄露汽水密钥")
	}
	if dir := os.Getenv("MM_FRAMES"); dir != "" {
		saveFrame(t, dir, "aggregate-qishui-resolver.png", tt.Image())
	}
}

// QQ 密钥只提交到 tx 范围，不能清除网易或历史通用服务尚未提交的草稿。
func TestAggregateSettingsQQKeyStaysSeparate(t *testing.T) {
	a, src, _, post := aggregateApp(t)
	// 此处单测设置内容，真实页面的滚动容器在外层；加高局部画布以完成底部控件交互。
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, a.library, paletteFor(c.Theme())) }, 760, 1500)
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit.loaded })
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Resolver Key")
	tt.Type("draft-general-key")
	clickSettingRole(t, tt, ui.RoleTextField, "NetEase Resolver Key")
	tt.Type("draft-wy-key")
	clickSettingRole(t, tt, ui.RoleTextField, "QQ Resolver Key")
	tt.Type("fixture-qq-ui-key")
	if err := tt.Click("Use QQ Key Once"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	field, ok := tt.AccessByRole(ui.RoleTextField, "QQ Resolver Key")
	if !ok || !field.Password || field.Value != "" {
		t.Fatal("QQ 密钥提交后未清空或不是密码控件")
	}
	for _, label := range []string{"Use Once", "Use NetEase Key Once"} {
		button, found := tt.AccessByRole(ui.RoleButton, label)
		if !found || button.Disabled {
			t.Fatal("QQ 操作清除了其他服务草稿")
		}
	}
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		ResolverCredentials map[string]struct{ State string }
	}
	if json.Unmarshal(raw, &state) != nil || state.ResolverCredentials["tx"].State != "temporary" || state.ResolverCredentials["wy"].State != "empty" {
		t.Fatal("QQ 密钥没有独立提交或修改了网易状态")
	}
	if strings.Contains(string(raw), "fixture-qq-ui-key") {
		t.Fatal("公开状态泄露 QQ 密钥")
	}
	if dir := os.Getenv("MM_FRAMES"); dir != "" {
		saveFrame(t, dir, "aggregate-qq-resolver.png", tt.Image())
	}
}

// 网易密钥操作只更新网易范围，清空已提交输入且保留另一服务尚未提交的草稿。
func TestAggregateSettingsWyKeyStaysSeparate(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	if _, err := src.Aggregate("setResolverKey", map[string]any{"value": "fixture-default-key", "temporary": true}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit.loaded })
	tt.Frame()
	clickSettingRole(t, tt, ui.RoleTextField, "Resolver Key")
	tt.Type("draft-other-key")
	clickSettingRole(t, tt, ui.RoleTextField, "NetEase Resolver Key")
	tt.Type("fixture-wy-ui-key")
	if err := tt.Click("Use NetEase Key Once"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	key, ok := tt.AccessByRole(ui.RoleTextField, "NetEase Resolver Key")
	if !ok || !key.Password || key.Value != "" {
		t.Fatal("网易密钥提交后仍被回填或未使用密码控件")
	}
	other, ok := tt.AccessByRole(ui.RoleButton, "Use Once")
	if !ok || other.Disabled {
		t.Fatal("网易操作清除了其他服务未提交的草稿")
	}
	wyAction, ok := tt.AccessByRole(ui.RoleButton, "Use NetEase Key Once")
	if !ok || !wyAction.Disabled {
		t.Fatal("网易密钥提交后仍留有可再次发送的输入")
	}
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		ResolverCredential  struct{ State string }
		ResolverCredentials map[string]struct{ State string }
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.ResolverCredential.State != "temporary" || status.ResolverCredentials["wy"].State != "temporary" {
		t.Fatal("两个服务的密钥状态发生串用")
	}
	if strings.Contains(string(raw), "fixture-wy-ui-key") || strings.Contains(string(raw), "fixture-default-key") {
		t.Fatal("状态接口泄露密钥")
	}
	if dir := os.Getenv("MM_FRAMES"); dir != "" {
		saveFrame(t, dir, "aggregate-wy-resolver.png", tt.Image())
	}
}

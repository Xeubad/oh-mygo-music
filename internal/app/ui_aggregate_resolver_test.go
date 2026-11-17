package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

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

package plugin

// 复制到隔离客户端 internal/plugin 执行，复用其账号桩并加载正式生成的插件。
import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 设置页独立操作网易时不能借用全局 QQ 目标，也不能清掉其他平台快照和凭据。
func TestAggregateAccountPlatformNeteaseIsolated(t *testing.T) {
	p, store := neteaseAccountTest(t, func(r *http.Request) (string, error) {
		if r.URL.Path == "/eapi/song/like/get" {
			return `{"code":200,"ids":[]}`, nil
		}
		t.Errorf("意外请求：%s", r.URL.Path)
		return `{"code":404}`, nil
	})
	if err := p.SetSetting("accountPlatform", "tx"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSetting("catalogPlatform", "qs"); err != nil {
		t.Fatal(err)
	}
	me, err := p.SignInPlatform("wy")
	if err != nil || me == nil || me.ID != "catalog:wy:account:700" {
		t.Fatalf("显式网易登录被全局目标覆盖：%+v / %v", me, err)
	}
	if _, err := p.Aggregate("refresh", map[string]any{"platform": "wy"}); err != nil {
		t.Fatal(err)
	}
	me, err = p.AccountPlatform("wy")
	if err != nil || me == nil || me.ID != "catalog:wy:account:700" {
		t.Fatalf("显式网易资料读取失去目标：%+v / %v", me, err)
	}
	if me, err := p.Account(); err != nil || me != nil {
		t.Fatalf("旧账号接口不再遵循全局 QQ 设置：%+v / %v", me, err)
	}
	for _, platform := range []string{"tx", "qs"} {
		store.values[p.credentialTarget(`platform:["`+platform+`","700"]`)] = "synthetic-other-session"
		if _, err := p.Aggregate("commitSnapshot", map[string]any{
			"platform": platform, "accountID": "700", "listID": "liked", "complete": true,
			"refreshedAt": 1234, "tracks": []map[string]any{{"id": "synthetic-song"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SignOutPlatform("wy"); err != nil {
		t.Fatal(err)
	}
	if me, err := p.AccountPlatform("wy"); err != nil || me != nil {
		t.Fatal("显式网易退出后会话仍可读取")
	}
	raw, err := p.Aggregate("lists", nil)
	var lists []struct{ Platform string }
	if err != nil || json.Unmarshal(raw, &lists) != nil || len(lists) != 2 {
		t.Fatalf("退出误清其他平台快照：%s / %v", raw, err)
	}
	for _, row := range lists {
		if row.Platform != "tx" && row.Platform != "qs" {
			t.Fatal("网易退出后仍保留该平台快照")
		}
	}
	if len(store.values) != 2 {
		t.Fatal("退出没有只删除所属网易凭据")
	}
	for _, platform := range []string{"tx", "qs"} {
		if store.values[p.credentialTarget(`platform:["`+platform+`","700"]`)] != "synthetic-other-session" {
			t.Fatal("退出改变了其他平台凭据")
		}
	}
	if p.Setting("accountPlatform") != "tx" || p.Setting("catalogPlatform") != "qs" {
		t.Fatal("明确账号操作修改了全局账号或搜索平台")
	}
}

// QQ 官方授权仍走原有只读适配器；显式平台不依赖当前汽水选择。
func TestAggregateAccountPlatformQQAndInvalidTarget(t *testing.T) {
	p, store := qqAccountTest(t, func(r *http.Request, method string) (string, error) {
		t.Errorf("登录和本人读取不应请求列表：%s", method)
		return `{"code":500}`, nil
	})
	if err := p.SetSetting("accountPlatform", "qs"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSetting("catalogPlatform", "wy"); err != nil {
		t.Fatal(err)
	}
	me, err := p.SignInPlatform("tx")
	if err != nil || me == nil || me.ID != "catalog:tx:account:700" {
		t.Fatalf("显式 QQ 登录被全局目标覆盖：%+v / %v", me, err)
	}
	if me, err := p.AccountPlatform("tx"); err != nil || me == nil || me.ID != "catalog:tx:account:700" {
		t.Fatalf("明确 QQ 资料读取失去目标：%+v / %v", me, err)
	}
	if me, err := p.AccountPlatform("qs"); err != nil || me != nil {
		t.Fatalf("未登录汽水却读取其他平台：%+v / %v", me, err)
	}
	for _, platform := range []string{"", "mg", "TX"} {
		if _, err := p.SignInPlatform(platform); !errors.Is(err, music.ErrUnsupported) {
			t.Fatalf("非法登录目标 %q 未拒绝：%v", platform, err)
		}
		if _, err := p.AccountPlatform(platform); !errors.Is(err, music.ErrUnsupported) {
			t.Fatalf("非法查询目标 %q 未拒绝：%v", platform, err)
		}
		if err := p.SignOutPlatform(platform); !errors.Is(err, music.ErrUnsupported) {
			t.Fatalf("非法退出目标 %q 未拒绝：%v", platform, err)
		}
	}
	if len(store.values) != 1 || !strings.Contains(store.values[p.credentialTarget(`platform:["tx","700"]`)], "fixture-music-key") {
		t.Fatal("非法目标操作改变已有 QQ 会话")
	}
	if err := p.SignOutPlatform("tx"); err != nil || len(store.values) != 0 {
		t.Fatalf("明确 QQ 退出未清凭据：%v", err)
	}
	if p.Setting("accountPlatform") != "qs" || p.Setting("catalogPlatform") != "wy" {
		t.Fatal("明确账号操作修改全局平台")
	}
}

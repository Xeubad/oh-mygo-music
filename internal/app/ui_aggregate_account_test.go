package app

import (
	"testing"

	"mygo-music/internal/music"
)

// 设置变更期间到达的旧账号查询不能倒写界面，必须重新读取当前平台身份。
func TestAggregateAccountPlatformChangeDropsLateAccount(t *testing.T) {
	a := newTestApp()
	post := posts(a)
	src := fake(a)
	a.library.account = &music.Account{ID: "catalog:wy:account:700", Name: "网易账号"}
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	src.account = func() (*music.Account, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return &music.Account{ID: "catalog:wy:account:700", Name: "网易账号"}, nil
		}
		return &music.Account{ID: "catalog:tx:account:800", Name: "QQ账号"}, nil
	}
	a.loadAccount(a.library)
	wait(t, started, "旧账号请求")
	a.libraryChanged(a.library)
	a.loadAccount(a.library)
	close(release)
	until(t, post, func() bool { return !a.library.accountBusy })
	if a.library.account == nil || a.library.account.ID != "catalog:tx:account:800" {
		t.Fatal("平台设置变更后旧账号查询覆盖了当前身份")
	}
}

// 切换两个均已登录的平台后，账号身份变更必须清除上一平台的个人列表。
func TestAggregateAccountPlatformChangeClearsPersonalLists(t *testing.T) {
	a := newTestApp()
	post := posts(a)
	src := fake(a)
	a.library.account = &music.Account{ID: "catalog:wy:account:700", Name: "网易账号"}
	a.library.likedList = []music.Track{{ID: "catalog:wy:track:101", Name: "旧喜欢"}}
	src.account = func() (*music.Account, error) {
		return &music.Account{ID: "catalog:tx:account:800", Name: "QQ账号"}, nil
	}
	a.loadAccount(a.library)
	runPost(t, post)
	if a.library.account == nil || a.library.account.ID != "catalog:tx:account:800" {
		t.Fatal("账号平台没有切换")
	}
	if len(a.library.likedList) != 0 {
		t.Fatal("账号身份已切换，上一平台喜欢列表仍保留在界面")
	}
}

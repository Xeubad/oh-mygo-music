package plugin

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"mygo-music/internal/music"
)

// 新检测取得已保存的当前时间；无效联合修改不污染旧设置，旧快照也不会被后续保存改写。
func TestAggregateTimingReadsAtomicSettingsWithoutChangingSnapshots(t *testing.T) {
	p := loadAggregate(t)
	if _, err := p.Aggregate("configure", map[string]any{
		"candidateTimeoutMs": 100, "stallTimeoutMs": 200, "recoveryTimeoutMs": 800,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := p.PlaybackTiming(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("configure", map[string]any{
		"candidateTimeoutMs": 150, "stallTimeoutMs": 900, "recoveryTimeoutMs": 800,
	}); err == nil {
		t.Fatal("冲突时间设置被保存")
	}
	afterInvalid, err := p.PlaybackTiming(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if afterInvalid.CandidateTimeoutMs != 100 || afterInvalid.StallTimeoutMs != 200 || afterInvalid.RecoveryTimeoutMs != 800 {
		t.Fatalf("无效修改污染了生效设置：%+v", afterInvalid)
	}
	if _, err := p.Aggregate("configure", map[string]any{
		"candidateTimeoutMs": 150, "stallTimeoutMs": 300, "recoveryTimeoutMs": 900,
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := p.PlaybackTiming(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest.CandidateTimeoutMs != 150 || latest.StallTimeoutMs != 300 || latest.RecoveryTimeoutMs != 900 {
		t.Fatalf("新检测没有取得联合保存后的当前时间：%+v", latest)
	}
	if first.CandidateTimeoutMs != 100 || first.StallTimeoutMs != 200 || first.RecoveryTimeoutMs != 800 {
		t.Fatalf("已经取得的检测快照被后续设置改写：%+v", first)
	}
}

// 每一档先耗尽已声明支持的同版来源，再按普通音质路径降至用户下限。
func TestAggregateQualityPlanOrdersDeclaredSourcesBeforeDowngrading(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "wy-recording"}
	right := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "tx-recording"}
	for _, fixture := range []struct {
		source    music.AggregateSource
		qualities []string
	}{
		{left, []string{"flac", "320k", "128k", "64k"}},
		{right, []string{"flac", "256k", "128k"}},
	} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{
			"platform": fixture.source.Platform, "accountID": fixture.source.AccountID,
			"listID": "liked", "complete": true, "refreshedAt": 1,
			"tracks": []any{map[string]any{"id": fixture.source.TrackID, "qualities": fixture.qualities}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("configure", map[string]any{
		"targetQuality": "flac", "minimumQuality": "128k",
		"platformOrder": []string{"wy", "tx", "kg", "kw", "mg", "qs"},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections) != 1 || len(page.Sections[0].Tracks) != 1 {
		t.Fatalf("人工确认的同版曲目没有合并: %+v", page)
	}
	plan, err := p.PlaybackPlan(context.Background(), page.Sections[0].Tracks[0].ID, "flac", "quality-order")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ platform, quality string }{
		{"wy", "flac"}, {"tx", "flac"}, {"wy", "320k"},
		{"tx", "256k"}, {"wy", "128k"}, {"tx", "128k"},
	}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("没有按声明音质生成完整降档路径: %+v", plan.Candidates)
	}
	seen := map[string]bool{}
	for i, expected := range want {
		got := plan.Candidates[i]
		if got.Platform != expected.platform || got.Quality != expected.quality {
			t.Fatalf("第 %d 项越过同档来源或尝试了未声明档位: %+v，期望 %+v", i, got, expected)
		}
		if got.ID == "" || seen[got.ID] {
			t.Fatalf("候选标识没有区分来源和请求档位: %+v", plan.Candidates)
		}
		seen[got.ID] = true
	}
}

// 聚合来源的目标单独配置，客户端通用音质标签不能覆盖已保存的精确档位。
func TestAggregateQualityPlaybackUsesAggregateSettings(t *testing.T) {
	p := loadAggregate(t)
	if _, err := p.Aggregate("commitSnapshot", map[string]any{
		"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 1,
		"tracks": []any{map[string]any{"id": "one", "qualities": []string{"flac", "320k", "128k"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("configure", map[string]any{"targetQuality": "320k", "minimumQuality": "128k"}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"high", "standard", "lossless", "32k"} {
		plan, err := p.PlaybackPlan(context.Background(), page.Sections[0].Tracks[0].ID, requested, "aggregate-settings")
		if err != nil {
			t.Fatalf("通用请求 %q 覆盖了聚合设置：%v", requested, err)
		}
		if plan.TargetQuality != "320k" || len(plan.Candidates) != 2 || plan.Candidates[0].Quality != "320k" || plan.Candidates[1].Quality != "128k" {
			t.Fatalf("通用请求 %q 改变了聚合目标或降档顺序：%+v", requested, plan)
		}
	}
}

// 高级目标只按精确名称匹配来源声明，之后回普通 FLAC，不遍历其它高级效果。
func TestAggregateQualityAdvancedTargetFallsStraightBackToOrdinaryFLAC(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "wy-recording"}
	right := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "tx-recording"}
	for _, fixture := range []struct {
		source    music.AggregateSource
		qualities []string
	}{
		{left, []string{"immersive", "master", "flac", "128k"}},
		{right, []string{"spatial", "flac", "320k", "128k"}},
	} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{
			"platform": fixture.source.Platform, "accountID": fixture.source.AccountID,
			"listID": "liked", "complete": true, "refreshedAt": 1,
			"tracks": []any{map[string]any{"id": fixture.source.TrackID, "qualities": fixture.qualities}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := p.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		QualityTargets []struct {
			Quality   string
			Platforms []string
		}
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.QualityTargets) != 3 {
		t.Fatalf("服务声明的高级目标未完整暴露: %s", raw)
	}
	for _, target := range status.QualityTargets {
		want := []string{"wy"}
		if target.Quality == "spatial" {
			want = []string{"tx"}
		}
		if !slices.Equal(target.Platforms, want) {
			t.Fatalf("高级目标平台范围错误: %+v", target)
		}
	}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("configure", map[string]any{"targetQuality": "immersive", "minimumQuality": "128k"}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.PlaybackPlan(context.Background(), page.Sections[0].Tracks[0].ID, "", "advanced-target")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ platform, quality string }{
		{"wy", "immersive"}, {"tx", "flac"}, {"wy", "flac"},
		{"tx", "320k"}, {"tx", "128k"}, {"wy", "128k"},
	}
	if plan.TargetQuality != "immersive" || len(plan.Candidates) != len(want) {
		t.Fatalf("高级回退路径不完整: %+v", plan)
	}
	for i, candidate := range plan.Candidates {
		if candidate.Platform != want[i].platform || candidate.Quality != want[i].quality {
			t.Fatalf("高级目标遍历了其它效果或顺序错误: %+v", plan.Candidates)
		}
	}
}

// 音质能力声明损坏时整张列表不生效，不能把字符串的子串或半份数据当作支持证据。
func TestAggregateQualityRejectsMalformedDeclarationWithoutReplacingSnapshot(t *testing.T) {
	p := loadAggregate(t)
	valid := map[string]any{
		"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 1,
		"tracks": []any{map[string]any{"id": "original", "qualities": []string{"flac", "128k"}}},
	}
	if _, err := p.Aggregate("commitSnapshot", valid); err != nil {
		t.Fatal(err)
	}
	for _, qualities := range []any{"flac", []any{"flac", 128}, []string{""}, []string{"flac", "flac"}} {
		_, err := p.Aggregate("commitSnapshot", map[string]any{
			"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 2,
			"tracks": []any{map[string]any{"id": "replacement", "qualities": qualities}},
		})
		if err == nil {
			t.Fatalf("非法声明覆盖了完整快照: %#v", qualities)
		}
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].AggregateSources[0].TrackID != "original" {
		t.Fatalf("拒绝声明后丢失原有列表: %+v", page)
	}
}

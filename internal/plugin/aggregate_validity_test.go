package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"mygo-music/internal/music"
)

// 真实 MoeJS 必须导入合成文本并按 provider 冻结计划及解析；来源故障只淘汰该 provider，不全局封锁。
func TestAggregateCustomSourcesImportPlanResolveInMoeJS(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	oldStore, oldTransport := systemCredentials, http.DefaultTransport
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = oldStore; http.DefaultTransport = oldTransport })
	imports, resolutions := 0, 0
	http.DefaultTransport = aggregateCatalogTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.URL.Host != "source.shiqianjiang.cn" || r.Method != "GET" || r.Header.Get("Cookie") != "" {
			t.Error("自定义来源越过合成 HTTP 边界或挪用了账号凭据")
		}
		switch r.URL.Path {
		case "/api/script/lx":
			imports++
			if r.URL.Query().Get("key") != "synthetic-primary" && r.URL.Query().Get("key") != "synthetic-backup" {
				t.Error("下载链接的合成凭据丢失")
			}
			body = fmt.Sprintf("// 合成音源，验证 UTF-8 字节计数\nconst API_URL = %q;\nconst API_KEY = %q;\nconst MUSIC_QUALITY = {\"tx\":[\"128k\",\"flac\"]};\nthrow new Error(\"source script must never execute\");", "https://source.shiqianjiang.cn/api", r.URL.Query().Get("key"))
		case "/api/music/url":
			resolutions++
			if r.URL.Query().Get("source") != "tx" || r.URL.Query().Get("songId") != "song123" || r.URL.Query().Get("quality") != "flac" || r.URL.Query().Get("key") != "" {
				t.Error("解析没有保留原始曲目/档位，或把密钥放进查询参数")
			}
			switch r.Header.Get("X-API-Key") {
			case "synthetic-primary":
				body = `{"code":403,"message":"invalid key"}`
			case "synthetic-backup":
				body = `{"code":200,"url":"https://media.invalid/synthetic-recording"}`
			default:
				t.Error("候选使用了错误的来源密钥")
				body = `{}`
			}
		default:
			t.Errorf("出现未约定的请求路径：%s", r.URL.Path)
			body = `{}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	p := loadAggregate(t)
	for _, key := range []string{"synthetic-primary", "synthetic-backup"} {
		if _, err := p.Aggregate("addSource", map[string]any{"url": "https://source.shiqianjiang.cn/api/script/lx?key=" + key}); err != nil {
			t.Fatalf("真实 MoeJS 导入合成来源失败：%v", err)
		}
	}
	raw, err := p.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		CustomSources struct {
			Items []struct{ ID string }
		}
	}
	if err := json.Unmarshal(raw, &status); err != nil || len(status.CustomSources.Items) != 2 {
		t.Fatalf("来源导入后公开状态不完整：%s %v", raw, err)
	}
	for _, secret := range []string{"synthetic-primary", "synthetic-backup", "/api/script/lx"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("公开状态泄露订阅地址或凭据")
		}
	}
	const id = "catalog:tx:track:song123"
	plan, err := p.PlaybackPlan(context.Background(), id, "high", "source-round")
	if err != nil || plan == nil || len(plan.Candidates) != 4 {
		t.Fatalf("没有按档位及 provider 扩展候选：%+v %v", plan, err)
	}
	for i, candidate := range plan.Candidates {
		quality := "flac"
		if i >= 2 {
			quality = "128k"
		}
		if candidate.Platform != "tx" || candidate.TrackID != "song123" || candidate.AccountID != "public" || candidate.Quality != quality || candidate.SourceID != status.CustomSources.Items[i%2].ID || candidate.SourceRevision == "" {
			t.Fatalf("候选顺序、provider 或冻结修订丢失：%+v", candidate)
		}
	}
	if _, err := p.ResolveCandidate(context.Background(), id, "source-round", plan.Candidates[0]); !errors.Is(err, music.ErrUnavailable) || errors.Is(err, music.ErrPlaybackBlocked) {
		t.Fatalf("单来源凭据失效被当成全局封锁：%v", err)
	}
	if valid, err := p.CandidateValid(context.Background(), id, "source-round", plan.Candidates[0]); err != nil || valid {
		t.Fatalf("来源故障后旧候选仍有效：valid=%v error=%v", valid, err)
	}
	result, err := p.ResolveCandidate(context.Background(), id, "source-round", plan.Candidates[1])
	if err != nil || result == nil || result.Stream.URL != "https://media.invalid/synthetic-recording" || result.Platform != "tx" || result.RequestedQuality != "flac" || result.Stream.Quality != "" || len(result.Stream.Headers) != 0 || imports != 2 || resolutions != 2 {
		t.Fatalf("备用没有按独立凭据解析，或返回标签冒充媒体证据：result=%+v imports=%d resolutions=%d error=%v", result, imports, resolutions, err)
	}
	if _, err := p.Aggregate("setSourceEnabled", map[string]any{"id": plan.Candidates[1].SourceID, "enabled": false}); err != nil {
		t.Fatal(err)
	}
	started := false
	valid, err := p.CommitCandidate(context.Background(), id, "source-round", plan.Candidates[1], func() error { started = true; return nil })
	if err != nil || valid || started {
		t.Fatalf("停用后旧 provider 仍被允许开播：valid=%v started=%v error=%v", valid, started, err)
	}
}

// 收紧来源必须与最终开播互斥并广播；其他控制动作及失败不能改写冻结候选的生命周期。
func TestAggregateSourceControlsSerializeCommitAndNotify(t *testing.T) {
	for _, tc := range []struct {
		action   string
		input    map[string]any
		tightens bool
	}{
		{"removeSource", map[string]any{"id": "source"}, true},
		{"replaceSource", map[string]any{"id": "source", "url": "synthetic-replacement"}, true},
		{"setSourceEnabled", map[string]any{"id": "source", "enabled": false}, true},
		{"setSourceEnabled", map[string]any{"id": "source", "enabled": true}, true},
		{"revokeLink", nil, true},
		{"logout", nil, true},
		{"addSource", map[string]any{"url": "synthetic-new-source"}, false},
		{"updateSource", map[string]any{"id": "source"}, false},
		{"moveSource", map[string]any{"id": "source", "direction": "up"}, false},
		{"moveSource", map[string]any{"id": "source", "direction": "down"}, false},
		{"retrySource", map[string]any{"id": "source"}, false},
		{"refreshSources", nil, false},
	} {
		t.Run(fmt.Sprint(tc.action, "/", tc.input), func(t *testing.T) {
			// 仅用内存 JS 和虚拟调度验证公开门禁，不需要真实网络、凭据或媒体设备。
			synctest.Test(t, func(t *testing.T) {
				p := load(t, plain, `
export function aggregate({input}) { if(input && input.fail) throw new Error("control failed"); return {}; }
export function candidateValid() { return true; }
`)
				changes := p.CandidateChanges()
				entered, release := make(chan struct{}), make(chan struct{})
				commitDone := make(chan error, 1)
				go func() {
					valid, err := p.CommitCandidate(context.Background(), "one", "round", music.PlaybackCandidate{}, func() error {
						close(entered)
						<-release
						return nil
					})
					if err == nil && !valid {
						err = context.Canceled
					}
					commitDone <- err
				}()
				<-entered
				controlDone := make(chan error, 1)
				go func() {
					_, err := p.Aggregate(tc.action, tc.input)
					controlDone <- err
				}()
				synctest.Wait()
				completed := false
				select {
				case err := <-controlDone:
					completed = true
					if err != nil {
						t.Error(err)
					}
				default:
				}
				if completed == tc.tightens {
					t.Errorf("收紧门禁不符：action=%s completedDuringCommit=%v", tc.action, completed)
				}
				close(release)
				if err := <-commitDone; err != nil {
					t.Fatal(err)
				}
				if !completed {
					if err := <-controlDone; err != nil {
						t.Fatal(err)
					}
				}
				notified := false
				select {
				case <-changes:
					notified = true
				default:
				}
				if notified != tc.tightens {
					t.Errorf("来源控制广播不符：action=%s notified=%v", tc.action, notified)
				}
				changes = p.CandidateChanges()
				if _, err := p.Aggregate(tc.action, map[string]any{"fail": true}); err == nil {
					t.Fatal("控制失败被当成成功")
				}
				select {
				case <-changes:
					t.Fatal("失败的来源控制广播了资格变化")
				default:
				}
			})
		})
	}
}

// 同版候选的重播身份指向该候选实际来源；拆分后原入队项和已播项各保留自己的版本。
func TestAggregateCandidateCarriesPlayedRecordingIdentity(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "two"}
	right := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "one"}
	for _, source := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	pair := map[string]any{"left": left, "right": right}
	if _, err := p.Aggregate("confirmLink", pair); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	id := page.Sections[0].Tracks[0].ID
	raw, err := p.Aggregate("playbackPlan", map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Candidates []struct {
			Platform   string
			AccountID  string
			TrackID    string
			PlaybackID string
		}
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("缺少同版候选：%s", raw)
	}
	for _, candidate := range plan.Candidates {
		var frozen struct {
			Version  int
			Identity music.AggregateSource
			Sources  []music.AggregateSource
		}
		if !strings.HasPrefix(candidate.PlaybackID, "aggregate:") {
			t.Fatalf("候选缺少可恢复的实际版本身份：%+v", candidate)
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(candidate.PlaybackID, "aggregate:")), &frozen); err != nil {
			t.Fatal(err)
		}
		if frozen.Version != 1 || frozen.Identity.Platform != candidate.Platform || frozen.Identity.AccountID != candidate.AccountID || frozen.Identity.TrackID != candidate.TrackID || len(frozen.Sources) != 2 {
			t.Fatalf("实际版本身份或冻结来源丢失：%+v", frozen)
		}
	}
	if _, err := p.Aggregate("revokeLink", pair); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range plan.Candidates {
		replay, err := p.PlaybackPlan(context.Background(), candidate.PlaybackID, "flac", "replay")
		if err != nil {
			t.Fatal(err)
		}
		if len(replay.Candidates) != 1 || replay.Candidates[0].Platform != candidate.Platform || replay.Candidates[0].AccountID != candidate.AccountID || replay.Candidates[0].TrackID != candidate.TrackID {
			t.Fatalf("拆分后的重播改换了版本：%+v", replay)
		}
	}
}

// 在途资格只认可旧队列的冻结来源；撤销和退出立即收紧，新增关联不能给旧轮次加来源。
func TestAggregateCandidateValidityTracksFrozenRecordingAndLogout(t *testing.T) {
	p := loadAggregate(t)
	if !p.Can("candidateValid") {
		t.Fatal("聚合插件缺少在途候选资格校验")
	}
	left := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "two"}
	right := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "one"}
	for _, s := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": s.Platform, "accountID": s.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": s.TrackID, "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	pair := map[string]any{"left": left, "right": right}
	if _, err := p.Aggregate("confirmLink", pair); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	id := page.Sections[0].Tracks[0].ID
	var queued struct{ Identity music.AggregateSource }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(id, "aggregate:")), &queued); err != nil {
		t.Fatal(err)
	}
	candidate := func(s music.AggregateSource) music.PlaybackCandidate {
		return music.PlaybackCandidate{ID: "frozen", Platform: s.Platform, AccountID: s.AccountID, TrackID: s.TrackID, Quality: "flac"}
	}
	check := func(s music.AggregateSource, want bool) {
		t.Helper()
		valid, err := p.CandidateValid(context.Background(), id, "round-1", candidate(s))
		if err != nil || valid != want {
			t.Fatalf("候选资格不符：source=%+v valid=%v want=%v error=%v", s, valid, want, err)
		}
	}
	check(left, true)
	check(right, true)
	third := music.AggregateSource{Platform: "mg", AccountID: "c", TrackID: "three"}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": third}); err != nil {
		t.Fatal(err)
	}
	check(third, false)
	if _, err := p.Aggregate("revokeLink", pair); err != nil {
		t.Fatal(err)
	}
	for _, s := range []music.AggregateSource{left, right} {
		check(s, s == queued.Identity)
	}
	if _, err := p.Aggregate("logout", map[string]any{"platform": queued.Identity.Platform, "accountID": queued.Identity.AccountID}); err != nil {
		t.Fatal(err)
	}
	check(queued.Identity, false)
}

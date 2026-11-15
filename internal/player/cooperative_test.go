package player_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/music"
	"mygo-music/internal/player"
	"mygo-music/internal/plugin"
)

func cooperativePlugin(t *testing.T, source string) *plugin.Plugin {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"plugin.json": `{"id":"cooperative","permissions":{"network":["127.0.0.1"]}}`,
		"index.js":    source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := plugin.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func cooperativePlayer(src music.Source) (*player.Player, chan func()) {
	posts := make(chan func(), 64)
	p := player.New(func(fn func()) { posts <- fn })
	p.Source = func(string) music.Source { return src }
	return p, posts
}

func finishLoading(t *testing.T, p *player.Player, posts <-chan func()) {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("player did not complete loading")
		}
	}
}

func audioPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal("file:///" + filepath.ToSlash(path))
	return string(b)
}

// 同档第一来源失败后继续下一来源，实际 FLAC 不依赖服务返回的音质标签。
func TestCooperativePluginFallsThroughToVerifiedAudio(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function stream() { throw new Error("legacy stream must not be used"); }
export function playbackPlan() { return {
  targetQuality: "flac", minimumQuality: "128k", candidates: [
    {id: "a", platform: "wy", trackID: "raw-a", quality: "flac"},
    {id: "b", platform: "tx", trackID: "raw-b", quality: "flac"}
  ]
}; }
export function resolveCandidate({candidate, roundID}) {
  if (!roundID) throw new Error("missing round identity");
  if (candidate.id === "a") throw Object.assign(new Error("unavailable"), {code: "unavailable"});
  return {stream: {url: %s, type: "mp3", quality: "32k"}, platform: "lying", requestedQuality: "flac"};
}`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "aggregate:1", Source: "cooperative", Duration: 1000}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Err() != "" {
		t.Fatalf("cooperative playback not ready: ready=%v err=%q", p.Ready(), p.Err())
	}
	if got := p.Stream(); got == nil || got.Quality != "flac" || got.Type != "flac" {
		t.Fatalf("actual media evidence not used: %+v", got)
	}
	p.TogglePlay()
}

// 暂停正在解析的聚合项必须中止真实请求，且不跳歌、不记作失败。
func TestCooperativePauseCancelsResolverRequest(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac", minimumQuality:"128k", candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 const response = await fetch(%q, {signal});
 return response.json();
}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("resolver request did not start")
	}
	p.TogglePlay()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("pause left resolver network request running")
	}
	for len(posts) > 0 {
		(<-posts)()
	}
	if p.Playing() || p.Loading() || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("pause changed playback intent: playing=%v loading=%v error=%q current=%+v", p.Playing(), p.Loading(), p.Err(), p.Current())
	}
}

// 首源返回达下限的低档音频也不能抢过同档下一源的真实 FLAC。
func TestCooperativeLowerActualQualityDoesNotPreemptFLAC(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},{id:"b",platform:"tx",trackID:"b",quality:"flac"},
{id:"c",platform:"wy",trackID:"a",quality:"128k"}]}; }
export function resolveCandidate({candidate}) { return {stream:{url:candidate.id==="b"?%s:%s,type:"flac",quality:"flac"}}; }
`, audioPath(t, "seek.flac"), audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Quality != "flac" || p.Stream().Platform != "tx" {
		t.Fatalf("lower-quality response preempted FLAC: ready=%v stream=%+v error=%q", p.Ready(), p.Stream(), p.Err())
	}
	p.TogglePlay()
}

// 高级名称本身不是媒体证据；返回普通低档时必须先走普通 FLAC 路径。
func TestCooperativeAdvancedTargetFallsBackToOrdinaryFLAC(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"immersive",minimumQuality:"128k",candidates:[
{id:"advanced",platform:"wy",trackID:"a",quality:"immersive"},
{id:"ordinary",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate}) { return {stream:{url:candidate.id==="advanced"?%s:%s}}; }
`, audioPath(t, "seek.mp3"), audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Quality != "flac" || p.Stream().Platform != "tx" {
		t.Fatalf("unverified advanced quality bypassed ordinary FLAC: ready=%v stream=%+v error=%q", p.Ready(), p.Stream(), p.Err())
	}
	p.TogglePlay()
}

// 聚合候选必须由当前播放轮次掌控，不能被旧预取接口提前访问。
func TestCooperativeDoesNotUseLegacyPrefetch(t *testing.T) {
	legacy := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case legacy <- struct{}{}:
		default:
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%s,type:"flac"}}; }
export async function stream() { await fetch(%q); throw new Error("legacy prefetch was used"); }
`, audioPath(t, "seek.flac"), server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer p.TogglePlay()
	select {
	case <-legacy:
		t.Fatal("cooperative source bypassed through legacy prefetch")
	case <-time.After(100 * time.Millisecond):
	}
}

// 实际有损流即使声称 FLAC，也不能突破用户的无损下限。
func TestCooperativeRejectsAudioBelowMinimum(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"flac",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%s,type:"flac",quality:"flac",bitRate:999999}}; }
`, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Err() == "" || p.Current().ID != "one" {
		t.Fatalf("below-minimum audio accepted: ready=%v playing=%v err=%q", p.Ready(), p.Playing(), p.Err())
	}
}

// 单曲循环的最终失败不是正常结束，不能顺着队列换到下一首。
func TestCooperativeRepeatOneFailureStaysOnCurrentTrack(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan({id}) { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id,platform:"wy",trackID:id,quality:"flac"}]}; }
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.CycleRepeat()
	p.CycleRepeat()
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Playing() || p.Current().ID != "one" || p.Err() != "failed:one" {
		t.Fatalf("repeat-one failure advanced queue: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
	}
}

// 每项候选耗尽才计一次最终失败；默认连续三项后不得再扫描第四项。
func TestCooperativeQueueStopsAfterThreeFinalFailures(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan({id}) { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id,platform:"wy",trackID:id,quality:"flac"}]}; }
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Playing() || p.Current().ID != "three" || p.Err() != "failed:three" {
		t.Fatalf("default failure limit ignored: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
	}
}

// 获取计划本身失败也属于本项最终失败，不能绕过默认连续三首限制。
func TestCooperativePlanFailuresStopAfterThreeTracks(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"error", `throw new Error("plan failed:"+id);`},
		{"nil", `return null;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan({id}) { %s }
export function resolveCandidate() { throw new Error("unexpected resolve"); }
`, tc.body))
			p, posts := cooperativePlayer(src)
			p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}}, 0)
			finishLoading(t, p, posts)
			if p.Playing() || p.Current().ID != "three" || p.Err() == "" {
				t.Fatalf("plan failure bypassed failure limit: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
			}
		})
	}
}

// 明确的全局密钥封锁不是单曲失败，自动队列也必须停在当前项。
func TestCooperativeBlockedStopsQueue(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { throw Object.assign(new Error("key revoked"), {code:"playbackBlocked"}); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Playing() || p.Ready() || p.Current().ID != "one" || p.Err() == "" {
		t.Fatalf("global block advanced queue: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
	}
}

// 拿到可解码媒体不是成功；主线程排队超过原候选截止也不得迟到开播。
func TestCooperativeExpiredCommitContinuesNextCandidate(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:40,roundTimeoutMs:1000,candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},{id:"b",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate}) {
 if (candidate.id==="b") throw new Error("next candidate reached");
 return {stream:{url:%s,type:"flac"}};
}`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	var commit func()
	select {
	case commit = <-posts:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate did not prepare")
	}
	time.Sleep(80 * time.Millisecond)
	commit()
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Err() != "next candidate reached" {
		t.Fatalf("expired candidate started: ready=%v playing=%v error=%q", p.Ready(), p.Playing(), p.Err())
	}
}

// 主线程回调已经进入后仍可能超时，音量准备耗尽预算不能继续安装并播放。
func TestCooperativeStartupDeadlineDoesNotPlayLate(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:40,roundTimeoutMs:1000,candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},{id:"b",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate}) {
 if(candidate.id==="b") throw new Error("next candidate reached");
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.Normalize = func() bool {
		// 只压缩等待到毫秒级；音频输出常驻 goroutine 不能放进 synctest bubble。
		time.Sleep(80 * time.Millisecond)
		return false
	}
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Stream() != nil || p.Err() != "next candidate reached" {
		t.Fatalf("startup exceeded deadline but played: ready=%v playing=%v stream=%+v error=%q", p.Ready(), p.Playing(), p.Stream(), p.Err())
	}
}

// 换来源不重置整轮时钟，多个慢候选最多共享原二十秒。
func TestCooperativeRoundBudgetDoesNotRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := cooperativePlugin(t, `
export function playbackPlan(){return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:5000,roundTimeoutMs:20000,candidates:Array.from({length:6},(_,i)=>({id:String(i),platform:"wy",trackID:String(i),quality:"flac"}))};}
export function resolveCandidate({signal}) { return new Promise((resolve, reject) => signal.addEventListener("abort", () => reject(new Error("aborted")), {once:true})); }
`)
		p, posts := cooperativePlayer(src)
		started := time.Now()
		p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
		timer := time.NewTimer(25 * time.Second)
		defer timer.Stop()
		for p.Loading() {
			select {
			case fn := <-posts:
				fn()
			case <-timer.C:
				t.Fatal("round budget restarted")
			}
		}
		if p.Ready() || p.Playing() || p.Err() == "" || time.Since(started) != 20*time.Second {
			t.Fatalf("round elapsed=%v ready=%v error=%q", time.Since(started), p.Ready(), p.Err())
		}
	})
}

// 首个解析永不返回时，候选五秒截止后在同一轮继续，不能卡死或重试该组合。
func TestCooperativeCandidateTimeoutUsesVirtualTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:5000,roundTimeoutMs:20000,candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},
{id:"b",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate,signal}) {
 if(candidate.id==="a") return new Promise((resolve,reject)=>signal.addEventListener("abort",()=>reject(new Error("aborted")),{once:true}));
 throw new Error("second candidate reached"); // %s
}
`, audioPath(t, "seek.flac")))
		p, posts := cooperativePlayer(src)
		start := time.Now()
		p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative", Duration: 1000}}, 0)
		timer := time.NewTimer(25 * time.Second)
		defer timer.Stop()
		for p.Loading() {
			select {
			case fn := <-posts:
				fn()
			case <-timer.C:
				t.Fatal("candidate did not expire within round budget")
			}
		}
		if p.Ready() || p.Err() != "second candidate reached" || time.Since(start) != 5*time.Second {
			t.Fatalf("candidate budget not shared: ready=%v err=%q elapsed=%v", p.Ready(), p.Err(), time.Since(start))
		}

	})
}

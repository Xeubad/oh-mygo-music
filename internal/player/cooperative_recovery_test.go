package player_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

// 新中断必须读取最新时间设置，不能永久沿用首次开播时的恢复预算。
func TestCooperativeNewInterruptionUsesUpdatedTiming(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut, entered, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds = 0;
let timing = {candidateTimeoutMs:1000,stallTimeoutMs:20,recoveryTimeoutMs:2000};
export function aggregate({input}) { Object.assign(timing,input); return {ok:true}; }
export function playbackTiming() { return timing; }
export function playbackPlan() { rounds++; return {...timing,targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 if (rounds>1) await fetch(%q+"/resolve",{signal});
 return {stream:{url:%q+"/audio"}};
}
`, server.URL, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	if _, err := src.Aggregate("configure", map[string]any{"recoveryTimeoutMs": 80}); err != nil {
		t.Fatal(err)
	}
	close(cut)
	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
waitUpdatedRecovery:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-entered:
			break waitUpdatedRecovery
		case <-timer.C:
			t.Fatal("新中断没有启动恢复")
		}
	}
	select {
	case <-canceled:
	case <-timer.C:
		t.Fatal("新中断仍沿用首次开播的两秒预算，未采用更新后的八十毫秒")
	}
	for p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("新恢复截止后未停留当前项")
		}
	}
	if p.Playing() || p.Err() == "" || p.Current().ID != "one" {
		t.Fatalf("时间设置改变了恢复终态：current=%+v error=%q", p.Current(), p.Err())
	}
}

// 同一次停滞随后变为明确来源错误时，仍沿用检测开始的预算，而不是重新读取设置。
func TestCooperativeStallKeepsTimingWhenSourceFails(t *testing.T) {
	for _, failure := range []string{"network", "decoder"} {
		t.Run(failure, func(t *testing.T) {
			audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
			if err != nil {
				t.Fatal(err)
			}
			cut, entered, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			snapshots := make(chan struct{}, 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/timing":
					snapshots <- struct{}{}
					return
				case "/resolve":
					close(entered)
					<-r.Context().Done()
					close(canceled)
					return
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
				w.Write(audio[:16384])
				w.(http.Flusher).Flush()
				select {
				case <-cut:
					if failure == "decoder" {
						// 完整交付损坏帧，确保错误来自真实解码而不是 HTTP 截断。
						damaged := append([]byte(nil), audio[16384:]...)
						clear(damaged[16913-16384 : 16923-16384])
						w.Write(damaged)
					}
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			defer server.CloseClientConnections()
			src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
let timing={candidateTimeoutMs:2000,stallTimeoutMs:400,recoveryTimeoutMs:800};
export function aggregate({input}) { Object.assign(timing,input); return {ok:true}; }
export async function playbackTiming({signal}) {
 const snapshot={...timing};
 await fetch(%q+"/timing",{signal});
 return snapshot;
}
export function playbackPlan() { rounds++; return {...timing,targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 if(rounds>1) await fetch(%q+"/resolve",{signal});
 return {stream:{url:%q+"/audio"}};
}
`, server.URL, server.URL, server.URL))
			p, posts := cooperativePlayer(src)
			p.SetVolume(0)
			p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
			finishLoading(t, p, posts)
			defer func() {
				if p.Playing() || p.Loading() {
					p.TogglePlay()
				}
			}()
			limit := time.NewTimer(3 * time.Second)
			defer limit.Stop()
			// 本地请求仅作时序闸门；等缓冲不再推进后再模拟用户改设置和断流。
			var quiet *time.Timer
			var quietC <-chan time.Time
		waitStableDetection:
			for {
				select {
				case fn := <-posts:
					fn()
				case <-snapshots:
					if quiet == nil {
						quiet = time.NewTimer(120 * time.Millisecond)
						quietC = quiet.C
					} else {
						quiet.Reset(120 * time.Millisecond)
					}
				case <-quietC:
					break waitStableDetection
				case <-limit.C:
					t.Fatal("没有观察到持续停滞的检测阶段")
				}
			}
			if quiet != nil {
				defer quiet.Stop()
			}
			if _, err := src.Aggregate("configure", map[string]any{"recoveryTimeoutMs": 2000}); err != nil {
				t.Fatal(err)
			}
			close(cut)
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
		waitFailureRecovery:
			for {
				select {
				case fn := <-posts:
					fn()
				case <-entered:
					break waitFailureRecovery
				case <-deadline.C:
					t.Fatal("停滞转明确断流后没有恢复当前项")
				}
			}
			select {
			case <-canceled:
			case <-deadline.C:
				t.Fatal("同一次检测转断流时重取设置，延长了原八百毫秒预算")
			}
			finishLoading(t, p, posts)
			if p.Playing() || p.Err() == "" || p.Current().ID != "one" {
				t.Fatalf("恢复截止没有保留当前项：current=%+v error=%q", p.Current(), p.Err())
			}
		})
	}
}

// 只有元数据的候选不能先报开播成功，再借自动恢复绕过首次候选截止。
func TestCooperativeInitialPlaybackWaitsForDecodedAudio(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:8304])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan() {
 if (++rounds !== 1) throw new Error("first playback incorrectly became recovery");
 return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:100,roundTimeoutMs:1000,stallTimeoutMs:400,candidates:[
 {id:"metadata",platform:"wy",trackID:"one",quality:"flac"},
 {id:"audio",platform:"tx",trackID:"one",quality:"flac"}]};
}
export function resolveCandidate({candidate}) {
 // 后备只确认同轮继续；真实音频开播另有集成覆盖，不能要求其在 100ms 内完成解码。
 if (candidate.id === "audio") throw new Error("next candidate reached");
 return {stream:{url:%q}};
}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	if p.Ready() || p.Playing() || p.Stream() != nil || p.Err() != "next candidate reached" || p.Current().ID != "one" {
		t.Fatalf("未解码音频被接受或首次候选截止后未继续：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("首次候选截止没有取消真实下载")
	}
}

// 已成功开播后的明确网络截断应恢复当前项，不能冒充正常结束推进队列。
func TestCooperativeNetworkInterruptionResumesSameQueue(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		if requests.Add(1) == 1 {
			w.Write(audio[:len(audio)/2])
			w.(http.Flusher).Flush()
			select {
			case <-cut:
			case <-r.Context().Done():
			}
			return
		}
		w.Write(audio)
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds = 0;
export function aggregate() { return {rounds}; }
export function playbackPlan({id}) {
 if (id !== "one") throw new Error("recovery changed queued recording");
 rounds++;
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"a",platform:"wy",trackID:"raw-one",quality:"flac"}]};
}
export function resolveCandidate() { return {stream:{url:%q}}; }
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() {
		t.Fatalf("初次开播失败：%q", p.Err())
	}
	close(cut)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for requests.Load() < 2 || !p.Ready() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("明确网络截断后未恢复当前项：requests=%d current=%+v error=%q", requests.Load(), p.Current(), p.Err())
		}
	}
	if !p.Playing() || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 2 || p.Queue()[1].ID != "two" {
		t.Fatalf("恢复改变了播放意图或队列：playing=%v current=%+v queue=%+v error=%q", p.Playing(), p.Current(), p.Queue(), p.Err())
	}
	p.TogglePlay()
}

// 恢复候选只有元数据而无法解码中断位置时，必须在原候选截止内转向下一来源。
func TestCooperativeRecoveryWaitsForDecodedPosition(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)+1))
		if r.URL.Path == "/initial" {
			w.Write(audio)
			w.(http.Flusher).Flush()
			select {
			case <-cut:
			case <-r.Context().Done():
			}
			return
		}
		// 只交付完整 FLAC 元数据；实际定位和第一帧解码都必须等待后续字节。
		w.Write(audio[:8304])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan() {
 rounds++;
 return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:150,roundTimeoutMs:2000,candidates:rounds===1?
 [{id:"initial",platform:"wy",trackID:"one",quality:"flac"}]:
 [{id:"blocked",platform:"wy",trackID:"one",quality:"flac"},{id:"complete",platform:"tx",trackID:"two",quality:"flac"}]};
}
export function resolveCandidate({candidate}) { return {stream:{url:candidate.id==="complete"?%s:%q+"/"+candidate.id}}; }
`, audioPath(t, "seek.flac"), server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	p.Seek(3 * time.Second)
	timer, tick := time.NewTimer(2*time.Second), time.NewTicker(time.Millisecond)
	defer timer.Stop()
	defer tick.Stop()
	// 无头验证不依赖声卡推进；本测试用公开定位意图建立固定中断基准。
	for p.Position() < 3*time.Second {
		select {
		case fn := <-posts:
			fn()
		case <-tick.C:
		case <-timer.C:
			t.Fatal("初始音频没有推进到中断位置")
		}
	}
	interrupted := p.Position()
	close(cut)
	for p.Stream() == nil || p.Stream().Platform != "tx" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("未完成定位的候选被当成恢复成功：stream=%+v error=%q", p.Stream(), p.Err())
		}
	}
	select {
	case <-canceled:
	case <-timer.C:
		t.Fatal("定位超时未取消真实下载")
	}
	if !p.Playing() || p.Current().ID != "one" || len(p.Queue()) != 2 || p.Position() < interrupted-2*time.Second || p.Position() > interrupted+100*time.Millisecond {
		t.Fatalf("恢复未保持中断位置与队列：interrupted=%v position=%v current=%+v", interrupted, p.Position(), p.Current())
	}
	p.TogglePlay()
}

// MP3 的声明时长不是定位证据，短音频不能靠字节比例伪装成中断位置的样本。
func TestCooperativeRecoveryRejectsMP3BeforeInterruption(t *testing.T) {
	flac, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	mp3, err := os.ReadFile(filepath.Join("testdata", "seek.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	cut := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/short" {
			w.Write(mp3[:10000])
			return
		}
		if r.URL.Path == "/complete" {
			// 冷启动或本机调度可超过 300ms；完整来源变慢不能改变短音频的拒绝结论。
			timer := time.NewTimer(350 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				w.Write(mp3)
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(flac)+1))
		w.Write(flac)
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan() {
 // 本用例核验真实定位，沿用正式候选预算；超时边界由独立虚拟时钟测试覆盖。
 rounds++; return {targetQuality:rounds===1?"flac":"128k",minimumQuality:"128k",candidates:rounds===1?
 [{id:"initial",platform:"wy",trackID:"one",quality:"flac"}]:
 [{id:"short",platform:"wy",trackID:"one",quality:"128k"},{id:"complete",platform:"tx",trackID:"one",quality:"128k"}]};
}
export function resolveCandidate({candidate}) {return {stream:{url:%q+"/"+candidate.id}};}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative", Duration: 5000}}, 0)
	finishLoading(t, p, posts)
	p.Seek(3 * time.Second)
	close(cut)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for p.Stream() == nil || p.Stream().Platform != "tx" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("短 MP3 冒充中断位置续播：stream=%+v position=%v error=%q", p.Stream(), p.Position(), p.Err())
		}
	}
	if !p.Playing() || p.Current().ID != "one" || p.Position() < time.Second || p.Position() > 3100*time.Millisecond {
		t.Fatalf("续播位置或当前项改变：position=%v current=%+v error=%q", p.Position(), p.Current(), p.Err())
	}
	p.TogglePlay()
}

// 非整帧会话位置在交接时必须沿用已核验样本，不能再次估算网络字节位置。
func TestCooperativeRecoveryKeepsPreparedSamplePosition(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "seek.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 真实样本已足够；夸大的总长度会暴露交接后再次走字节比例定位的问题。
		w.Header().Set("Content-Length", fmt.Sprint(len(data)*10))
		w.Write(data)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){return {targetQuality:"128k",minimumQuality:"128k",candidateTimeoutMs:300,candidates:[{id:"a",platform:"wy",trackID:"one",quality:"128k"}]};}
export function resolveCandidate(){return {stream:{url:%q,size:%d}};}
`, server.URL, len(data)*10))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	// 3006ms 在 44.1kHz 下为 132564.6 帧，稳定暴露截断与四舍五入的差异。
	state := `{"queue":[{"id":"one","source":"cooperative","duration":5000}],"order":[0],"at":0,"position":3006}`
	var session player.Session
	if err := json.Unmarshal([]byte(state), &session); err != nil {
		t.Fatal(err)
	}
	p.Restore(session)
	finishLoading(t, p, posts)
	settled := time.NewTimer(350 * time.Millisecond)
	defer settled.Stop()
waitSeek:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-settled.C:
			break waitSeek
		}
	}
	if p.Playing() || !p.Ready() || p.Loading() || p.Err() != "" || p.Position() < 3*time.Second {
		t.Fatalf("交接丢失已核验起点：loading=%v position=%v error=%q", p.Loading(), p.Position(), p.Err())
	}
}

// 已开播后的恢复有独立总预算，不能重新领取首次开播的整轮时限。
func TestCooperativeRecoveryUsesRecoveryBudget(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut, entered, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan() { rounds++; return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:1000,roundTimeoutMs:2000,stallTimeoutMs:20,recoveryTimeoutMs:80,candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 if(rounds>1) await fetch(%q+"/resolve", {signal});
 return {stream:{url:%q+"/audio"}};
}
`, server.URL, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	close(cut)
	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
waitRecoveryBudget:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-entered:
			break waitRecoveryBudget
		case <-timer.C:
			t.Fatal("没有进入自动恢复")
		}
	}
	select {
	case <-canceled:
	case <-timer.C:
		t.Fatal("恢复忽略了八十毫秒的独立总预算")
	}
	for p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("恢复截止后没有完成失败回执")
		}
	}
	if p.Playing() || p.Ready() || p.Err() == "" || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("恢复超时改变了手动点播项或队列：current=%+v error=%q", p.Current(), p.Err())
	}
}

// 无报错但解码持续等待时触发恢复，检测阶段占用同一个恢复总预算。
func TestCooperativeStallConsumesRecoveryBudget(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		// 不满一秒的真实音频会耗尽解码缓冲，连接保持打开且没有网络错误。
		w.Write(audio[:16384])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan() { rounds++; return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:2000,roundTimeoutMs:3000,stallTimeoutMs:400,recoveryTimeoutMs:600,candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 if(rounds>1) await fetch(%q+"/resolve", {signal});
 return {stream:{url:%q+"/audio"}};
}
`, server.URL, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
waitStall:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-entered:
			break waitStall
		case <-timer.C:
			t.Fatal("持续无进展未触发恢复")
		}
	}
	// 六百毫秒预算已用四百毫秒检测；留足调度余量仍不能再获得六百毫秒。
	remaining := time.NewTimer(350 * time.Millisecond)
	defer remaining.Stop()
	select {
	case <-canceled:
	case <-remaining.C:
		t.Fatal("停滞检测没有计入恢复总预算")
	}
	for p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("停滞恢复超时后未完成回执")
		}
	}
	if p.Playing() || p.Current().ID != "one" || len(p.Queue()) != 2 || p.Err() == "" {
		t.Fatalf("停滞恢复改变了当前项或队列：current=%+v error=%q", p.Current(), p.Err())
	}
}

// 普通连播经用户暂停续播后，本首恢复失败必须停留，不能继续沿用旧的自动跳歌意图。
func TestCooperativeManualResumeFailureStaysOnCurrentTrack(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function aggregate() { return {rounds}; }
export function playbackPlan() { rounds++; return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export function resolveCandidate() {
 if(rounds>1) throw new Error("recovery unavailable");
 return {stream:{url:%q}};
}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	p.TogglePlay()
	p.TogglePlay()
	close(cut)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("手动续播后的失败未结束")
		}
	}
	if p.Current().ID != "one" || len(p.Queue()) != 2 || p.Queue()[1].ID != "two" || p.Err() == "" {
		t.Fatalf("手动续播仍沿用自动跳歌意图：current=%+v queue=%+v error=%q", p.Current(), p.Queue(), p.Err())
	}
}

// 近结尾暂停仍是续播，不能仅凭位置接近时长就擅自从头播放。
func TestCooperativeNearEndPauseResumesPosition(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"one",quality:"flac"}]};}
export function resolveCandidate(){return {stream:{url:%s}};}
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	p.TogglePlay()
	// 暂停淡出完成后再定位，避免把尚未执行的定位命令误认为已经续播。
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	paused := false
	p.Changed = func() { paused = !p.Playing() }
	for !paused {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("暂停没有完成回执")
		}
	}
	p.Seek(4500 * time.Millisecond)
	before := p.Position()
	p.TogglePlay()
	if !p.Playing() || p.Current().ID != "one" || p.Position() < before {
		t.Fatalf("近结尾暂停续播意外从头播放：before=%v after=%v current=%+v", before, p.Position(), p.Current())
	}
	p.TogglePlay()
}

// 暂停期间不累计来源停滞；跨过检测阈值后续播也必须重新等待有效检测时间。
func TestCooperativePauseRestartsStallDetection(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:16384])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",stallTimeoutMs:400,recoveryTimeoutMs:1000,candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%q}}; }
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	p.TogglePlay()
	// 不处理暂停期间积压的回调，检查旧检测结果不能在续播后突然生效。
	time.Sleep(550 * time.Millisecond)
	p.TogglePlay()
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
waitFreshDetection:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			break waitFreshDetection
		}
	}
	if requests.Load() != 1 || p.Current().ID != "one" || !p.Playing() || !p.Ready() {
		t.Fatalf("暂停时间或旧检测回调导致提前恢复：requests=%d playing=%v current=%+v error=%q", requests.Load(), p.Playing(), p.Current(), p.Err())
	}
	p.TogglePlay()
}

// 完整下载中的损坏帧是来源错误，不能被音频层的结束回调冒充正常结束。
func TestCooperativeDecoderFailureRecoversWithoutAdvancing(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := make(chan struct{})
	damaged := append([]byte(nil), audio...)
	// 保留开播所需帧，在该固定夹具后续真实帧头处破坏同步字。
	clear(damaged[16913:16923])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(damaged)))
		w.Write(damaged[:16384])
		w.(http.Flusher).Flush()
		select {
		case <-corrupt:
			w.Write(damaged[16384:])
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0, visited=[];
export function aggregate() { return {rounds,visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 if(id!=="one") throw new Error("decoder error advanced queue");
 rounds++;
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"a",platform:rounds===1?"wy":"tx",trackID:"one",quality:"flac"}]};
}
export function resolveCandidate(){return {stream:{url:rounds===1?%q:%s}};}
`, server.URL, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() || p.Stream() == nil || p.Stream().Platform != "wy" {
		t.Fatalf("损坏帧尚未交付前已偏离首次开播：stream=%+v error=%q", p.Stream(), p.Err())
	}
	close(corrupt)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for p.Stream() == nil || p.Stream().Platform != "tx" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("解码错误没有恢复当前项：current=%+v stream=%+v error=%q", p.Current(), p.Stream(), p.Err())
		}
	}
	if p.Current().ID != "one" || len(p.Queue()) != 2 || !p.Playing() || p.Err() != "" {
		t.Fatalf("解码错误改变队列或播放意图：current=%+v error=%q", p.Current(), p.Err())
	}
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Visited) != 2 || state.Visited[0] != "one" || state.Visited[1] != "one" {
		t.Fatalf("解码错误伪装正常结束并访问其他队列项：visited=%v", state.Visited)
	}
	p.TogglePlay()
}

// 定位失败也是播放错误，不能被底层无原因的结束通知伪装成正常播完。
func TestCooperativeSeekFailureDoesNotAdvanceQueue(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	// 保留足够首段供真实开播，后段无有效帧，使流式定位无法建立解码器。
	clear(audio[len(audio)/2:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio)
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[];
export function aggregate(){return {visited};}
export function playbackPlan({id}) {
 visited.push(id);
 if(id!=="one") throw new Error("seek failure advanced queue");
 return {targetQuality:"128k",minimumQuality:"128k",candidates:[{id:"a",platform:visited.length===1?"wy":"tx",trackID:id,quality:"128k"}]};
}
export function resolveCandidate(){return {stream:{url:visited.length===1?%q:%s}};}
`, server.URL, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative", Duration: 5000}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() {
		t.Fatalf("完整首段未成功开播：%q", p.Err())
	}
	p.Seek(4 * time.Second)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for p.Stream() == nil || p.Stream().Platform != "tx" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("定位错误没有恢复当前项：current=%+v error=%q", p.Current(), p.Err())
		}
	}
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Visited) != 2 || state.Visited[0] != "one" || state.Visited[1] != "one" || p.Current().ID != "one" || len(p.Queue()) != 2 || !p.Playing() {
		t.Fatalf("定位错误推进了队列：visited=%v current=%+v error=%q", state.Visited, p.Current(), p.Err())
	}
	p.TogglePlay()
}

// 成功开播会截断连续失败链，之后的恢复失败不能继续累计开播之前的失败。
func TestCooperativeSuccessfulPlaybackResetsFailureChain(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[], current="", b=0;
export function aggregate(){return {visited};}
export function playbackPlan({id}) {
 visited.push(id); current=id;
 if(id==="b") b++;
 return {targetQuality:"flac",minimumQuality:"128k",consecutiveFailureLimit:3,candidates:[{id:"a",platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate() {
 if(current==="a" || current==="c" || current==="b" && b>1) throw new Error("unavailable");
 return {stream:{url:current==="b"?%q:%s}};
}
`, server.URL, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayQueue([]music.Track{{ID: "a", Source: "cooperative"}, {ID: "b", Source: "cooperative"}, {ID: "c", Source: "cooperative"}, {ID: "d", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Current().ID != "b" {
		t.Fatalf("未建立成功开播的失败链分界：current=%+v error=%q", p.Current(), p.Err())
	}
	close(cut)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for p.Current().ID == "b" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("中断失败后未完成队列处理")
		}
	}
	if p.Current().ID != "d" || !p.Ready() || !p.Playing() || p.Err() != "" {
		t.Fatalf("成功开播没有清零之前的连续失败：current=%+v error=%q", p.Current(), p.Err())
	}
	p.TogglePlay()
}

// 正常播完后的单曲循环是新一次播放，不能沿用上一遍已消耗的恢复机会。
func TestCooperativeNormalRepeatStartsNewRecoveryChance(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	path := filepath.Join(t.TempDir(), "repeat.flac")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	local, _ := json.Marshal("file:///" + filepath.ToSlash(path))
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function aggregate(){return {rounds};}
export function playbackPlan(){rounds++;return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:rounds===1?"wy":rounds===2?"tx":"kg",trackID:"one",quality:"flac"}]};}
export function resolveCandidate(){return {stream:{url:rounds===1?%q:rounds===2?%s:%s}};}
`, server.URL, local, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.CycleRepeat()
	p.CycleRepeat()
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	close(cut)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Stream() == nil || p.Stream().Platform != "tx" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("第一遍中断没有恢复")
		}
	}
	// 通过公开定位到结尾触发真实正常 EOF，不直接调用内部 ended 或改恢复状态。
	p.Seek(p.Duration())
	for p.Position() >= time.Second || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("没有正常循环到新一遍：position=%v error=%q", p.Position(), p.Err())
		}
	}
	// 正常结束已确认；只损坏临时文件的后段，下一遍才会遇到新的解码错误。
	clear(audio[150000:210000])
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	p.Seek(3 * time.Second)
	for p.Stream() == nil || p.Stream().Platform != "kg" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("正常循环未获得新恢复机会：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
		}
	}
	if !p.Playing() || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 1 {
		t.Fatalf("循环恢复改变了播放项：current=%+v error=%q", p.Current(), p.Err())
	}
	p.TogglePlay()
}

// 正常结束后手动从头重播属于新一次播放，应获得独立恢复机会。
func TestCooperativeCompletedReplayStartsNewRecoveryChance(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	path := filepath.Join(t.TempDir(), "repeat.flac")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	local, _ := json.Marshal("file:///" + filepath.ToSlash(path))
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function aggregate(){return {rounds};}
export function playbackPlan(){rounds++;return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:rounds===1?"wy":rounds===2?"tx":"kg",trackID:"one",quality:"flac"}]};}
export function resolveCandidate(){return {stream:{url:rounds===1?%q:rounds===2?%s:%s}};}
`, server.URL, local, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	close(cut)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Stream() == nil || p.Stream().Platform != "tx" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("第一遍中断没有恢复")
		}
	}
	// 通过公开定位到结尾触发真实正常 EOF，不直接调用内部 ended 或改恢复状态。
	p.Seek(p.Duration())
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("正常结束没有停止播放：position=%v error=%q", p.Position(), p.Err())
		}
	}
	p.TogglePlay()
	// 用户已从正常结束处重播；只损坏后段，新的解码错误属于下一遍。
	clear(audio[150000:210000])
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	p.Seek(3 * time.Second)
	for p.Stream() == nil || p.Stream().Platform != "kg" || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("正常结束后手动重播未获得新恢复机会：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
		}
	}
	if !p.Playing() || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 1 {
		t.Fatalf("重播恢复改变了播放项：current=%+v error=%q", p.Current(), p.Err())
	}
	p.TogglePlay()
}

// 自动恢复成功后再次断流必须结束本次播放；不能靠成功交接重新领取恢复机会。
func TestCooperativeSuccessfulRecoveryDoesNotReturnChance(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cuts := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(requests.Add(1)) - 1
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		if i < len(cuts) {
			select {
			case <-cuts[i]:
			case <-r.Context().Done():
			}
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[];
export function aggregate(){return {visited};}
export function playbackPlan({id}){visited.push(id);return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"one",quality:"flac"}]};}
export function resolveCandidate(){return {stream:{url:%q}};}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	close(cuts[0])
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for requests.Load() < 2 || !p.Ready() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("首轮自动恢复没有完成")
		}
	}
	if !p.Playing() {
		t.Fatalf("恢复未续播：%q", p.Err())
	}
	close(cuts[1])
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("第二次断流没有停止")
		}
	}
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || len(state.Visited) != 2 || state.Visited[0] != "one" || state.Visited[1] != "one" || p.Current().ID != "one" || len(p.Queue()) != 2 || p.Err() == "" {
		t.Fatalf("成功恢复返还机会或推进队列：requests=%d visited=%v current=%+v error=%q", requests.Load(), state.Visited, p.Current(), p.Err())
	}
}

// 重载解析尚未返回时的新定位必须保留，不能被重载前准备的旧位置覆盖。
func TestCooperativeReloadKeepsSeekDuringLoading(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan() { rounds++; return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 if(rounds===2) await fetch(%q,{signal});
 return {stream:{url:%s}};
}
`, server.URL, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.Restore(player.Session{
		Queue: []music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}},
		Order: []int{0, 1}, At: 0, Position: 1000,
	})
	finishLoading(t, p, posts)
	p.Reload()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
waitReloadSeek:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-entered:
			break waitReloadSeek
		case <-timer.C:
			t.Fatal("没有进入重载解析等待")
		}
	}
	p.Seek(3 * time.Second)
	close(release)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Playing() || p.Err() != "" || p.Position() < 2999*time.Millisecond || p.Position() > 3001*time.Millisecond || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("重载吞掉加载期间的新定位：position=%v ready=%v error=%q", p.Position(), p.Ready(), p.Err())
	}
}

// 播完后的重载保留结束位置；明确再次播放时才从头解析新设置，不能把末尾当中断定位。
func TestCooperativeReloadCompletedTrackWaitsForReplay(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let quality="flac";
export function aggregate() { quality="128k"; return {ok:true}; }
export function playbackPlan() { return {targetQuality:quality,minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality}]}; }
export function resolveCandidate() { return {stream:{url:quality==="flac"?%s:%s}}; }
`, audioPath(t, "seek.flac"), audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	p.Seek(p.Duration())
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("没有观察到真实正常结束")
		}
	}
	endedAt := p.Position()
	p.Reload()
	finishLoading(t, p, posts)
	if p.Playing() || p.Err() != "" || p.Position() < endedAt-time.Millisecond || p.Current().ID != "one" || len(p.Queue()) != 1 {
		t.Fatalf("结束处重载改变了结束状态：position=%v ended=%v error=%q", p.Position(), endedAt, p.Err())
	}
	if _, err := src.Aggregate("configure", nil); err != nil {
		t.Fatal(err)
	}
	p.TogglePlay()
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() || p.Err() != "" || p.Stream().Quality != "128k" || p.Position() >= time.Second {
		t.Fatalf("明确重播没有从头应用新设置：stream=%+v position=%v error=%q", p.Stream(), p.Position(), p.Err())
	}
}

// 正常结束后重载再指定位置属于定位续播，不能被完成态强制改成从头重播。
func TestCooperativeReloadCompletedTrackRespectsSeek(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%s}}; }
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	p.Seek(p.Duration())
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("没有观察到真实正常结束")
		}
	}
	p.Reload()
	finishLoading(t, p, posts)
	p.Seek(2 * time.Second)
	p.TogglePlay()
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() || p.Err() != "" || p.Position() < 1999*time.Millisecond || p.Position() >= 2500*time.Millisecond || p.Current().ID != "one" || len(p.Queue()) != 1 {
		t.Fatalf("完成态重载吞掉明确定位：position=%v ready=%v error=%q", p.Position(), p.Ready(), p.Err())
	}
}

// 同曲重新加载应用新设置，但不能返还本次播放已经用掉的自动恢复机会。
func TestCooperativeReloadKeepsUsedRecoveryChance(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cuts := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(requests.Add(1)) - 1
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		if i < len(cuts) {
			select {
			case <-cuts[i]:
			case <-r.Context().Done():
			}
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function aggregate() { return {rounds}; }
export function playbackPlan() { rounds++; return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"one",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%q}}; }
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	close(cuts[0])
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for requests.Load() < 2 || !p.Ready() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("首轮自动恢复没有完成：%q", p.Err())
		}
	}
	p.Reload()
	finishLoading(t, p, posts)
	if !p.Playing() || !p.Ready() || requests.Load() != 3 {
		t.Fatalf("重载没有重新打开当前项：requests=%d error=%q", requests.Load(), p.Err())
	}
	close(cuts[2])
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("重载后的第二次中断没有停止")
		}
	}
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Rounds int `json:"rounds"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Rounds != 3 || requests.Load() != 3 || p.Current().ID != "one" || len(p.Queue()) != 2 || p.Err() == "" {
		t.Fatalf("同曲重载返还恢复机会或改变队列：rounds=%d requests=%d queue=%+v error=%q", state.Rounds, requests.Load(), p.Queue(), p.Err())
	}
}

// 手动重载取消在途恢复，并使用新音质重新解析；旧轮迟到结果不能覆盖重载。
func TestCooperativeReloadCancelsRecoveryAndAppliesQuality(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cut, entered, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-cut:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0, quality="flac";
export function aggregate() { quality="128k"; return {ok:true}; }
export function playbackPlan() { rounds++; return {targetQuality:quality,minimumQuality:"128k",candidates:[
{id:"a",platform:quality==="flac"?"wy":"tx",trackID:"one",quality}]}; }
export async function resolveCandidate({signal}) {
 if(rounds===2) await fetch(%q+"/resolve",{signal});
 return {stream:{url:quality==="flac"?%q+"/audio":%s}};
}
`, server.URL, server.URL, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	close(cut)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
waitReloadRecovery:
	for {
		select {
		case fn := <-posts:
			fn()
		case <-entered:
			break waitReloadRecovery
		case <-timer.C:
			t.Fatal("没有进入待取消的恢复请求")
		}
	}
	if _, err := src.Aggregate("configure", nil); err != nil {
		t.Fatal(err)
	}
	p.Reload()
	select {
	case <-canceled:
	case <-timer.C:
		t.Fatal("同曲重载没有取消旧恢复的真实请求")
	}
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() || p.Err() != "" || p.Stream().Quality != "128k" || p.Stream().Platform != "tx" || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("重载未应用新音质或被旧恢复覆盖：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
	}
}

// 恢复中暂停取消真实请求；之后手动续播不能返还本次播放已用的恢复机会。
func TestCooperativeCanceledRecoveryDoesNotReturnChance(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	cuts := []chan struct{}{make(chan struct{}), make(chan struct{})}
	entered, canceled := make(chan struct{}), make(chan struct{})
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return
		}
		i := int(downloads.Add(1)) - 1
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		if i < len(cuts) {
			select {
			case <-cuts[i]:
			case <-r.Context().Done():
			}
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds = 0;
export function aggregate() { return {rounds}; }
export function playbackPlan() { rounds++; return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"raw-one",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 if(rounds===2) await fetch(%q + "/resolve", {signal});
 if(rounds>3) throw new Error("unexpected second automatic recovery");
 return {stream:{url:%q + "/audio"}};
}
`, server.URL, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	close(cuts[0])
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
waitRecovery:
	for {
		select {
		case <-entered:
			break waitRecovery
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("未进入恢复请求")
		}
	}
	p.TogglePlay()
	select {
	case <-canceled:
	case <-timer.C:
		t.Fatal("暂停未取消恢复请求")
	}
	if p.Playing() || p.Loading() || p.Current().ID != "one" || len(p.Queue()) != 2 || p.Err() != "" {
		t.Fatalf("暂停恢复改变了当前项：current=%+v error=%q", p.Current(), p.Err())
	}
	p.TogglePlay()
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() {
		t.Fatalf("手动续播失败：%q", p.Err())
	}
	close(cuts[1])
	for p.Playing() || p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("第二次中断未终止")
		}
	}
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Rounds int `json:"rounds"`
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Rounds != 3 || downloads.Load() != 2 || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("取消或手动续播返还了恢复机会：rounds=%d downloads=%d current=%+v", state.Rounds, downloads.Load(), p.Current())
	}
}

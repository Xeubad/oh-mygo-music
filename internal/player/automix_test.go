package player

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/automix"
	"mygo-music/internal/music"
)

type waitingAutoMixAdvisor struct {
	called   chan struct{}
	canceled chan struct{}
}

func (advisor waitingAutoMixAdvisor) Plan(ctx context.Context, request automix.Request) (automix.Decision, error) {
	close(advisor.called)
	<-ctx.Done()
	close(advisor.canceled)
	return automix.Decision{}, ctx.Err()
}

// 云端请求尚未返回时，手动切换队列要立即取消请求，不能让旧模型结果复活。
func TestAutoMixQueueChangeCancelsOptionalAdvisor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{"a": path, "b": path, "c": path}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		advisor := waitingAutoMixAdvisor{called: make(chan struct{}), canceled: make(chan struct{})}
		current.AutoMixAdvisor = func() automix.Planner { return advisor }
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		entered := false
		for step := 0; step < 1000; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
			select {
			case <-advisor.called:
				entered = true
			default:
			}
			if entered {
				break
			}
		}
		if !entered {
			t.Fatal("未进入可选 AI 分析")
		}
		current.PlayNext(music.Track{ID: "c", Source: "test"})
		synctest.Wait()
		select {
		case <-advisor.canceled:
		default:
			t.Fatal("改变队列没有取消旧云请求")
		}
		if current.Current().ID != "a" {
			t.Fatal("候选准备提前修改了当前歌曲")
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 在线流预分析不得吞样本；未知长度只能在正常 EOF 后变为可信结束帧。
func TestAutoMixBufferReplaysEveryStreamFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		samples := make([]float32, 10*automix.SampleRate*2)
		for index := range samples {
			samples[index] = float32(index%199-99) / 100
		}
		buffer := newAnalyzedTransitionBuffer(&transitionSamples{samples: samples, rate: 48000, unknown: true}, true)
		defer buffer.Close()
		buffer.ready()
		synctest.Wait()
		features, end, prefix := buffer.analysis()
		if end != 10*automix.SampleRate || features.End != end || !reflect.DeepEqual(prefix, samples) {
			t.Fatalf("预分析边界错误: end=%d features=%d prefix=%d", end, features.End, len(prefix))
		}
		var actual []float32
		chunk := make([]float32, 2186)
		for {
			count, err := buffer.Read(chunk)
			actual = append(actual, chunk[:count]...)
			if err == io.EOF {
				break
			}
			if err != nil || count == 0 {
				t.Fatalf("回放流失败: %d %v", count, err)
			}
		}
		if !reflect.DeepEqual(samples, actual) {
			t.Fatalf("分析吞掉音源帧: expected=%d actual=%d", len(samples), len(actual))
		}
	})
}

type autoMixTestSource struct {
	transitionSource
	files map[string]string
}

func (source *autoMixTestSource) Stream(id, quality string) (*music.Stream, error) {
	source.mu.Lock()
	if source.requests == nil {
		source.requests = map[string]int{}
	}
	source.requests[id]++
	source.mu.Unlock()
	return &music.Stream{URL: music.FileURL(source.files[id])}, nil
}

type failingAutoMixAdvisor struct{}

func (failingAutoMixAdvisor) Plan(context.Context, automix.Request) (automix.Decision, error) {
	return automix.Decision{}, errors.New("synthetic offline advisor")
}

type gatedAutoMixAdvisor struct {
	called  chan struct{}
	release chan struct{}
}

func (advisor gatedAutoMixAdvisor) Plan(ctx context.Context, request automix.Request) (automix.Decision, error) {
	close(advisor.called)
	select {
	case <-advisor.release:
		decision, _ := request.LocalDecision()
		return decision, nil
	case <-ctx.Done():
		return automix.Decision{}, ctx.Err()
	}
}

// 模型结果已排队但尚未应用时，取消增强仍必须使其失效，本机方案继续工作。
func TestAutoMixCancellationRejectsAlreadyPostedAdvisorDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{"a": path, "b": path}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		advisor := gatedAutoMixAdvisor{called: make(chan struct{}), release: make(chan struct{})}
		current.AutoMixAdvisor = func() automix.Planner { return advisor }
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		entered := false
		for step := 0; step < 1000; step++ {
			advanceTransitionPlayer(current, posted, 480)
			select {
			case <-advisor.called:
				entered = true
			default:
			}
			if entered {
				break
			}
		}
		if !entered || current.AutoMixStatus() != "local" {
			t.Fatalf("AI 返回前本机方案未就绪: entered=%v status=%s", entered, current.AutoMixStatus())
		}
		close(advisor.release)
		synctest.Wait()
		current.CancelAutoMixEnhancement()
		pumpTransitionPosts(posted)
		if current.AutoMixStatus() != "local" {
			t.Fatal("已取消的云结果覆盖了本机方案")
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 不配置 AI 或 AI 失败时，本机分析都必须在自然曲尾前完成动态交接，设备与历史各保持一次。
func TestAutoMixPlayerWorksLocallyAndIgnoresAdvisorFailure(t *testing.T) {
	for _, useAdvisor := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "cloud-offline"}[useAdvisor], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				current, posted, _ := transitionPlayerFixture(t)
				source := &autoMixTestSource{files: map[string]string{
					"a": autoMixSongFixture(t, 120, .1),
					"b": autoMixSongFixture(t, 124, .15),
				}}
				current.Source = func(string) music.Source { return source }
				current.Transitions = func() TransitionSettings {
					return TransitionSettings{AutoMix: true}
				}
				if useAdvisor {
					current.AutoMixAdvisor = func() automix.Planner { return failingAutoMixAdvisor{} }
				}
				current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
				for range 20 {
					synctest.Wait()
					pumpTransitionPosts(posted)
				}
				original := current.out
				for step := 0; step < 1600 && current.Current().ID != "b"; step++ {
					advanceTransitionPlayer(current, posted, 480)
				}
				if current.Current().ID != "b" || current.out != original {
					t.Fatalf("本机 AutoMix 没有连续交接: current=%+v same=%v status=%s", current.Current(), current.out == original, current.AutoMixStatus())
				}
				if position := original.Position(); position >= 16*time.Second {
					t.Fatalf("AutoMix 没有提前融接: %v", position)
				}
				if current.AutoMixStatus() != "local" {
					t.Fatalf("AI 不可用影响本机计划: %s", current.AutoMixStatus())
				}
				synctest.Wait()
				if records := source.records(); !reflect.DeepEqual(records, []string{"a", "b"}) {
					t.Fatalf("交接记录重复或提前: %v", records)
				}
				if current.Position() < 0 || current.Position() > time.Second {
					t.Fatalf("变速首帧源时间映射错误: %v", current.Position())
				}
				current.stop()
				time.Sleep(200 * time.Millisecond)
				pumpTransitionPosts(posted)
			})
		})
	}
}

// 连续三首都使用本机计算，第三首不能混入上一轮尾轨，也不能重新建立设备输出。
func TestAutoMixThreeSongsKeepOneOutputAndSourceClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		source := &autoMixTestSource{files: map[string]string{
			"a": autoMixSongFixture(t, 120, .1),
			"b": autoMixSongFixture(t, 124, .15),
			"c": autoMixSongFixture(t, 122, .2),
		}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}, {ID: "c", Source: "test"}}, 0)
		for range 20 {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		original := current.out
		for step := 0; step < 3200 && current.Current().ID != "c"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "c" || current.out != original || current.AutoMixStatus() != "local" {
			t.Fatalf("连续交接中断: current=%+v same=%v status=%s", current.Current(), current.out == original, current.AutoMixStatus())
		}
		synctest.Wait()
		if records := source.records(); !reflect.DeepEqual(records, []string{"a", "b", "c"}) {
			t.Fatalf("三曲开播记录不正确: %v", records)
		}
		if position := current.Position(); position < 0 || position > time.Second {
			t.Fatalf("第三曲沿用了上一轮时间轴: %v", position)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 待接曲加载失败时保留 A 的输出和记录，只在后备 C 真正可听时交接。
func TestAutoMixFailedNextKeepsCurrentAndRecordsOnlyAudibleFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		path := autoMixSongFixture(t, 120, .1)
		source := &autoMixTestSource{files: map[string]string{
			"a": path,
			"b": filepath.Join(t.TempDir(), "unavailable.wav"),
			"c": path,
		}}
		current.Source = func(string) music.Source { return source }
		current.Transitions = func() TransitionSettings { return TransitionSettings{AutoMix: true} }
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}, {ID: "c", Source: "test"}}, 0)
		for range 20 {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		original := current.out
		for range 600 {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "a" || current.out != original || !current.Playing() {
			t.Fatal("加载失败影响了正在播放的 A")
		}
		synctest.Wait()
		if records := source.records(); !reflect.DeepEqual(records, []string{"a"}) {
			t.Fatalf("准备期间提前记录了待接曲: %v", records)
		}
		for step := 0; step < 1000 && current.Current().ID != "c"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "c" || current.out != original {
			t.Fatalf("没有在同一输出交接后备 C: %+v", current.Current())
		}
		synctest.Wait()
		if records := source.records(); !reflect.DeepEqual(records, []string{"a", "c"}) {
			t.Fatalf("失败曲或后备曲记账不正确: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

func autoMixSongFixture(t *testing.T, bpm, phase float64) string {
	t.Helper()
	const frames = 16 * automix.SampleRate
	data := make([]byte, 44+frames*4)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 2)
	binary.LittleEndian.PutUint32(data[24:], automix.SampleRate)
	binary.LittleEndian.PutUint32(data[28:], automix.SampleRate*4)
	binary.LittleEndian.PutUint16(data[32:], 4)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], frames*4)
	period := 60 / bpm
	for frame := range frames {
		at := float64(frame) / automix.SampleRate
		distance := math.Mod(at-phase+period, period)
		value := .025 * math.Sin(2*math.Pi*330*at)
		if at >= phase && distance < .09 {
			value += .7 * math.Exp(-distance*55) * (math.Sin(2*math.Pi*82*at) + .3*math.Sin(2*math.Pi*1700*at))
		}
		binary.LittleEndian.PutUint16(data[44+frame*4:], uint16(int16(value*32000)))
		binary.LittleEndian.PutUint16(data[46+frame*4:], uint16(int16(value*25600)))
	}
	path := filepath.Join(t.TempDir(), "rhythm.wav")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// 变速后的前段接回原始流时不能重复播放；映射终点必须回到原始歌曲时间。
func TestAutoMixBufferInstallsPreparedIntroOnlyBeforeConsumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		samples := make([]float32, 18*automix.SampleRate*2)
		for index := range samples {
			samples[index] = float32(index%99) / 200
		}
		buffer := newAnalyzedTransitionBuffer(&transitionSamples{samples: samples, rate: 48000}, true)
		defer buffer.Close()
		buffer.ready()
		synctest.Wait()
		_, _, prefix := buffer.analysis()
		intro, mapping, err := automix.StretchIntro(prefix, 1.03, 2*automix.SampleRate)
		if err != nil {
			t.Fatal(err)
		}
		if !buffer.installIntro(intro, mapping, len(prefix)) {
			t.Fatal("已准备的变速前段没有安装")
		}
		var actual []float32
		chunk := make([]float32, 2186)
		for {
			count, err := buffer.Read(chunk)
			actual = append(actual, chunk[:count]...)
			if err == io.EOF {
				break
			}
			if err != nil || count == 0 {
				t.Fatalf("变速回放失败: %d %v", count, err)
			}
		}
		expected := append(append([]float32(nil), intro...), samples[len(prefix):]...)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("前段回放重复或丢帧: actual=%d expected=%d", len(actual), len(expected))
		}
		if frame := buffer.sourceFrame(int64(len(actual) / 2)); frame != int64(len(samples)/2) {
			t.Fatalf("变速回放源时钟不完整: %d", frame)
		}
		if buffer.installIntro(intro, mapping, len(prefix)) {
			t.Fatal("已经消费的下一首接受了迟到计划")
		}
	})
}

// 所有现有在线音频格式在下载尚未完成时也能开始本机分析，不等整首静音扫描。
func TestAutoMixAnalyzesIncompleteStreamFormats(t *testing.T) {
	for _, name := range []string{"seek.mp3", "seek.flac", "seek.ogg", "seek.aac", "seek-aac.m4a", "seek-aac-frag.m4a", "seek.m4a", "seek-flac-frag.m4a"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				download := downloaded(readFixture(t, name))
				download.done = false
				complete := func() {
					download.mu.Lock()
					download.done = true
					download.cond.Broadcast()
					download.mu.Unlock()
				}
				defer complete()
				decoded, err := openStream(download, 600000)
				if err != nil {
					t.Fatal(err)
				}
				buffer := newAnalyzedTransitionBuffer(newTransitionDecoder(decoded), true)
				defer buffer.Close()
				chunk := make([]float32, 4096)
				count, err := buffer.Read(chunk)
				if err != nil || count <= 0 {
					t.Fatalf("在线首段无法分析/回放: %d %v", count, err)
				}
				features, _, _ := buffer.analysis()
				if features.End <= 0 || len(features.Points) == 0 {
					t.Fatal("未完整下载的音源没有生成本机特征")
				}
				complete()
				for {
					amount, err := buffer.Read(chunk)
					if err == io.EOF {
						break
					}
					if err != nil || amount == 0 {
						t.Fatalf("在线流完成后不能继续: %d %v", amount, err)
					}
				}
			})
		})
	}
}

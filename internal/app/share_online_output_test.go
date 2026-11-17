package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 重新打开分享面板后仍复用服务端未过期的同一快照，不能再次下载或上传音频。
func TestOnlineCardReusesShareAfterReopening(t *testing.T) {
	a, tester, posted, uploads := onlineCardOutputApp(t)
	previous := copyText
	var copied string
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = previous }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	track := a.sharing.track
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return copied != "" && len(a.sharing.png) > 0 })
	first := copied
	if err := tester.Click("Close Share Card"); err != nil {
		t.Fatal(err)
	}
	a.openShare(track)
	until(t, posted, func() bool { tester.Frame(); return a.sharing != nil && len(a.sharing.png) > 0 })
	delete(a.library.caps, "stream")
	fake(a).stream = func(string, string) (*music.Stream, error) {
		return nil, errors.New("reuse must not need the original audio")
	}
	copied = ""
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return copied != "" || a.sharing.onlineError != nil })
	if copied != first || uploads.Load() != 1 {
		t.Fatalf("reopened share = %q, want %q; uploads=%d; error=%v", copied, first, uploads.Load(), a.sharing.onlineError)
	}
}

// 已复制过的链接也必须重新验证，服务端判定失效时重新发布，不能沿用旧地址。
func TestOnlineCardReplacesExpiredOrRevokedLink(t *testing.T) {
	var unavailable atomic.Bool
	a, tester, posted, uploads := onlineCardOutputApp(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if unavailable.Load() && request.URL.Path == "/api/shares/reuse" {
			writer.WriteHeader(http.StatusNoContent)
			return true
		}
		return false
	})
	previous := copyText
	var copied string
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = previous }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return copied != "" && len(a.sharing.png) > 0 })
	unavailable.Store(true)
	copied = ""
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return copied != "" && len(a.sharing.png) > 0 })
	if copied != "https://share.example/s/listening-2" || uploads.Load() != 2 {
		t.Fatalf("invalidated link reused: link=%q uploads=%d", copied, uploads.Load())
	}
}

// 查询失败要恢复操作并允许重试，不能复制缓存旧链接或为网络错误重复上传。
func TestOnlineCardReuseFailureRestoresControlsWithoutCopyingStaleLink(t *testing.T) {
	var fail atomic.Bool
	a, tester, posted, uploads := onlineCardOutputApp(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if fail.Load() && request.URL.Path == "/api/shares/reuse" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(writer, `{"error":"temporarily_unavailable"}`)
			return true
		}
		return false
	})
	previous := copyText
	var copied string
	copyText = func(text string) error { copied = text; return nil }
	defer func() { copyText = previous }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return copied != "" && len(a.sharing.png) > 0 })
	first := copied
	fail.Store(true)
	copied = ""
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.onlineError != nil && !a.sharing.onlinePublishing })
	if copied != "" || uploads.Load() != 1 {
		t.Fatal("failed reuse copied a stale link or uploaded audio again")
	}
	fail.Store(false)
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return copied != "" })
	if copied != first || uploads.Load() != 1 {
		t.Fatal("retry did not reuse the original live link")
	}
}

// 已配置服务时，复制卡片必须先上传音频，并让最终 PNG 的二维码指向听歌页。
func TestCopyShareCardPublishesAndEncodesListeningLink(t *testing.T) {
	a, tester, posted, uploads := onlineCardOutputApp(t)
	copied := make(chan []byte, 1)
	previous := copyImage
	copyImage = func(data []byte) error { copied <- data; return nil }
	defer func() { copyImage = previous }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.status == "Image copied" })
	data := wait(t, copied, "published card image")
	if uploads.Load() != 1 {
		t.Fatalf("audio uploads = %d, want one", uploads.Load())
	}
	assertCardListeningQR(t, data, "https://share.example/s/listening-1")
}

// 上传失败不能复制原曲二维码，失败解除后同一个复制动作仍能正常重试。
func TestOnlineCardCopyRetriesAfterAudioFailure(t *testing.T) {
	a, tester, posted, uploads := onlineCardOutputApp(t)
	copied := make(chan []byte, 1)
	previous := copyImage
	copyImage = func(data []byte) error { copied <- data; return nil }
	defer func() { copyImage = previous }()
	stream := fake(a).stream
	fake(a).stream = func(string, string) (*music.Stream, error) { return nil, errors.New("audio unavailable") }
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.onlineError != nil && !a.sharing.onlinePublishing })
	select {
	case <-copied:
		t.Fatal("upload failure copied an unrelated card image")
	default:
	}
	fake(a).stream = stream
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.status == "Image copied" })
	assertCardListeningQR(t, wait(t, copied, "retried card"), "https://share.example/s/listening-1")
	if uploads.Load() != 1 {
		t.Fatalf("uploads = %d", uploads.Load())
	}
}

// 生成中不能退出、改选或重复导出，Escape 和点击遮罩也必须保持锁定。
func TestOnlineCardLoadingBlocksInteractionUntilFinished(t *testing.T) {
	a, tester, posted, uploads := onlineCardOutputApp(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	stream := fake(a).stream
	fake(a).stream = func(id, quality string) (*music.Stream, error) {
		started <- struct{}{}
		<-release
		return stream(id, quality)
	}
	copied := make(chan struct{}, 1)
	previous := copyImage
	copyImage = func([]byte) error { copied <- struct{}{}; return nil }
	defer func() { copyImage = previous }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	wait(t, started, "audio request")
	tester.Frame()
	for _, control := range []string{"Cancel Upload", "Close Share Card", "Copy Lyrics", "Copy Image", "Audio Quality"} {
		if err := tester.Click(control); err == nil {
			t.Fatalf("%s remained interactive during generation", control)
		}
	}
	tester.Key(0, ui.KeyEscape)
	tester.ClickAt(2, 2)
	tester.Frame()
	if a.sharing == nil || !a.sharing.open || !a.sharing.onlinePublishing {
		t.Fatal("loading modal was dismissed")
	}
	unblock()
	until(t, posted, func() bool { tester.Frame(); return a.sharing.status == "Image copied" })
	wait(t, copied, "card after loading")
	if uploads.Load() != 1 {
		t.Fatalf("uploads=%d, want one", uploads.Load())
	}
	if err := tester.Click("Close Share Card"); err != nil {
		t.Fatalf("dialog did not restore controls: %v", err)
	}
}

// 连历史查询阶段也要锁住底层点击与快捷键，查询失败后恢复面板而不触发任何导出。
func TestOnlineCardLookupLoadingBlocksUnderlyingActions(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	a, _, posted, uploads := onlineCardOutputApp(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/api/shares/reuse" {
			return false
		}
		started <- struct{}{}
		<-release
		writer.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(writer, `{"error":"temporarily_unavailable"}`)
		return true
	})
	underlyingActions := 0
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		context.OnShortcut(0, ui.KeySpace, func() { underlyingActions++ })
		if ui.Button(context, "Underlying action").Clicked() {
			underlyingActions++
		}
		a.shareDialog(context)
	}, 1200, 850)
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	wait(t, started, "reuse lookup")
	tester.Frame()
	tester.Key(0, ui.KeySpace)
	tester.Key(0, ui.KeyEscape)
	tester.ClickAt(10, 10)
	tester.Frame()
	if underlyingActions != 0 || a.sharing == nil || !a.sharing.open || !a.sharing.onlinePublishing {
		t.Fatalf("lookup modal leaked input: underlyingActions=%d", underlyingActions)
	}
	unblock()
	until(t, posted, func() bool { tester.Frame(); return !a.sharing.onlinePublishing })
	if uploads.Load() != 0 || a.sharing.pendingOutput != shareOutputNone {
		t.Fatal("lookup failure started an export")
	}
	if err := tester.Click("Close Share Card"); err != nil {
		t.Fatalf("failed lookup did not restore controls: %v", err)
	}
}

// 点击一次复制听歌链接即可发布；之后保存 PNG 复用同一分享，不重复上传。
func TestCopyListenLinkAndSaveCardReusePublishedShare(t *testing.T) {
	a, tester, posted, uploads := onlineCardOutputApp(t)
	var link string
	oldCopy := copyText
	copyText = func(text string) error { link = text; return nil }
	defer func() { copyText = oldCopy }()
	path := filepath.Join(t.TempDir(), "online-card.png")
	oldDialog := saveCardAs
	saveCardAs = func(*app, string, string) (string, error) { return path, nil }
	defer func() { saveCardAs = oldDialog }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if uploads.Load() != 0 {
		t.Fatal("opening the share dialog uploaded audio without an action")
	}
	if err := tester.Click("Copy Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return link != "" && len(a.sharing.png) > 0 })
	if link != "https://share.example/s/listening-1" {
		t.Fatalf("copied link %q", link)
	}
	if err := tester.Click("Save PNG"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.status == "Image saved" })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertCardListeningQR(t, data, link)
	if uploads.Load() != 1 {
		t.Fatalf("copied link and image caused %d uploads", uploads.Load())
	}
}

// 原曲目标不触发上传，选句变化后的在线导出必须重新发布当前快照。
func TestCardLinkTargetAndChangedSelectionKeepCorrectQRCode(t *testing.T) {
	a, tester, posted, uploads := onlineCardOutputApp(t)
	copied := make(chan []byte, 3)
	previous := copyImage
	copyImage = func(data []byte) error { copied <- data; return nil }
	defer func() { copyImage = previous }()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Card Link"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Original Song Page"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return !a.sharing.exporting })
	assertCardListeningQR(t, wait(t, copied, "original card"), "https://catalog.example/song/old")
	if uploads.Load() != 0 {
		t.Fatal("original-song card uploaded audio")
	}
	if err := tester.Click("Card Link"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Online Listening Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.status == "Image copied" })
	assertCardListeningQR(t, wait(t, copied, "online card"), "https://share.example/s/listening-1")
	if err := tester.Click("Audio Quality"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Lossless"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 && !a.sharing.rendering })
	if err := tester.Click("Copy Image"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.status == "Image copied" })
	assertCardListeningQR(t, wait(t, copied, "updated card"), "https://share.example/s/listening-2")
	if uploads.Load() != 2 {
		t.Fatalf("updated snapshot uploads = %d, want two", uploads.Load())
	}
}

func onlineCardOutputApp(t *testing.T, intercept ...func(http.ResponseWriter, *http.Request) bool) (*app, *ui.Tester, chan func(), *atomic.Int32) {
	t.Helper()
	var uploads atomic.Int32
	var serviceMu sync.Mutex
	var snapshot string
	ready := make(map[string]string)
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serviceMu.Lock()
		defer serviceMu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if len(intercept) > 0 && intercept[0](writer, request) {
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/shares/reuse":
			data, _ := io.ReadAll(request.Body)
			if result, ok := ready[string(data)]; ok {
				io.WriteString(writer, result)
			} else {
				writer.WriteHeader(http.StatusNoContent)
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/shares":
			data, _ := io.ReadAll(request.Body)
			snapshot = string(data)
			writer.WriteHeader(http.StatusCreated)
			json.NewEncoder(writer).Encode(shareResult{ID: "card-1", Status: "awaiting_upload"})
		case request.Method == http.MethodPut && request.URL.Path == "/api/shares/card-1/audio":
			io.Copy(io.Discard, request.Body)
			uploads.Add(1)
			writer.WriteHeader(http.StatusAccepted)
			result, _ := json.Marshal(shareResult{ID: "card-1", Status: "ready", URL: fmt.Sprintf("https://share.example/s/listening-%d", uploads.Load())})
			ready[snapshot] = string(result)
			writer.Write(result)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(service.Close)
	previous := getShareServiceSecret
	getShareServiceSecret = func(string) ([]byte, error) { return []byte("card-test-token"), nil }
	t.Cleanup(func() { getShareServiceSecret = previous })
	audio := filepath.Join(t.TempDir(), "audio.mp3")
	if err := os.WriteFile(audio, []byte("test audio copy"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp()
	a.settings.ShareServerURL = service.URL
	fake(a).lyrics = func(string) (*music.Lyrics, error) { return &music.Lyrics{}, nil }
	fake(a).stream = func(string, string) (*music.Stream, error) {
		return &music.Stream{URL: "file:///" + filepath.ToSlash(audio), Type: "mp3", Quality: "high"}, nil
	}
	posted := posts(a)
	a.openShare(music.Track{ID: "card-song", Source: "fake", Name: "Online card", Duration: 12000, URL: "https://catalog.example/song/old"})
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		a.shareDialog(context)
	}, 1200, 850)
	tester.Frame()
	return a, tester, posted, &uploads
}

func assertCardListeningQR(t *testing.T, data []byte, expected string) {
	t.Helper()
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	binary, err := gozxing.NewBinaryBitmapFromImage(image)
	if err != nil {
		t.Fatal(err)
	}
	result, err := qrcode.NewQRCodeReader().Decode(binary, nil)
	if err != nil || result.GetText() != expected {
		t.Fatalf("card QR = %v, error=%v, want %q", result, err, expected)
	}
}

// 无窗口保存在线卡片选择与导出入口，供目视检查桌面及紧凑布局。
func TestShareOnlineCardLinkFrame(t *testing.T) {
	directory := os.Getenv("MM_FRAMES")
	if directory == "" {
		t.Skip("set MM_FRAMES for visual acceptance")
	}
	previousLanguage := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(previousLanguage)
	a := newTestApp()
	posted := posts(a)
	a.settings.ShareServerURL = "https://music.zxbdwy.online"
	a.sharing = &shareDraft{
		open: true, onlineMode: "full", onlineQuality: "high", cardOnline: true,
		track:    music.Track{ID: "card-preview", Source: "fake", Name: "把这一刻，唱给你听", Artists: []music.Artist{{Name: "Oh MyGo Music"}}, Duration: 12000},
		selected: map[int]bool{0: true, 2: true},
		cover:    coverImage(ui.RGB(39, 75, 95), ui.RGB(160, 103, 121)),
		lines: []lyrics.Line{
			{Text: "让风慢慢经过", Translation: "Let the evening wind pass softly.", At: time.Second, End: 3 * time.Second},
			{Text: "你会听见远方", At: 4 * time.Second, End: 7 * time.Second},
			{Text: "我们一起出发", Translation: "Let's set out together.", At: 8 * time.Second, End: 11 * time.Second},
		},
	}
	for _, size := range []struct {
		width, height int
		name          string
	}{
		{1200, 850, "online-card-link-desktop.png"}, {760, 640, "online-card-link-compact.png"},
	} {
		tester := ui.NewTester(func(context *ui.Context) { a.pal = paletteFor(context.Theme()); a.shareDialog(context) }, size.width, size.height)
		tester.SetDark(true)
		tester.Frame()
		a.requestShareRender()
		until(t, posted, func() bool { tester.Frame(); return a.sharing.preview != nil })
		settled(tester)
		saveFrame(t, directory, size.name, tester.Image())
		a.sharing.onlinePublishing = true
		a.sharing.status = T("Checking existing shares…")
		tester.Frame()
		saveFrame(t, directory, "loading-"+size.name, tester.Image())
		a.sharing.onlinePublishing = false
		tester.Frame()
	}
}

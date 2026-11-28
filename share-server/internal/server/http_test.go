package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	testToken     = "test-token-0001"
	testOrigin    = "http://127.0.0.1:8080"
	testMusicBody = "ID3\x04\x00\x00\x00\x00\x00\x00"
)

type stubTranscoder struct {
	clip    *Clip
	demuxer string
	err     error
	calls   int
}

func (s *stubTranscoder) Transcode(ctx context.Context, input AudioInput, clip *Clip, dst string) error {
	s.calls++
	s.demuxer = input.Demuxer
	if clip != nil {
		copied := *clip
		s.clip = &copied
	}
	if s.err != nil {
		return s.err
	}
	return os.WriteFile(dst, []byte(testMusicBody), 0o600)
}

func newTestServer(t *testing.T) (*Server, *Store, *stubTranscoder) {
	t.Helper()
	store, err := OpenStore(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	transcoder := &stubTranscoder{}
	server, err := New(Config{
		Listen:       "127.0.0.1:0",
		PublicOrigin: testOrigin,
		Token:        testToken,
		TTL:          time.Hour,
	}, Options{Store: store, Transcoder: transcoder})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server, store, transcoder
}

func do(handler http.Handler, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func adminHeaders(extra map[string]string) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + testToken,
		"Origin":        testOrigin,
	}
	for key, value := range extra {
		headers[key] = value
	}
	return headers
}

func jsonBody(t *testing.T, metadata Metadata) []byte {
	t.Helper()
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, wantStatus, recorder.Body.String())
	}
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return value
}

func createShare(t *testing.T, handler http.Handler, metadata Metadata, key string) shareResult {
	t.Helper()
	recorder := do(handler, "POST", "/api/shares", jsonBody(t, metadata), adminHeaders(map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": key,
	}))
	return decode[shareResult](t, recorder, http.StatusCreated)
}

func uploadAudio(t *testing.T, handler http.Handler, id string, contentType string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	return do(handler, "PUT", "/api/shares/"+id+"/audio", []byte(testMusicBody), adminHeaders(map[string]string{
		"Content-Type": contentType,
	}))
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 200, G: 40, B: 90, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// A published share exposes playback and lyrics, not the source identity or URL.
func TestFullShareLifecycle(t *testing.T) {
	server, store, _ := newTestServer(t)
	handler := server.Handler()
	cover := testPNG(t)

	metadata := testMetadata()
	metadata.AccountID = "acct-secret"
	metadata.PlaybackID = "playback-secret"
	metadata.SourceID = "source-secret"
	metadata.TrackID = "track-secret"
	metadata.SourceURL = "https://example.invalid/private-source-url"
	metadata.CoverHash = sha256Hex(cover)
	metadata.Lyrics = []LyricLine{{
		Text: "第一句", Translation: "first line", AtMs: 1000, EndMs: 3000,
		Words: []LyricWord{{Text: "第", StartMs: 1000, EndMs: 2000}},
	}}
	body := jsonBody(t, metadata)
	headers := adminHeaders(map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": "idem-key-0001",
	})

	created := decode[shareResult](t, do(handler, "POST", "/api/shares", body, headers), http.StatusCreated)
	if created.Status != statusAwaitingUpload || created.URL != "" {
		t.Fatalf("created = %+v, want an awaiting share without a URL", created)
	}
	repeat := decode[shareResult](t, do(handler, "POST", "/api/shares", body, headers), http.StatusOK)
	if repeat.ID != created.ID {
		t.Fatalf("idempotent retry returned %q, want %q", repeat.ID, created.ID)
	}

	list := decode[struct {
		Shares []shareListItem `json:"shares"`
	}](t, do(handler, "GET", "/api/shares", nil, adminHeaders(nil)), http.StatusOK)
	if len(list.Shares) != 1 || list.Shares[0].ID != created.ID {
		t.Fatalf("list = %+v, want the created share", list.Shares)
	}

	coverRecorder := do(handler, "PUT", "/api/shares/"+created.ID+"/cover", cover, adminHeaders(map[string]string{
		"Content-Type": "image/png",
	}))
	if coverRecorder.Code != http.StatusNoContent {
		t.Fatalf("cover status = %d, want 204 (body %s)", coverRecorder.Code, coverRecorder.Body.String())
	}

	published := decode[shareResult](t, uploadAudio(t, handler, created.ID, "audio/mpeg", http.StatusOK), http.StatusOK)
	if published.Status != statusReady {
		t.Fatalf("status = %q, want ready", published.Status)
	}
	if want := testOrigin + "/s/" + created.ID; published.URL != want {
		t.Errorf("URL = %q, want %q", published.URL, want)
	}
	if published.Expires == "" {
		t.Error("ready share has no expiresAt")
	}

	got := decode[shareResult](t, do(handler, "GET", "/api/shares/"+created.ID, nil, adminHeaders(nil)), http.StatusOK)
	if got.Status != statusReady {
		t.Errorf("admin GET status = %q, want ready", got.Status)
	}

	public := do(handler, "GET", "/api/public/"+created.ID, nil, nil)
	if public.Code != http.StatusOK {
		t.Fatalf("public JSON status = %d, want 200 (body %s)", public.Code, public.Body.String())
	}
	for _, secret := range []string{"source-secret", "acct-secret", "track-secret", "playback-secret", "coverHash", "sourceId", "accountId", "sourceUrl", "private-source-url"} {
		if strings.Contains(public.Body.String(), secret) {
			t.Errorf("public JSON leaks %q: %s", secret, public.Body.String())
		}
	}
	var publicShare PublicShare
	if err := json.Unmarshal(public.Body.Bytes(), &publicShare); err != nil {
		t.Fatal(err)
	}
	if len(publicShare.Lyrics) != 1 || publicShare.Lyrics[0].AtMs != 1000 {
		t.Errorf("full share lyrics = %+v, want times unchanged", publicShare.Lyrics)
	}

	page := do(handler, "GET", "/s/"+created.ID, nil, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("page status = %d, want 200 (body %s)", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "private-source-url") {
		t.Error("listening page exposed the original source URL")
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "nonce-") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want a nonce-scoped policy", csp)
	}
	if page.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("page is missing nosniff")
	}
	if !strings.Contains(page.Body.String(), "/s/"+created.ID+"/audio") {
		t.Error("page does not point at the audio route")
	}

	rangeRequest := httptest.NewRequest("GET", "/s/"+created.ID+"/audio", nil)
	rangeRequest.Header.Set("Range", "bytes=0-1")
	rangeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rangeRecorder, rangeRequest)
	if rangeRecorder.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rangeRecorder.Code)
	}
	if rangeRecorder.Body.String() != "ID" {
		t.Errorf("range body = %q, want %q", rangeRecorder.Body.String(), "ID")
	}
	if contentType := rangeRecorder.Header().Get("Content-Type"); contentType != "audio/mpeg" {
		t.Errorf("audio Content-Type = %q, want audio/mpeg", contentType)
	}

	publicCover := do(handler, "GET", "/s/"+created.ID+"/cover", nil, nil)
	if publicCover.Code != http.StatusOK || publicCover.Header().Get("Content-Type") != "image/png" {
		t.Errorf("cover = %d %q, want 200 image/png", publicCover.Code, publicCover.Header().Get("Content-Type"))
	}

	reused := decode[shareResult](t, do(handler, "POST", "/api/shares/reuse", body, adminHeaders(map[string]string{
		"Content-Type": "application/json",
	})), http.StatusOK)
	if reused.ID != created.ID {
		t.Errorf("reuse ID = %q, want %q", reused.ID, created.ID)
	}
	other := testMetadata()
	other.Title = "另一首"
	miss := do(handler, "POST", "/api/shares/reuse", jsonBody(t, other), adminHeaders(map[string]string{
		"Content-Type": "application/json",
	}))
	if miss.Code != http.StatusNoContent || miss.Body.Len() != 0 {
		t.Errorf("reuse miss = %d %q, want 204 with no body", miss.Code, miss.Body.String())
	}

	if recorder := do(handler, "DELETE", "/api/shares/"+created.ID, nil, adminHeaders(nil)); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", recorder.Code)
	}
	for _, target := range []string{"/s/" + created.ID, "/api/public/" + created.ID, "/s/" + created.ID + "/audio", "/s/" + created.ID + "/cover"} {
		if recorder := do(handler, "GET", target, nil, nil); recorder.Code != http.StatusNotFound {
			t.Errorf("%s after revoke = %d, want 404", target, recorder.Code)
		}
	}
	if got := decode[shareResult](t, do(handler, "GET", "/api/shares/"+created.ID, nil, adminHeaders(nil)), http.StatusOK); got.Status != statusRevoked {
		t.Errorf("status after revoke = %q, want revoked", got.Status)
	}
	if recorder := do(handler, "DELETE", "/api/shares/"+created.ID, nil, adminHeaders(nil)); recorder.Code != http.StatusNoContent {
		t.Errorf("second delete = %d, want 204", recorder.Code)
	}
	if _, err := os.Stat(store.AudioPath(created.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("audio still on disk after revoke: %v", err)
	}
}

func TestClipSharePublishesRebasedTimeline(t *testing.T) {
	server, _, transcoder := newTestServer(t)
	handler := server.Handler()
	metadata := testMetadata()
	metadata.Mode = "clip"
	metadata.DurationMs = 12000
	metadata.ClipStartMs = 425
	metadata.ClipEndMs = 2125
	metadata.Lyrics = []LyricLine{{
		Text: "第一句", AtMs: 0, EndMs: 2000,
		Words: []LyricWord{
			{Text: "第", StartMs: 100, EndMs: 700},
			{Text: "二", StartMs: 1000, EndMs: 1800},
		},
	}}

	created := createShare(t, handler, metadata, "idem-clip-0001")
	if recorder := uploadAudio(t, handler, created.ID, "audio/mpeg", http.StatusOK); recorder.Code != http.StatusOK {
		t.Fatalf("audio status = %d (body %s)", recorder.Code, recorder.Body.String())
	}
	if transcoder.clip == nil || transcoder.clip.StartMs != 425 || transcoder.clip.EndMs != 2125 {
		t.Fatalf("transcoder clip = %+v, want 425..2125", transcoder.clip)
	}
	if transcoder.demuxer != "mp3" {
		t.Errorf("demuxer = %q, want mp3", transcoder.demuxer)
	}

	publicShare := decode[PublicShare](t, do(handler, "GET", "/api/public/"+created.ID, nil, nil), http.StatusOK)
	if publicShare.DurationMs != 1700 {
		t.Errorf("durationMs = %d, want 1700", publicShare.DurationMs)
	}
	if len(publicShare.Lyrics) != 1 || len(publicShare.Lyrics[0].Words) != 2 {
		t.Fatalf("lyrics = %+v, want one line with two words", publicShare.Lyrics)
	}
	if start := publicShare.Lyrics[0].Words[1].StartMs; start != 575 {
		t.Errorf("second word start = %d, want 575", start)
	}
	if end := publicShare.Lyrics[0].Words[1].EndMs; end != 1375 {
		t.Errorf("second word end = %d, want 1375", end)
	}
}

func TestAdminRequiresTokenAndOrigin(t *testing.T) {
	server, _, _ := newTestServer(t)
	handler := server.Handler()
	body := jsonBody(t, testMetadata())

	if recorder := do(handler, "GET", "/healthz", nil, nil); recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Errorf("healthz = %d %q, want 200 ok", recorder.Code, recorder.Body.String())
	}

	noAuth := do(handler, "POST", "/api/shares", body, map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "idem-auth-0001",
	})
	if noAuth.Code != http.StatusUnauthorized {
		t.Errorf("missing token = %d, want 401", noAuth.Code)
	}
	wrongToken := do(handler, "POST", "/api/shares", body, map[string]string{
		"Authorization": "Bearer nope-nope-nope", "Origin": testOrigin,
		"Content-Type": "application/json", "Idempotency-Key": "idem-auth-0001",
	})
	if wrongToken.Code != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", wrongToken.Code)
	}
	badOrigin := do(handler, "POST", "/api/shares", body, map[string]string{
		"Authorization": "Bearer " + testToken, "Origin": "https://evil.example",
		"Content-Type": "application/json", "Idempotency-Key": "idem-auth-0001",
	})
	if badOrigin.Code != http.StatusForbidden {
		t.Errorf("foreign origin = %d, want 403", badOrigin.Code)
	}
	crossSite := do(handler, "POST", "/api/shares", body, adminHeaders(map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "idem-auth-0001",
		"Sec-Fetch-Site": "cross-site",
	}))
	if crossSite.Code != http.StatusForbidden {
		t.Errorf("cross-site fetch = %d, want 403", crossSite.Code)
	}
	allowed := do(handler, "POST", "/api/shares", body, adminHeaders(map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "idem-auth-0001",
	}))
	if allowed.Code != http.StatusCreated {
		t.Errorf("authorized create = %d, want 201 (body %s)", allowed.Code, allowed.Body.String())
	}
}

func TestCreateRejectsBadRequests(t *testing.T) {
	server, _, _ := newTestServer(t)
	handler := server.Handler()

	plain := do(handler, "POST", "/api/shares", jsonBody(t, testMetadata()), adminHeaders(map[string]string{
		"Content-Type": "text/plain", "Idempotency-Key": "idem-bad-0001",
	}))
	if plain.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain = %d, want 415", plain.Code)
	}

	missingKey := do(handler, "POST", "/api/shares", jsonBody(t, testMetadata()), adminHeaders(map[string]string{
		"Content-Type": "application/json",
	}))
	if missingKey.Code != http.StatusBadRequest {
		t.Errorf("missing idempotency key = %d, want 400", missingKey.Code)
	}

	badMode := testMetadata()
	badMode.Mode = "stream"
	invalid := do(handler, "POST", "/api/shares", jsonBody(t, badMode), adminHeaders(map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "idem-bad-0002",
	}))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "invalid_metadata") {
		t.Errorf("bad mode = %d %s, want 400 invalid_metadata", invalid.Code, invalid.Body.String())
	}

	oversize := bytes.Repeat([]byte(" "), maxMetadataBytes+1)
	tooLarge := do(handler, "POST", "/api/shares", oversize, adminHeaders(map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "idem-bad-0003",
	}))
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body = %d, want 413", tooLarge.Code)
	}

	if recorder := uploadAudio(t, handler, "unknown-identifier", "audio/mpeg", http.StatusNotFound); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown share audio = %d, want 404", recorder.Code)
	}
	created := createShare(t, handler, testMetadata(), "idem-bad-0004")
	if recorder := uploadAudio(t, handler, created.ID, "video/webm", http.StatusUnsupportedMediaType); recorder.Code != http.StatusUnsupportedMediaType {
		t.Errorf("video/webm audio = %d, want 415", recorder.Code)
	}
	empty := do(handler, "PUT", "/api/shares/"+created.ID+"/audio", nil, adminHeaders(map[string]string{
		"Content-Type": "audio/mpeg",
	}))
	if empty.Code != http.StatusBadRequest {
		t.Errorf("empty audio = %d, want 400", empty.Code)
	}
}

func TestFailedTranscodeNeverPublishesOriginal(t *testing.T) {
	server, store, transcoder := newTestServer(t)
	handler := server.Handler()
	metadata := testMetadata()

	created := createShare(t, handler, metadata, "idem-fail-0001")
	transcoder.err = errors.New("ffmpeg said no")
	failed := uploadAudio(t, handler, created.ID, "audio/mpeg", http.StatusUnprocessableEntity)
	if !strings.Contains(failed.Body.String(), "transcode_failed") {
		t.Errorf("body = %s, want transcode_failed", failed.Body.String())
	}
	if got := decode[shareResult](t, do(handler, "GET", "/api/shares/"+created.ID, nil, adminHeaders(nil)), http.StatusOK); got.Status != statusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if _, err := os.Stat(store.AudioPath(created.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed share kept a media file: %v", err)
	}
	if recorder := do(handler, "GET", "/s/"+created.ID, nil, nil); recorder.Code != http.StatusNotFound {
		t.Errorf("failed share page = %d, want 404", recorder.Code)
	}
}

func TestUnavailableFFmpegAllowsRetry(t *testing.T) {
	server, _, transcoder := newTestServer(t)
	handler := server.Handler()
	created := createShare(t, handler, testMetadata(), "idem-retry-0001")

	transcoder.err = ErrFFmpegUnavailable
	unavailable := uploadAudio(t, handler, created.ID, "audio/mpeg", http.StatusServiceUnavailable)
	if !strings.Contains(unavailable.Body.String(), "ffmpeg_unavailable") {
		t.Errorf("body = %s, want ffmpeg_unavailable", unavailable.Body.String())
	}
	if got := decode[shareResult](t, do(handler, "GET", "/api/shares/"+created.ID, nil, adminHeaders(nil)), http.StatusOK); got.Status != statusAwaitingUpload {
		t.Fatalf("status after unavailable = %q, want awaiting_upload", got.Status)
	}

	transcoder.err = nil
	retried := decode[shareResult](t, uploadAudio(t, handler, created.ID, "audio/mpeg", http.StatusOK), http.StatusOK)
	if retried.Status != statusReady {
		t.Errorf("status after retry = %q, want ready", retried.Status)
	}
}

func TestTranscodeTimeoutFailsTheShare(t *testing.T) {
	server, _, transcoder := newTestServer(t)
	handler := server.Handler()
	created := createShare(t, handler, testMetadata(), "idem-timeout-0001")
	transcoder.err = context.DeadlineExceeded
	if recorder := uploadAudio(t, handler, created.ID, "audio/mpeg", http.StatusGatewayTimeout); recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout = %d, want 504", recorder.Code)
	}
	got := decode[shareResult](t, do(handler, "GET", "/api/shares/"+created.ID, nil, adminHeaders(nil)), http.StatusOK)
	if got.Status != statusFailed || got.Error != "transcode_timeout" {
		t.Errorf("status = %q/%q, want failed/transcode_timeout", got.Status, got.Error)
	}
}

func TestCoverValidation(t *testing.T) {
	server, _, _ := newTestServer(t)
	handler := server.Handler()
	metadata := testMetadata()
	metadata.CoverHash = "aa11"
	created := createShare(t, handler, metadata, "idem-cover-0001")

	mismatch := do(handler, "PUT", "/api/shares/"+created.ID+"/cover", testPNG(t), adminHeaders(map[string]string{
		"Content-Type": "image/png",
	}))
	if mismatch.Code != http.StatusConflict || !strings.Contains(mismatch.Body.String(), "cover_hash_mismatch") {
		t.Errorf("mismatched cover = %d %s, want 409 cover_hash_mismatch", mismatch.Code, mismatch.Body.String())
	}

	notAnImage := do(handler, "PUT", "/api/shares/"+created.ID+"/cover", []byte("not a png"), adminHeaders(map[string]string{
		"Content-Type": "image/png",
	}))
	if notAnImage.Code != http.StatusUnprocessableEntity {
		t.Errorf("garbage cover = %d, want 422", notAnImage.Code)
	}

	wrongType := do(handler, "PUT", "/api/shares/"+created.ID+"/cover", testPNG(t), adminHeaders(map[string]string{
		"Content-Type": "image/jpeg",
	}))
	if wrongType.Code != http.StatusUnsupportedMediaType {
		t.Errorf("jpeg cover = %d, want 415", wrongType.Code)
	}
}

func TestUnpublishedShareIsInvisible(t *testing.T) {
	server, _, _ := newTestServer(t)
	handler := server.Handler()
	created := createShare(t, handler, testMetadata(), "idem-hidden-0001")
	for _, target := range []string{"/s/" + created.ID, "/api/public/" + created.ID, "/s/" + created.ID + "/audio", "/s/" + created.ID + "/cover"} {
		if recorder := do(handler, "GET", target, nil, nil); recorder.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, recorder.Code)
		}
	}
}

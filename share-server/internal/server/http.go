package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	maxMetadataBytes    = 1 << 20
	maxCoverBytes       = 20 << 20
	maxAudioBytes       = 200 << 20
	uploadConcurrency   = 2
	audioRequestTimeout = 10 * time.Minute
	maxCoverDimension   = 8192
	maxCoverPixels      = 36_000_000
	janitorInterval     = 10 * time.Minute
)

// Options lets tests supply a store and transcoder. Both default to the real
// implementations when nil.
type Options struct {
	Store      *Store
	Transcoder Transcoder
	Logger     *slog.Logger
}

// Server serves the admin API, the public listening page and the media routes.
type Server struct {
	cfg        Config
	store      *Store
	transcoder Transcoder
	logger     *slog.Logger
	mux        *http.ServeMux
	uploads    chan struct{}
}

func New(cfg Config, opts Options) (*Server, error) {
	store := opts.Store
	if store == nil {
		var err error
		store, err = OpenStore(cfg.DataDir, cfg.TTL)
		if err != nil {
			return nil, err
		}
	}
	transcoder := opts.Transcoder
	if transcoder == nil {
		path := strings.TrimSpace(cfg.FFmpegPath)
		if path == "" {
			path, _ = exec.LookPath("ffmpeg")
		}
		transcoder = NewFFmpeg(path)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server := &Server{
		cfg:        cfg,
		store:      store,
		transcoder: transcoder,
		logger:     logger,
		uploads:    make(chan struct{}, uploadConcurrency),
	}
	server.routes()
	return server, nil
}

// Handler exposes the routing tree for tests and for embedding.
func (s *Server) Handler() http.Handler { return s.mux }

// Run listens until ctx is cancelled, then shuts down in-flight work.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Listen, err)
	}
	httpServer := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	s.logger.Info("share server listening",
		"listen", listener.Addr().String(),
		"publicOrigin", s.cfg.PublicOrigin,
		"dataDir", s.cfg.DataDir,
		"ttl", s.cfg.TTL.String(),
		"ffmpeg", s.transcoderAvailable(),
	)
	go s.janitor(ctx)
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-finished:
			return
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	err = httpServer.Serve(listener)
	close(finished)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) transcoderAvailable() bool {
	if available, ok := s.transcoder.(interface{ Available() bool }); ok {
		return available.Available()
	}
	return s.transcoder != nil
}

func (s *Server) janitor(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.Prune(s.store.Now()); err != nil {
				s.logger.Error("could not prune shares", "error", err)
			}
		}
	}
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("POST /api/shares", s.admin(http.HandlerFunc(s.handleCreate)))
	mux.Handle("GET /api/shares", s.admin(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/shares/reuse", s.admin(http.HandlerFunc(s.handleReuse)))
	mux.Handle("GET /api/shares/{id}", s.admin(http.HandlerFunc(s.handleGet)))
	mux.Handle("DELETE /api/shares/{id}", s.admin(http.HandlerFunc(s.handleDelete)))
	mux.Handle("PUT /api/shares/{id}/cover", s.admin(http.HandlerFunc(s.handleCover)))
	mux.Handle("PUT /api/shares/{id}/audio", s.admin(http.HandlerFunc(s.handleAudio)))
	mux.HandleFunc("GET /api/public/{id}", s.handlePublicJSON)
	mux.HandleFunc("GET /s/{id}", s.handlePage)
	mux.HandleFunc("GET /s/{id}/audio", s.handlePublicAudio)
	mux.HandleFunc("GET /s/{id}/cover", s.handlePublicCover)
	s.mux = mux
}

func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, "origin_not_allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	token := strings.TrimSpace(header[len(prefix):])
	provided := sha256.Sum256([]byte(token))
	expected := sha256.Sum256([]byte(s.cfg.Token))
	return subtle.ConstantTimeCompare(provided[:], expected[:]) == 1
}

// originAllowed rejects cross-site browser requests to the admin API while
// still accepting the desktop client, which sends its own origin.
func (s *Server) originAllowed(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	normalized, err := normalizeOrigin(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(normalized, s.cfg.PublicOrigin)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	metadata, ok := s.decodeMetadata(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(key) {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	share, created, err := s.store.Create(metadata, key, requestKey(metadata))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.shareResult(share))
}

func (s *Server) handleReuse(w http.ResponseWriter, r *http.Request) {
	metadata, ok := s.decodeMetadata(w, r)
	if !ok {
		return
	}
	share, found := s.store.Reuse(requestKey(metadata))
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, s.shareResult(share))
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	share, found := s.store.Get(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, s.shareResult(share))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	shares := s.store.List()
	items := make([]shareListItem, 0, len(shares))
	for _, share := range shares {
		items = append(items, s.listItem(share))
	}
	writeJSON(w, http.StatusOK, struct {
		Shares []shareListItem `json:"shares"`
	}{Shares: items})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.Revoke(r.PathValue("id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	share, found := s.store.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	switch share.Status {
	case statusRevoked, statusExpired:
		writeError(w, http.StatusGone, "share_gone")
		return
	}
	if !isPNG(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if r.ContentLength > maxCoverBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	path, size, hash, err := s.receiveTemp(w, r, maxCoverBytes, "cover-*.png")
	if err != nil {
		s.writeBodyError(w, err)
		return
	}
	if size == 0 {
		_ = os.Remove(path)
		writeError(w, http.StatusBadRequest, "empty_body")
		return
	}
	if !validCoverImage(path) {
		_ = os.Remove(path)
		writeError(w, http.StatusUnprocessableEntity, "invalid_cover")
		return
	}
	if err := s.store.SetCover(id, path, hash); err != nil {
		_ = os.Remove(path)
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	share, found := s.store.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	switch share.Status {
	case statusRevoked, statusExpired:
		writeError(w, http.StatusGone, "share_gone")
		return
	case statusReady:
		writeJSON(w, http.StatusOK, s.shareResult(share))
		return
	case statusProcessing:
		writeError(w, http.StatusConflict, "upload_in_progress")
		return
	}
	demuxer, ok := audioDemuxer(r.Header.Get("Content-Type"))
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if r.ContentLength > maxAudioBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	input, size, _, err := s.receiveTemp(w, r, maxAudioBytes, "audio-*")
	if err != nil {
		s.writeBodyError(w, err)
		return
	}
	defer os.Remove(input)
	if size == 0 {
		writeError(w, http.StatusBadRequest, "empty_body")
		return
	}

	if _, err := s.store.BeginAudio(id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	// From here on every path must either publish, fail, or reset the upload.
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	case <-r.Context().Done():
		s.store.ResetAudio(id)
		return
	}

	output, err := os.CreateTemp(s.store.TempDir(), "transcode-*.mp3")
	if err != nil {
		s.store.ResetAudio(id)
		s.writeStoreError(w, err)
		return
	}
	outputPath := output.Name()
	_ = output.Close()
	_ = os.Remove(outputPath)
	defer os.Remove(outputPath)

	var clip *Clip
	if share.Metadata.Mode == "clip" {
		clip = &Clip{StartMs: share.Metadata.ClipStartMs, EndMs: share.Metadata.ClipEndMs}
	}
	ctx, cancel := context.WithTimeout(r.Context(), audioRequestTimeout)
	defer cancel()
	transcodeErr := s.transcoder.Transcode(ctx, AudioInput{Path: input, Demuxer: demuxer}, clip, outputPath)
	if transcodeErr != nil {
		switch {
		case errors.Is(transcodeErr, ErrFFmpegUnavailable):
			s.store.ResetAudio(id)
			writeError(w, http.StatusServiceUnavailable, "ffmpeg_unavailable")
		case errors.Is(transcodeErr, context.Canceled):
			s.store.ResetAudio(id)
		case errors.Is(transcodeErr, context.DeadlineExceeded):
			s.store.FailAudio(id, "transcode_timeout")
			writeError(w, http.StatusGatewayTimeout, "transcode_timeout")
		default:
			s.logger.Warn("could not transcode share audio", "share", id, "error", transcodeErr)
			s.store.FailAudio(id, "transcode_failed")
			writeError(w, http.StatusUnprocessableEntity, "transcode_failed")
		}
		return
	}

	durationMs := share.Metadata.DurationMs
	if clip != nil {
		durationMs = clip.EndMs - clip.StartMs
	}
	published, err := s.store.FinishAudio(id, outputPath, durationMs)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.shareResult(published))
}

func (s *Server) handlePublicJSON(w http.ResponseWriter, r *http.Request) {
	share, ok := s.store.Published(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, publicShareFor(share))
}

func (s *Server) handlePublicAudio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	share, ok := s.store.Published(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	file, err := os.Open(s.store.AudioPath(id))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	var modtime time.Time
	if share.ReadyAt != nil {
		modtime = *share.ReadyAt
	}
	http.ServeContent(w, r, "audio.mp3", modtime, file)
}

func (s *Server) handlePublicCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	share, ok := s.store.Published(id)
	if !ok || !share.HasCover {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	file, err := os.Open(s.store.CoverPath(id))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	var modtime time.Time
	if share.ReadyAt != nil {
		modtime = *share.ReadyAt
	}
	http.ServeContent(w, r, "cover.png", modtime, file)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	share, ok := s.store.Published(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	nonce, err := newNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	page := sharePage{
		PublicShare: publicShareFor(share),
		Nonce:       nonce,
		HasCover:    share.HasCover,
	}
	var body strings.Builder
	if err := sharePageTemplate.Execute(&body, page); err != nil {
		s.logger.Error("could not render share page", "error", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf(
		"default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'nonce-%s'; script-src 'nonce-%s'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
		nonce, nonce))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body.String())
	s.store.TouchOpened(id)
}

func (s *Server) decodeMetadata(w http.ResponseWriter, r *http.Request) (Metadata, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return Metadata{}, false
	}
	if r.ContentLength > maxMetadataBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return Metadata{}, false
	}
	body := http.MaxBytesReader(w, r.Body, maxMetadataBytes)
	decoder := json.NewDecoder(body)
	var metadata Metadata
	if err := decoder.Decode(&metadata); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json")
		}
		return Metadata{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json")
		}
		return Metadata{}, false
	}
	if err := metadata.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_metadata")
		return Metadata{}, false
	}
	return metadata, true
}

// receiveTemp copies the request body into the data directory while hashing it.
func (s *Server) receiveTemp(w http.ResponseWriter, r *http.Request, limit int64, pattern string) (string, int64, string, error) {
	body := http.MaxBytesReader(w, r.Body, limit)
	file, err := os.CreateTemp(s.store.TempDir(), pattern)
	if err != nil {
		return "", 0, "", err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(file.Name())
		return "", 0, "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(file.Name())
		return "", 0, "", closeErr
	}
	return file.Name(), written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *Server) writeBodyError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	s.logger.Error("could not receive upload", "error", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrShareGone):
		writeError(w, http.StatusGone, "share_gone")
	case errors.Is(err, ErrUploadInProgress):
		writeError(w, http.StatusConflict, "upload_in_progress")
	case errors.Is(err, ErrAlreadyReady):
		writeError(w, http.StatusConflict, "already_ready")
	case errors.Is(err, ErrCoverMismatch):
		writeError(w, http.StatusConflict, "cover_hash_mismatch")
	case errors.Is(err, ErrNotUploadable):
		writeError(w, http.StatusConflict, "not_uploadable")
	case errors.Is(err, ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict")
	case errors.Is(err, ErrTooManyShares):
		writeError(w, http.StatusTooManyRequests, "too_many_shares")
	default:
		s.logger.Error("share store error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal")
	}
}

func (s *Server) shareResult(share Share) shareResult {
	result := shareResult{ID: share.ID, Status: share.Status, Error: share.Error}
	if share.Status == statusReady {
		result.URL = s.publicURL(share.ID)
		result.Expires = formatTime(share.ExpiresAt)
	}
	return result
}

func (s *Server) listItem(share Share) shareListItem {
	item := shareListItem{
		ID:       share.ID,
		Title:    share.Metadata.Title,
		Artists:  share.Metadata.Artists,
		Mode:     share.Metadata.Mode,
		Template: share.Metadata.Template,
		Status:   share.Status,
		Error:    share.Error,
	}
	if share.Status == statusReady {
		item.URL = s.publicURL(share.ID)
		item.Expires = formatTime(share.ExpiresAt)
	}
	if share.LastOpenedAt != nil {
		item.LastOpened = formatTime(*share.LastOpenedAt)
	}
	return item
}

func (s *Server) publicURL(id string) string {
	return strings.TrimRight(s.cfg.PublicOrigin, "/") + "/s/" + id
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func validIdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 200 {
		return false
	}
	for _, r := range key {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func isPNG(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "image/png")
}

func validCoverImage(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		return false
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxCoverDimension || config.Height > maxCoverDimension {
		return false
	}
	return int64(config.Width)*int64(config.Height) <= maxCoverPixels
}

func audioDemuxer(contentType string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(mediaType) {
	case "audio/mpeg", "audio/mp3":
		return "mp3", true
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "mov", true
	case "audio/aac", "audio/aacp", "audio/x-aac":
		return "aac", true
	case "audio/flac", "audio/x-flac":
		return "flac", true
	case "audio/ogg", "audio/opus", "application/ogg":
		return "ogg", true
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav", true
	default:
		return "", false
	}
}

func newNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// Response shapes mirror the desktop client.
type shareResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
	Expires string `json:"expiresAt,omitempty"`
	Error   string `json:"error,omitempty"`
}

type shareListItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artists    string `json:"artists"`
	Mode       string `json:"mode"`
	Template   string `json:"template"`
	Status     string `json:"status"`
	URL        string `json:"url,omitempty"`
	Expires    string `json:"expiresAt,omitempty"`
	LastOpened string `json:"lastOpenedAt,omitempty"`
	Error      string `json:"error,omitempty"`
}

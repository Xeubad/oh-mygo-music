package betterlyrics

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"mygo-music/internal/music"
)

type Status struct {
	TrackID     uint64 `json:"trackId"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	Playing     bool   `json:"playing"`
	PositionMs  int64  `json:"positionMs"`
	DurationMs  int64  `json:"durationMs"`
	LyricsReady bool   `json:"lyricsReady"`
}

type Lyrics struct {
	TrackID     uint64            `json:"trackId"`
	LRC         string            `json:"lrc,omitempty"`
	Translation string            `json:"translation,omitempty"`
	Lines       []music.LyricLine `json:"lines,omitempty"`
}

type Server struct {
	mu         sync.RWMutex
	status     Status
	updated    time.Time
	lyrics     *Lyrics
	subs       map[chan struct{}]struct{}
	httpServer *http.Server
	listener   net.Listener
}

const DefaultAddress = "127.0.0.1:23332"

func NewServer() *Server {
	return &Server{subs: make(map[chan struct{}]struct{})}
}

func (s *Server) Start(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	// 只接受明确的回环 IP，避免解析自定义主机名后意外开放到局域网。
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("BetterLyrics integration must listen on loopback")
	}
	s.mu.RLock()
	alreadyStarted := s.httpServer != nil
	s.mu.RUnlock()
	if alreadyStarted {
		return nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.httpServer != nil {
		s.mu.Unlock()
		_ = listener.Close()
		return nil
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	s.httpServer, s.listener = server, listener
	s.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (s *Server) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close() error {
	s.mu.Lock()
	server := s.httpServer
	s.httpServer, s.listener = nil, nil
	s.mu.Unlock()
	if server != nil {
		return server.Close()
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.serveStatus)
	mux.HandleFunc("/subscribe-player-status", s.serveStatusStream)
	mux.HandleFunc("/lyrics", s.serveLyrics)
	return mux
}

func (s *Server) Publish(status Status, lyrics *Lyrics) {
	s.mu.Lock()
	status.LyricsReady = lyrics != nil && lyrics.TrackID == status.TrackID
	s.status, s.updated = status, time.Now()
	if lyrics != nil && lyrics.TrackID == status.TrackID {
		copy := cloneLyrics(*lyrics)
		s.lyrics = &copy
	} else {
		s.lyrics = nil
	}
	for sub := range s.subs {
		select {
		case sub <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

func cloneLyrics(lyrics Lyrics) Lyrics {
	lyrics.Lines = slices.Clone(lyrics.Lines)
	for i := range lyrics.Lines {
		lyrics.Lines[i].Words = slices.Clone(lyrics.Lines[i].Words)
	}
	return lyrics
}

func (s *Server) currentStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentStatusLocked(time.Now())
}

func (s *Server) currentStatusLocked(now time.Time) Status {
	status := s.status
	if status.Playing && !s.updated.IsZero() {
		status.PositionMs += now.Sub(s.updated).Milliseconds()
	}
	if status.DurationMs > 0 && status.PositionMs > status.DurationMs {
		status.PositionMs = status.DurationMs
	}
	return status
}

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(s.currentStatus()); err != nil {
		http.Error(w, "could not encode playback status", http.StatusInternalServerError)
	}
}

func (s *Server) serveLyrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	trackID, err := strconv.ParseUint(r.URL.Query().Get("trackId"), 10, 64)
	if err != nil {
		http.Error(w, "a valid trackId is required", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	status, lyrics := s.currentStatusLocked(time.Now()), s.lyrics
	if trackID != status.TrackID || lyrics == nil || lyrics.TrackID != trackID {
		s.mu.RUnlock()
		http.NotFound(w, r)
		return
	}
	copy := cloneLyrics(*lyrics)
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(copy); err != nil {
		http.Error(w, "could not encode lyrics", http.StatusInternalServerError)
	}
}

func (s *Server) serveStatusStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	sub := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}()
	writeStatus := func() bool {
		data, err := json.Marshal(s.currentStatus())
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: status\ndata: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !writeStatus() {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub:
			if !writeStatus() {
				return
			}
		case <-ticker.C:
			if s.currentStatus().Playing && !writeStatus() {
				return
			}
		}
	}
}

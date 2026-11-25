package player

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"mygo-music/internal/music"
)

// 取消分享必须中断正在读取的音频请求，并保留原文件、不残留半成品。
func TestSaveSongContextCancelsAnInFlightDownload(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	var asked int
	source := streamer{st: music.Stream{URL: server.URL, Type: "mp3"}, asked: &asked}
	song, err := New(func(func()) {}).FindSong(source, music.Track{ID: "cancel", Source: "src"}, "high")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- song.SaveToContext(ctx, file) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled download did not stop")
	}
	if contents, _ := os.ReadFile(file); string(contents) != "original" {
		t.Fatal("cancelled download changed original file")
	}
	if entries, _ := os.ReadDir(filepath.Dir(file)); len(entries) != 1 {
		t.Fatalf("partial download remained: %v", entries)
	}
}

// streamer answers the same stream for any track, counting how often it's asked.
type streamer struct {
	nothing
	st    music.Stream
	asked *int
}

func (s streamer) Info() music.Info { return music.Info{ID: "src", Playback: music.PlaybackStream} }
func (s streamer) Stream(id, quality string) (*music.Stream, error) {
	*s.asked++
	st := s.st
	return &st, nil
}

func TestFindSongDoesNotUseAnotherCachedPlaybackVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("selected recording"))
	}))
	defer server.Close()

	var asked int
	source := streamer{
		st:    music.Stream{URL: server.URL, Type: "mp3", PlaybackID: "recording-current"},
		asked: &asked,
	}
	player := New(func(func()) {})
	player.Cache = NewCache(t.TempDir(), 1<<20)
	cached := []byte("another recording")
	part := player.Cache.part(&music.Stream{Type: "mp3"})
	if _, err := part.Write(cached); err != nil {
		t.Fatal(err)
	}
	if err := part.Close(); err != nil {
		t.Fatal(err)
	}
	player.Cache.keep("src", "song", "high", &music.Stream{
		Type: "mp3", PlaybackID: "recording-previous",
	}, part.Name(), int64(len(cached)))

	song, err := player.FindSong(source, music.Track{
		ID: "song", Source: "src", PlaybackID: "recording-current",
	}, "high")
	if err != nil {
		t.Fatal(err)
	}
	defer song.Close()
	if asked != 1 {
		t.Fatalf("requested source %d times, want 1 after rejecting the old cached version", asked)
	}
	if song.st == nil || song.st.PlaybackID != "recording-current" {
		t.Fatalf("returned playback version %+v", song.st)
	}
	song.Close()
}

// A song saved comes from its source with the headers the source says it
// needs, and a song kept in the cache is saved from there, without asking.
func TestSaveSong(t *testing.T) {
	audio := []byte("ID3 not really a song")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Write(audio)
	}))
	defer srv.Close()
	var asked int
	src := streamer{st: music.Stream{URL: srv.URL, Type: "flac", Headers: map[string]string{"Authorization": "Bearer token"}}, asked: &asked}
	p := New(func(func()) {})
	p.Cache = NewCache(t.TempDir(), 1<<30)
	dir := t.TempDir()

	song, err := p.FindSong(src, music.Track{ID: "1", Source: "src"}, "high")
	if err != nil {
		t.Fatal(err)
	}
	if song.Type != "flac" {
		t.Errorf("type %q", song.Type)
	}
	path := filepath.Join(dir, "a.flac")
	if err := song.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, audio) {
		t.Errorf("saved %q", b)
	}

	kept := []byte("kept in the cache")
	st := &music.Stream{Quality: "lossless", Type: "ogg"}
	f := p.Cache.part(st)
	f.Write(kept)
	f.Close()
	p.Cache.keep("src", "2", "lossless", st, f.Name(), int64(len(kept)))
	asked = 0
	song, err = p.FindSong(src, music.Track{ID: "2", Source: "src"}, "high")
	if err != nil {
		t.Fatal(err)
	}
	if asked != 0 || song.Type != "ogg" {
		t.Errorf("asked the source %d times, for a song of type %q", asked, song.Type)
	}
	path = filepath.Join(dir, "b.ogg")
	if err := song.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, kept) {
		t.Errorf("saved %q", b)
	}
}

// A song cut short on its way leaves the file it was to replace as it was, and
// nothing beside it.
func TestSaveSongCutShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(1000))
		w.Write([]byte("half"))
	}))
	defer srv.Close()
	var asked int
	src := streamer{st: music.Stream{URL: srv.URL, Type: "mp3"}, asked: &asked}
	dir := t.TempDir()
	path := filepath.Join(dir, "song.mp3")
	os.WriteFile(path, []byte("before"), 0o644)

	song, err := New(func(func()) {}).FindSong(src, music.Track{ID: "1", Source: "src"}, "high")
	if err != nil {
		t.Fatal(err)
	}
	if err := song.SaveTo(path); err == nil {
		t.Error("saved a song cut short")
	}
	if b, _ := os.ReadFile(path); string(b) != "before" {
		t.Errorf("left %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left %v", entries)
	}
}

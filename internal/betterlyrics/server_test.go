package betterlyrics

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

func TestStatusReturnsCurrentTrack(t *testing.T) {
	server := NewServer()
	server.Publish(Status{
		TrackID:    7,
		Title:      "醒",
		Artist:     "林俊杰",
		Playing:    true,
		PositionMs: 12000,
		DurationMs: 240000,
	}, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code %d", response.Code)
	}
	var got Status
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TrackID != 7 || got.Title != "醒" || got.PositionMs != 12000 || got.DurationMs != 240000 || !got.Playing {
		t.Fatalf("status: %+v", got)
	}
}

func TestStatusStreamReportsTrackChanges(t *testing.T) {
	server := NewServer()
	server.Publish(Status{TrackID: 7, Title: "醒"}, nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/subscribe-player-status", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var initial Status
	if err := json.Unmarshal([]byte(readSSEData(t, reader)), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.TrackID != 7 || initial.Title != "醒" || initial.Playing {
		t.Fatalf("initial status: %+v", initial)
	}
	server.Publish(Status{TrackID: 8, Title: "{{PERSON_9tzg4fjw}}", Playing: true}, nil)
	if got := readSSEData(t, reader); !strings.Contains(got, `"trackId":8`) || !strings.Contains(got, `"title":"{{PERSON_9tzg4fjw}}"`) {
		t.Fatalf("updated status: %s", got)
	}
}

// 歌词比音频晚返回时，同一曲目的订阅者也必须收到就绪通知。
func TestStatusStreamReportsLyricsArrivingAfterPlaybackStarts(t *testing.T) {
	server := NewServer()
	server.Publish(Status{TrackID: 7, Title: "醒"}, nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response, err := httpServer.Client().Get(httpServer.URL + "/subscribe-player-status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var initial Status
	if err := json.Unmarshal([]byte(readSSEData(t, reader)), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.LyricsReady {
		t.Fatal("reported lyrics before they arrived")
	}
	server.Publish(Status{TrackID: 7, Title: "醒"}, &Lyrics{TrackID: 7, LRC: "[00:01.000]歌词已到达"})
	var updated Status
	if err := json.Unmarshal([]byte(readSSEData(t, reader)), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.TrackID != 7 || !updated.LyricsReady {
		t.Fatalf("did not report lyrics arriving for the same track: %+v", updated)
	}
}

func TestLyricsRejectsPreviousTrackRevision(t *testing.T) {
	server := NewServer()
	server.Publish(Status{TrackID: 8}, &Lyrics{TrackID: 7, LRC: "[00:01.00]old lyrics"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/lyrics?trackId=7", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status code %d; body %s", response.Code, response.Body.String())
	}
}

func TestLyricsReturnsWordTimesForCurrentTrack(t *testing.T) {
	server := NewServer()
	server.Publish(Status{TrackID: 8}, &Lyrics{
		TrackID:     8,
		Translation: "[00:01.00]你好",
		Lines: []music.LyricLine{{
			Start: 1000,
			End:   3000,
			Words: []music.LyricWord{{Start: 1000, End: 2000, Text: "hello "}, {Start: 2000, End: 3000, Text: "world"}},
		}},
	})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/lyrics?trackId=8", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code %d", response.Code)
	}
	var got Lyrics
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TrackID != 8 || got.Lines[0].Words[0].Start != 1000 || got.Lines[0].Words[1].End != 3000 {
		t.Fatalf("lyrics: %+v", got)
	}
}

func TestServerListensOnlyOnLoopback(t *testing.T) {
	server := NewServer()
	if err := server.Start("0.0.0.0:0"); err == nil {
		t.Fatal("accepted a non-loopback address")
	}
	if err := server.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := http.Get("http://" + server.Address() + "/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code %d", response.StatusCode)
	}
}

func readSSEData(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
		if err == io.EOF {
			t.Fatal("event stream ended")
		}
	}
}

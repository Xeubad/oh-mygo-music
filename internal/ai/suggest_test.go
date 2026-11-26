package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSuggestReturnsCandidateFromModelJSON checks that a suggestion keeps the
// candidate the model picked, its confidence and its reason.
func TestSuggestReturnsCandidateFromModelJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"playlist_id\":\"p1\",\"confidence\":0.9,\"reason\":\"适合通勤\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{
		Song: Song{Name: "夜曲", Artists: "周杰伦", Album: "十一月的萧邦"},
		Candidates: []Candidate{{
			ID: "p1", Name: "通勤", Description: "路上听", TrackCount: 20,
			Samples: []string{"稻香 - 周杰伦"},
		}},
	})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.PlaylistID != "p1" || got.Confidence != 0.9 || got.Reason != "适合通勤" {
		t.Errorf("suggestion = %+v", got)
	}
}

// TestSuggestDropsPlaylistOutsideCandidates checks that a hallucinated id is
// treated as no suggestion, never as a playlist to write to.
func TestSuggestDropsPlaylistOutsideCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"playlist_id\":\"p9\",\"confidence\":0.99,\"reason\":\"x\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{
		Song:       Song{Name: "夜曲"},
		Candidates: []Candidate{{ID: "p1", Name: "通勤"}},
	})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.PlaylistID != "" || got.Confidence != 0 {
		t.Errorf("suggestion = %+v, want none", got)
	}
}

// TestSuggestParsesFencedJSON checks that a reply wrapped in a Markdown fence
// still gives a suggestion, as many services do that.
func TestSuggestParsesFencedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": "```json\n{\"playlist_id\":\"p1\",\"confidence\":0.8,\"reason\":\"通勤\"}\n```",
			}}},
		})
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{
		Song:       Song{Name: "夜曲"},
		Candidates: []Candidate{{ID: "p1", Name: "通勤"}},
	})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.PlaylistID != "p1" || got.Confidence != 0.8 {
		t.Errorf("suggestion = %+v", got)
	}
}

// TestSuggestSkipsModelWithoutCandidates checks that a listener with no own
// playlists costs no request.
func TestSuggestSkipsModelWithoutCandidates(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{Song: Song{Name: "夜曲"}})
	if err != nil || got.PlaylistID != "" {
		t.Fatalf("suggestion = %+v, err = %v", got, err)
	}
	if asked != 0 {
		t.Errorf("asked the model %d times, want 0", asked)
	}
}

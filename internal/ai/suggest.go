package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Song is what the suggestion is for.
type Song struct {
	Name    string
	Artists string
	Album   string
}

// Candidate is one of the listener's own playlists, as the model sees it.
type Candidate struct {
	ID          string
	Name        string
	Description string
	TrackCount  int
	Samples     []string
}

// SuggestInput is everything the model is asked about one song.
type SuggestInput struct {
	Song       Song
	Candidates []Candidate
}

// Suggestion is the model's pick. An empty PlaylistID means it found no fit.
type Suggestion struct {
	PlaylistID string
	Confidence float64
	Reason     string
}

const suggestSystem = `You choose one of the listener's own playlists for a song.
Answer with a single JSON object and nothing else:
{"playlist_id":"<one of the candidate ids, or empty>","confidence":<0..1>,"reason":"<one short sentence>"}
Only pick an id from the candidate list. If none fits, use an empty playlist_id and confidence 0.
Write the reason in the language of the playlist names.`

// Suggest asks the model which of the candidates fits the song.
func Suggest(ctx context.Context, c *Client, in SuggestInput) (Suggestion, error) {
	if len(in.Candidates) == 0 {
		return Suggestion{}, nil
	}
	text, err := c.Complete(ctx, []Message{
		{Role: "system", Content: suggestSystem},
		{Role: "user", Content: suggestPrompt(in)},
	})
	if err != nil {
		return Suggestion{}, err
	}
	return parseSuggestion(text, in.Candidates)
}

func suggestPrompt(in SuggestInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Song: %s", in.Song.Name)
	if in.Song.Artists != "" {
		fmt.Fprintf(&b, " — %s", in.Song.Artists)
	}
	if in.Song.Album != "" {
		fmt.Fprintf(&b, " (album: %s)", in.Song.Album)
	}
	b.WriteString("\n\nCandidate playlists:\n")
	for _, c := range in.Candidates {
		fmt.Fprintf(&b, "- id: %s\n  name: %s\n", c.ID, c.Name)
		if c.Description != "" {
			fmt.Fprintf(&b, "  description: %s\n", c.Description)
		}
		if c.TrackCount > 0 {
			fmt.Fprintf(&b, "  tracks: %d\n", c.TrackCount)
		}
		if len(c.Samples) > 0 {
			b.WriteString("  samples:\n")
			for _, s := range c.Samples {
				fmt.Fprintf(&b, "    - %s\n", s)
			}
		}
	}
	return b.String()
}

// parseSuggestion reads the model's JSON, ignoring fences or text around it,
// and drops a pick that is not one of the candidates.
func parseSuggestion(text string, candidates []Candidate) (Suggestion, error) {
	raw := extractJSON(text)
	var out struct {
		PlaylistID string  `json:"playlist_id"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Suggestion{}, fmt.Errorf("ai: decode suggestion: %w", err)
	}
	known := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		known[c.ID] = true
	}
	if out.PlaylistID == "" || !known[out.PlaylistID] {
		return Suggestion{}, nil
	}
	return Suggestion{
		PlaylistID: out.PlaylistID,
		Confidence: math.Max(0, math.Min(1, out.Confidence)),
		Reason:     strings.TrimSpace(out.Reason),
	}, nil
}

func extractJSON(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return text
}

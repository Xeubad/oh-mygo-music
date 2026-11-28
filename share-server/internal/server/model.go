package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxTitleRunes      = 300
	maxArtistsRunes    = 500
	maxFieldRunes      = 300
	maxTemplateRunes   = 64
	maxDurationMs      = int64(24 * 60 * 60 * 1000)
	maxTimelineMs      = maxDurationMs * 2
	maxLyricLines      = 5000
	maxLyricTextRunes  = 2000
	maxWordRunes       = 200
	maxWordsPerLine    = 1000
	maxTotalWords      = 20000
	maxHotComments     = 100
	maxCommentRunes    = 1000
	maxTotalTextRunes  = 200000
	maxSelectedIndexes = 1000000
)

var (
	ErrInvalidMetadata     = errors.New("invalid metadata")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different content")
	ErrTooManyShares       = errors.New("too many shares")
	ErrNotFound            = errors.New("share not found")
	ErrShareGone           = errors.New("share is no longer available")
	ErrUploadInProgress    = errors.New("share upload is already in progress")
	ErrAlreadyReady        = errors.New("share is already published")
	ErrCoverMismatch       = errors.New("cover hash does not match")
	ErrNotUploadable       = errors.New("share is not waiting for an upload")
)

// Metadata mirrors the desktop's share snapshot. Clip bounds are only
// meaningful when Mode is "clip".
type Metadata struct {
	Title               string       `json:"title"`
	Artists             string       `json:"artists"`
	SourceID            string       `json:"sourceId"`
	AccountID           string       `json:"accountId,omitempty"`
	TrackID             string       `json:"trackId"`
	PlaybackID          string       `json:"playbackId,omitempty"`
	Quality             string       `json:"quality"`
	Template            string       `json:"template"`
	CardSquare          bool         `json:"cardSquare"`
	DefaultView         string       `json:"defaultView,omitempty"`
	Mode                string       `json:"mode"`
	DurationMs          int64        `json:"durationMs"`
	Lyrics              []LyricLine  `json:"lyrics"`
	SelectedLineIndexes []int        `json:"selectedLineIndexes"`
	HotComments         []HotComment `json:"hotComments,omitempty"`
	ClipStartMs         int64        `json:"clipStartMs"`
	ClipEndMs           int64        `json:"clipEndMs"`
	SourceURL           string       `json:"sourceUrl,omitempty"`
	CoverHash           string       `json:"coverHash,omitempty"`
}

type LyricLine struct {
	Text        string      `json:"text"`
	Translation string      `json:"translation,omitempty"`
	AtMs        int64       `json:"atMs"`
	EndMs       int64       `json:"endMs"`
	Words       []LyricWord `json:"words,omitempty"`
	StageEndMs  int64       `json:"stageEndMs,omitempty"`
}

type LyricWord struct {
	Text    string `json:"text"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

type HotComment struct {
	Text   string `json:"text"`
	Author string `json:"author"`
	Likes  int    `json:"likes"`
}

func (m Metadata) Validate() error {
	if m.Mode != "full" && m.Mode != "clip" {
		return metadataError("mode must be full or clip")
	}
	if strings.TrimSpace(m.Title) == "" {
		return metadataError("title is required")
	}
	if utf8.RuneCountInString(m.Title) > maxTitleRunes {
		return metadataError("title is too long")
	}
	if utf8.RuneCountInString(m.Artists) > maxArtistsRunes {
		return metadataError("artists is too long")
	}
	for name, value := range map[string]string{
		"sourceId": m.SourceID, "accountId": m.AccountID, "trackId": m.TrackID, "playbackId": m.PlaybackID,
	} {
		if utf8.RuneCountInString(value) > maxFieldRunes {
			return metadataError(name + " is too long")
		}
	}
	for name, value := range map[string]string{
		"quality": m.Quality, "template": m.Template, "defaultView": m.DefaultView,
	} {
		if utf8.RuneCountInString(value) > maxTemplateRunes {
			return metadataError(name + " is too long")
		}
	}
	if m.DurationMs < 0 || m.DurationMs > maxDurationMs {
		return metadataError("durationMs is out of range")
	}
	if len(m.Lyrics) > maxLyricLines {
		return metadataError("too many lyric lines")
	}
	if len(m.SelectedLineIndexes) > maxSelectedIndexes {
		return metadataError("too many selected lyric indexes")
	}
	for _, index := range m.SelectedLineIndexes {
		if index < 0 || index > maxSelectedIndexes {
			return metadataError("selected lyric index is out of range")
		}
	}
	if len(m.HotComments) > maxHotComments {
		return metadataError("too many hot comments")
	}
	if len(m.SourceURL) > 2048 {
		return metadataError("sourceUrl is too long")
	}
	if len(m.CoverHash) > 128 {
		return metadataError("coverHash is too long")
	}
	if m.Mode == "clip" {
		if m.DurationMs <= 0 {
			return metadataError("clip shares need the song duration")
		}
		if m.ClipStartMs < 0 || m.ClipEndMs <= m.ClipStartMs {
			return metadataError("clip range is invalid")
		}
		if m.ClipEndMs > m.DurationMs+1 {
			return metadataError("clip range exceeds the song duration")
		}
	}

	totalWords, totalRunes := 0, 0
	for index, line := range m.Lyrics {
		label := fmt.Sprintf("lyrics[%d]", index)
		if line.AtMs < 0 || line.EndMs < 0 || line.EndMs < line.AtMs {
			return metadataError(label + " has invalid timing")
		}
		if line.AtMs > maxTimelineMs || line.EndMs > maxTimelineMs ||
			line.StageEndMs < 0 || line.StageEndMs > maxTimelineMs {
			return metadataError(label + " timing is out of range")
		}
		lineRunes := utf8.RuneCountInString(line.Text)
		translationRunes := utf8.RuneCountInString(line.Translation)
		if lineRunes > maxLyricTextRunes || translationRunes > maxLyricTextRunes {
			return metadataError(label + " text is too long")
		}
		totalRunes += lineRunes + translationRunes
		if len(line.Words) > maxWordsPerLine {
			return metadataError(label + " has too many words")
		}
		for _, word := range line.Words {
			if word.StartMs < 0 || word.EndMs < 0 || word.EndMs < word.StartMs ||
				word.StartMs > maxTimelineMs || word.EndMs > maxTimelineMs {
				return metadataError(label + " word timing is invalid")
			}
			if utf8.RuneCountInString(word.Text) > maxWordRunes {
				return metadataError(label + " word is too long")
			}
			totalWords++
			totalRunes += utf8.RuneCountInString(word.Text)
		}
	}
	for index, comment := range m.HotComments {
		commentRunes := utf8.RuneCountInString(comment.Text)
		authorRunes := utf8.RuneCountInString(comment.Author)
		if commentRunes > maxCommentRunes || authorRunes > maxFieldRunes {
			return metadataError(fmt.Sprintf("hotComments[%d] is too long", index))
		}
		totalRunes += commentRunes + authorRunes
	}
	if totalWords > maxTotalWords || totalRunes > maxTotalTextRunes {
		return metadataError("share snapshot is too large")
	}
	return nil
}

func metadataError(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidMetadata, message)
}

// requestKey is the stable identity used by reuse and idempotent retries. It
// normalises collections and ignores clip fields on full-song snapshots.
func requestKey(m Metadata) string {
	type word struct {
		Text    string `json:"text"`
		StartMs int64  `json:"startMs"`
		EndMs   int64  `json:"endMs"`
	}
	type line struct {
		Text        string `json:"text"`
		Translation string `json:"translation"`
		AtMs        int64  `json:"atMs"`
		EndMs       int64  `json:"endMs"`
		Words       []word `json:"words,omitempty"`
		StageEndMs  int64  `json:"stageEndMs"`
	}
	type comment struct {
		Text   string `json:"text"`
		Author string `json:"author"`
		Likes  int    `json:"likes"`
	}
	type canonical struct {
		Title               string    `json:"title"`
		Artists             string    `json:"artists"`
		SourceID            string    `json:"sourceId"`
		AccountID           string    `json:"accountId"`
		TrackID             string    `json:"trackId"`
		PlaybackID          string    `json:"playbackId"`
		Quality             string    `json:"quality"`
		Template            string    `json:"template"`
		CardSquare          bool      `json:"cardSquare"`
		DefaultView         string    `json:"defaultView"`
		Mode                string    `json:"mode"`
		DurationMs          int64     `json:"durationMs"`
		Lyrics              []line    `json:"lyrics"`
		SelectedLineIndexes []int     `json:"selectedLineIndexes"`
		HotComments         []comment `json:"hotComments,omitempty"`
		ClipStartMs         int64     `json:"clipStartMs"`
		ClipEndMs           int64     `json:"clipEndMs"`
		SourceURL           string    `json:"sourceUrl"`
		CoverHash           string    `json:"coverHash"`
	}

	snapshot := canonical{
		Title: m.Title, Artists: m.Artists, SourceID: m.SourceID, AccountID: m.AccountID,
		TrackID: m.TrackID, PlaybackID: m.PlaybackID, Quality: m.Quality, Template: m.Template,
		CardSquare: m.CardSquare, DefaultView: m.DefaultView, Mode: m.Mode,
		DurationMs: m.DurationMs, ClipStartMs: m.ClipStartMs, ClipEndMs: m.ClipEndMs,
		SourceURL: m.SourceURL, CoverHash: m.CoverHash,
	}
	if snapshot.Mode != "clip" {
		snapshot.ClipStartMs, snapshot.ClipEndMs = 0, 0
	}
	snapshot.Lyrics = make([]line, 0, len(m.Lyrics))
	for _, source := range m.Lyrics {
		projected := line{
			Text: source.Text, Translation: source.Translation,
			AtMs: source.AtMs, EndMs: source.EndMs, StageEndMs: source.StageEndMs,
		}
		for _, sourceWord := range source.Words {
			projected.Words = append(projected.Words, word{
				Text: sourceWord.Text, StartMs: sourceWord.StartMs, EndMs: sourceWord.EndMs,
			})
		}
		snapshot.Lyrics = append(snapshot.Lyrics, projected)
	}
	snapshot.SelectedLineIndexes = append([]int{}, m.SelectedLineIndexes...)
	for _, source := range m.HotComments {
		snapshot.HotComments = append(snapshot.HotComments, comment{
			Text: source.Text, Author: source.Author, Likes: source.Likes,
		})
	}
	data, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PublicShare is what a listener's browser may see. It deliberately contains
// no account, source, playback, quality, or cover-hash fields.
type PublicShare struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Artists     string        `json:"artists"`
	Template    string        `json:"template,omitempty"`
	CardSquare  bool          `json:"cardSquare"`
	DefaultView string        `json:"defaultView,omitempty"`
	Mode        string        `json:"mode"`
	DurationMs  int64         `json:"durationMs"`
	Lyrics      []PublicLyric `json:"lyrics"`
	HotComments []HotComment  `json:"hotComments,omitempty"`
	ExpiresAt   string        `json:"expiresAt"`
}

type PublicLyric struct {
	Text        string      `json:"text"`
	Translation string      `json:"translation,omitempty"`
	AtMs        int64       `json:"atMs"`
	EndMs       int64       `json:"endMs"`
	Words       []LyricWord `json:"words,omitempty"`
	StageEndMs  int64       `json:"stageEndMs,omitempty"`
}

func publicShareFor(share Share) PublicShare {
	m := share.Metadata
	duration := share.DurationMs
	if m.Mode == "clip" {
		if duration <= 0 {
			duration = m.ClipEndMs - m.ClipStartMs
		}
	} else if duration <= 0 {
		duration = m.DurationMs
	}
	if duration < 0 {
		duration = 0
	}
	comments := make([]HotComment, len(m.HotComments))
	copy(comments, m.HotComments)
	if len(comments) == 0 {
		comments = nil
	}
	return PublicShare{
		ID: share.ID, Title: m.Title, Artists: m.Artists, Template: m.Template,
		CardSquare: m.CardSquare, DefaultView: m.DefaultView, Mode: m.Mode,
		DurationMs: duration, Lyrics: projectLyrics(m, duration), HotComments: comments,
		ExpiresAt: share.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func projectLyrics(m Metadata, duration int64) []PublicLyric {
	out := make([]PublicLyric, 0, len(m.Lyrics))
	clip := m.Mode == "clip"
	clipStart := m.ClipStartMs
	for _, line := range m.Lyrics {
		if strings.TrimSpace(line.Text) == "" && strings.TrimSpace(line.Translation) == "" {
			continue
		}
		at, end, stage := line.AtMs, line.EndMs, line.StageEndMs
		if clip {
			at -= clipStart
			end -= clipStart
			if stage > 0 {
				stage -= clipStart
			}
			if end <= 0 || at >= duration {
				continue
			}
			if at < 0 {
				at = 0
			}
			if end > duration {
				end = duration
			}
			if end <= at {
				continue
			}
			if stage < at {
				stage = at
			}
			if stage > duration {
				stage = duration
			}
		}
		projected := PublicLyric{
			Text: line.Text, Translation: line.Translation, AtMs: at, EndMs: end,
		}
		if stage > at {
			projected.StageEndMs = stage
		}
		for _, word := range line.Words {
			start, wordEnd := word.StartMs, word.EndMs
			if clip {
				start -= clipStart
				wordEnd -= clipStart
				if wordEnd <= 0 || start >= duration {
					continue
				}
				if start < 0 {
					start = 0
				}
				if wordEnd > duration {
					wordEnd = duration
				}
				if wordEnd <= start {
					continue
				}
			}
			projected.Words = append(projected.Words, LyricWord{
				Text: word.Text, StartMs: start, EndMs: wordEnd,
			})
		}
		out = append(out, projected)
	}
	return out
}

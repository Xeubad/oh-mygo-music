package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
	"mygo-music/internal/sharecard"
)

func (a *app) publishOnlineShare(draft *shareDraft) {
	if draft.onlinePublishing {
		return
	}
	a.stopSharePreview(draft)
	client, err := a.shareClientForSettings()
	if err != nil {
		draft.onlineError = err
		draft.status = T("Could not connect to share service")
		return
	}
	metadata, err := a.onlineShareMetadata(draft)
	if err != nil {
		draft.onlineError = err
		draft.status = err.Error()
		return
	}
	cover, err := encodeShareCover(draft.cover)
	if err != nil {
		draft.onlineError = err
		draft.status = T("Could not prepare cover")
		return
	}
	lib := a.trackLib(&draft.track)
	if lib == nil || lib.src == nil || !a.canDownload(&draft.track) {
		draft.onlineError = music.ErrUnavailable
		draft.status = T("Could not prepare audio")
		return
	}
	requestSnapshot, err := shareRequestSnapshot(metadata, cover)
	if err != nil {
		draft.onlineError = err
		draft.status = T("Could not prepare online share")
		return
	}
	idempotencyKey := draft.onlineRequestKey
	previousID := ""
	if draft.onlineURL != "" || idempotencyKey == "" || draft.onlineRequestSnapshot != requestSnapshot {
		if draft.onlineURL == "" && draft.onlineID != "" && draft.onlineRequestSnapshot != requestSnapshot {
			previousID = draft.onlineID
		}
		idempotencyKey, err = newShareIdempotencyKey()
		if err != nil {
			draft.onlineError = err
			draft.status = T("Could not prepare online share")
			return
		}
		draft.onlineRequestKey = idempotencyKey
		draft.onlineRequestSnapshot = requestSnapshot
	}

	// 后台只读本次发布快照，不能在用户切换来源或关闭面板后读取可变草稿。
	track, quality, source, player := draft.track, draft.onlineQuality, lib.src, a.pl
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	draft.onlineCancel = cancel
	draft.onlinePublishing, draft.onlineError, draft.status = true, nil, T("Preparing online share…")
	go func() {
		defer cancel()
		tempDir, err := os.MkdirTemp("", "mygo-share-")
		if err != nil {
			a.finishOnlineShare(draft, nil, err)
			return
		}
		defer os.RemoveAll(tempDir)

		if previousID != "" {
			if err := client.revoke(ctx, previousID); err != nil {
				a.finishOnlineShare(draft, nil, fmt.Errorf("could not replace unfinished share: %w", err))
				return
			}
		}
		song, err := player.FindSongContext(ctx, source, track, quality)
		if err != nil {
			a.finishOnlineShare(draft, nil, err)
			return
		}
		if err := ctx.Err(); err != nil {
			song.Close()
			a.finishOnlineShare(draft, nil, err)
			return
		}
		audioPath := filepath.Join(tempDir, songFileName(&track, song.Type))
		err = song.SaveToContext(ctx, audioPath)
		if err != nil {
			a.finishOnlineShare(draft, nil, err)
			return
		}

		result, err := client.create(ctx, idempotencyKey, metadata, cover, "image/png", audioPath)
		if err == nil {
			shareID := result.ID
			a.post(func() {
				if a.sharing == draft && draft.open {
					draft.onlineID = shareID
				}
			})
		}
		if err == nil && result.URL == "" && result.Status != "failed" && result.Status != "revoked" {
			a.post(func() {
				if a.sharing == draft && draft.open {
					draft.status = T("Processing online share…")
				}
			})
			for result.URL == "" && result.Status != "failed" && result.Status != "revoked" {
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-time.After(750 * time.Millisecond):
				}
				if err != nil {
					break
				}
				updated, pollErr := client.get(ctx, result.ID)
				if pollErr != nil {
					err = pollErr
					break
				}
				result = updated
			}
		}
		if err != nil {
			a.finishOnlineShare(draft, result, err)
			return
		}
		if result.URL == "" {
			a.finishOnlineShare(draft, result, errors.New("share service did not publish a link"))
			return
		}
		a.finishOnlineShare(draft, result, nil)
	}()
}

func (a *app) stopSharePreview(draft *shareDraft) {
	draft.clipPreviewSerial++
	if draft.clipPreviewCancel != nil {
		draft.clipPreviewCancel()
		draft.clipPreviewCancel = nil
	}
	if draft.clipPreview != nil {
		draft.clipPreview.Close()
		draft.clipPreview = nil
	}
	draft.clipPreviewLoading = false
}

func (a *app) previewShareClip(draft *shareDraft) {
	a.stopSharePreview(draft)
	start, end, ok := shareClipRange(draft.lines, draft.selected, draft.track.Length(), draft.timingReliable)
	lib := a.trackLib(&draft.track)
	if !ok || lib == nil || lib.src == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	draft.clipPreviewCancel, draft.clipPreviewLoading = cancel, true
	serial, track, quality, source, currentPlayer := draft.clipPreviewSerial, draft.track, draft.onlineQuality, lib.src, a.pl
	go func() {
		defer cancel()
		directory, err := os.MkdirTemp("", "mygo-clip-preview-")
		if err != nil {
			a.post(func() {
				if a.sharing == draft && serial == draft.clipPreviewSerial {
					draft.clipPreviewLoading = false
					draft.status = T("Could not preview clip")
				}
			})
			return
		}
		var preview *player.ClipPreview
		song, err := currentPlayer.FindSongContext(ctx, source, track, quality)
		if err == nil {
			path := filepath.Join(directory, "preview."+song.Type)
			err = song.SaveToContext(ctx, path)
			if err == nil {
				preview, err = player.OpenClipPreview(path, start, end)
			}
		}
		workerErr := ctx.Err()
		a.post(func() {
			// 选句或来源变化后，旧试听结果不能播放，更不能改动主播放队列。
			if a.sharing != draft || !draft.open || serial != draft.clipPreviewSerial || workerErr != nil {
				if a.sharing == draft && serial == draft.clipPreviewSerial {
					draft.clipPreviewLoading = false
				}
				if preview != nil {
					preview.Close()
				}
				os.RemoveAll(directory)
				return
			}
			draft.clipPreviewLoading = false
			if err != nil {
				draft.status = T("Could not preview clip")
				os.RemoveAll(directory)
				return
			}
			draft.clipPreview = preview
			if a.pl.Playing() {
				a.pl.TogglePlay()
			}
			// 文件句柄释放后才删除临时目录，兼容 Windows 文件占用约束。
			draft.clipPreviewCancel = func() { cancel(); preview.Close(); os.RemoveAll(directory) }
			preview.Play()
		})
	}()
}

func (a *app) finishOnlineShare(draft *shareDraft, result *shareResult, err error) {
	a.post(func() {
		if a.sharing != draft || !draft.open {
			return
		}
		draft.onlinePublishing = false
		draft.onlineCancel = nil
		draft.onlineError = err
		if err != nil {
			draft.status = T("Could not publish online share")
			if errors.Is(err, context.Canceled) {
				draft.status = T("Online sharing cancelled")
			}
			if result != nil {
				draft.onlineID = result.ID
			}
			return
		}
		draft.onlineURL = result.URL
		draft.onlineID = result.ID
		draft.onlinePublishedSnapshot = draft.onlineRequestSnapshot
		draft.onlinePublishedRevision = draft.onlineRevision
		draft.onlineRequestKey = ""
		draft.status = T("Online link ready")
		a.requestShareRender()
	})
}

func (a *app) onlineShareMetadata(draft *shareDraft) (shareMetadata, error) {
	lines := a.shareLines(draft)
	template := a.settings.CardTemplate
	if template == "" {
		template = "minimal"
	}
	metadata := shareMetadata{
		Title:               draft.track.Name,
		Artists:             draft.track.ArtistNames(),
		SourceID:            draft.track.Source,
		TrackID:             draft.track.ID,
		PlaybackID:          draft.track.PlaybackID,
		Quality:             draft.onlineQuality,
		Template:            template,
		CardSquare:          a.settings.CardSquare,
		Mode:                draft.onlineMode,
		DurationMs:          draft.track.Length().Milliseconds(),
		SelectedLineIndexes: selectedIndexes(draft.selected),
		SourceURL:           sharecard.PageURL(draft.track.URL),
		Lyrics:              []shareLyricLine{},
	}
	if draft.recordingSource != nil {
		metadata.SourceID = draft.recordingSource.Platform
		metadata.AccountID = draft.recordingSource.AccountID
		metadata.TrackID = draft.recordingSource.TrackID
	}
	// 只附带本曲已加载且用户启用的真实热评，不能把上一首的评论写入分享快照。
	if a.settings.HotComments && a.comments.track == draft.track.Source+"\x00"+draft.track.ID {
		for _, comment := range a.comments.topComments() {
			if utf8.RuneCountInString(comment.Text) <= quietRunes && strings.TrimSpace(comment.Text) != "" && len(comment.Author) <= 128 {
				metadata.HotComments = append(metadata.HotComments, shareHotComment{
					Text: comment.Text, Author: comment.Author, Likes: comment.Likes,
				})
			}
		}
	}
	for index, line := range lines {
		end := line.End
		if metadata.DurationMs > 0 && index < len(draft.lines) {
			end = lyricSourceEnd(draft.lines, index, time.Duration(metadata.DurationMs)*time.Millisecond)
		}
		shared := shareLyricLine{
			Text: line.Text, Translation: line.Translation,
			AtMs: line.At.Milliseconds(), EndMs: end.Milliseconds(),
		}
		if stageEnd := min(shared.EndMs, line.End.Milliseconds()); stageEnd > shared.AtMs {
			// 裁剪用的可靠区间可能延伸到下一句，律动仍需原显示终点识别真实间奏。
			shared.StageEndMs = stageEnd
		}
		for _, word := range line.Words {
			startMs := max(shared.AtMs, word.Start.Milliseconds())
			endMs := min(shared.EndMs, word.End.Milliseconds())
			// 毫秒舍入或来源异常不能产生越界、倒序时间；没有有效词时网页按整行做显示降级。
			if strings.TrimSpace(word.Text) == "" || endMs <= startMs ||
				len(shared.Words) > 0 && startMs < shared.Words[len(shared.Words)-1].StartMs {
				continue
			}
			shared.Words = append(shared.Words, shareLyricWord{Text: word.Text, StartMs: startMs, EndMs: endMs})
		}
		metadata.Lyrics = append(metadata.Lyrics, shared)
	}
	if metadata.DurationMs <= 0 && metadata.Mode == "clip" {
		return shareMetadata{}, errors.New(T("Song duration is unavailable"))
	}
	if metadata.Mode == "clip" {
		start, end, ok := shareClipRange(draft.lines, draft.selected, time.Duration(metadata.DurationMs)*time.Millisecond, draft.timingReliable)
		if !ok {
			return shareMetadata{}, errors.New(T("Selected lyrics have no reliable timing"))
		}
		metadata.ClipStartMs, metadata.ClipEndMs = start.Milliseconds(), end.Milliseconds()
	}
	return metadata, nil
}

func shareRequestSnapshot(metadata shareMetadata, cover []byte) (string, error) {
	if len(cover) > 0 {
		coverHash := sha256.Sum256(cover)
		metadata.CoverHash = fmt.Sprintf("%x", coverHash[:])
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (a *app) onlineLinkMatchesDraft(draft *shareDraft) bool {
	return draft.onlineURL != "" && draft.onlineRevision == draft.onlinePublishedRevision
}

func selectedIndexes(selected map[int]bool) []int {
	indexes := make([]int, 0, len(selected))
	for index, on := range selected {
		if on {
			indexes = append(indexes, index)
		}
	}
	slices.Sort(indexes)
	return indexes
}

func reliableShareTiming(answer *music.Lyrics, lines []lyrics.Line) bool {
	if answer == nil || len(lines) == 0 {
		return false
	}
	if len(answer.Lines) == 0 {
		// Build only returns LRC lines when they carry real source timestamps.
		return strings.TrimSpace(answer.LRC) != ""
	}
	for _, line := range answer.Lines {
		if line.Start > 0 || line.End > line.Start {
			return true
		}
		for _, word := range line.Words {
			if word.Start > 0 || word.End > word.Start {
				return true
			}
		}
	}
	return false
}

func encodeShareCover(cover image.Image) ([]byte, error) {
	if cover == nil {
		return nil, nil
	}
	var data bytes.Buffer
	if err := png.Encode(&data, cover); err != nil {
		return nil, fmt.Errorf("encode cover: %w", err)
	}
	return data.Bytes(), nil
}

func (a *app) copyOnlineShareLink(draft *shareDraft) {
	if draft.onlineURL == "" {
		return
	}
	if err := copyText(draft.onlineURL); err != nil {
		a.toast = T("Could not copy link")
		return
	}
	a.toast = T("Link copied")
}

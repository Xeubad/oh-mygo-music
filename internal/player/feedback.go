package player

import (
	"context"
	"errors"

	"mygo-music/internal/music"
)

// PlaybackFeedback 只提供已知阶段和错误分类，不把插件的原始错误交给界面。
type PlaybackFeedback struct {
	Attempt int
	Source  string
	Stage   string
	Failure string
	Tried   int
}

func (p *Player) setLiveFeedback(gen int, stage string, tried int) {
	p.liveMu.Lock()
	p.liveGen = gen
	p.liveStage = stage
	p.liveTried = tried
	p.liveMu.Unlock()
}

func (p *Player) setLiveStage(gen int, stage string) {
	p.liveMu.Lock()
	if p.liveGen == gen && p.liveStage != "recovering" {
		p.liveStage = stage
	}
	p.liveMu.Unlock()
}

func (p *Player) Feedback() PlaybackFeedback {
	if p.track == nil {
		return PlaybackFeedback{}
	}
	f := p.feedback
	f.Attempt = p.gen
	if f.Source == "" {
		f.Source = p.track.Source
	}
	if p.loading {
		p.liveMu.Lock()
		if p.liveGen == p.gen {
			if p.liveStage != "" {
				f.Stage = p.liveStage
			}
			if p.liveTried > f.Tried {
				f.Tried = p.liveTried
			}
		}
		p.liveMu.Unlock()
	}
	if p.err == "" {
		f.Failure = ""
		if !p.loading {
			f.Stage = ""
			if p.Loading() && (p.remote == nil || !p.remote.started) {
				f.Stage = "buffering"
			}
		}
	} else if f.Failure == "" {
		f.Failure = "failed"
	}
	return f
}

func (p *Player) setFailure(source string, err error) {
	p.liveMu.Lock()
	if p.liveGen == p.gen {
		if p.liveStage != "" {
			p.feedback.Stage = p.liveStage
		}
		if p.liveTried > p.feedback.Tried {
			p.feedback.Tried = p.liveTried
		}
	}
	p.liveMu.Unlock()
	p.err = errorText(err)
	p.feedback.Source = source
	switch {
	case errors.Is(err, music.ErrSignIn):
		p.feedback.Failure = "sign-in"
	case errors.Is(err, music.ErrSetup):
		p.feedback.Failure = "setup"
	case errors.Is(err, errNoSource):
		p.feedback.Failure = "source-unavailable"
	case errors.Is(err, music.ErrUnavailable):
		p.feedback.Failure = "unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		p.feedback.Failure = "timeout"
	default:
		p.feedback.Failure = "failed"
	}
}

// RetryCurrent 只重试按钮所属的一次失败，保留队列与当前位置。
func (p *Player) RetryCurrent(attempt int) {
	if attempt != p.gen || p.loading || p.err == "" || p.at < 0 || p.track == nil || p.sourceOf(p.track) == nil {
		return
	}
	p.loadSkipping(p.at, 0, stay, true, p.Position())
}

// InvalidateSource 隔离退出账号、停用或替换之前发出的回调，同时保留队列。
func (p *Player) InvalidateSource(id string) {
	if p.track == nil || p.track.Source != id {
		// 下个位置可能来自被切换的来源，不能从旧的预取直接交接。
		p.dropAhead()
		p.cancelTransitionPreparation()
		return
	}
	from := p.Position()
	p.stopForFeed()
	p.gen++
	p.pending = from
	p.loading, p.playing, p.err = false, false, ""
	p.feedback = PlaybackFeedback{}
	p.lyrics, p.lyricsSource, p.lyricsLoaded = nil, nil, false
	p.sync()
}

package player

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"mygo-music/internal/music"
)

var roundSequence atomic.Uint64

// 用户撤销关联导致的失效不属于歌曲失败，不能消耗连续失败额度或推进队列。
var errCandidateInvalidated = errors.New("player: candidate invalidated")

type cooperativeFailure struct {
	err   error
	limit int
}

func (f *cooperativeFailure) Error() string { return f.err.Error() }
func (f *cooperativeFailure) Unwrap() error { return f.err }

// 仅保存已经打开核验的低档地址；流立即关闭，后续档位须重新打开并核验。
type lowerQualityResult struct{ info music.Stream }

func (r *lowerQualityResult) Error() string { return "player: audio is below requested quality" }

func (p *Player) openCooperative(ctx context.Context, src music.CooperativeSource, track music.Track, quality string, timing *recoveryTiming, commit func(context.Context, *music.Stream, stream, playbackPolicy, func(func() error) error) error) (info *music.Stream, result stream, failure error) {
	roundID := fmt.Sprintf("playback-%d", roundSequence.Add(1))
	started := time.Now()
	if timing != nil {
		// 读取新候选计划也计入恢复剩余时间，不能在解析前重新计时。
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, timing.started.Add(timing.policy.recovery))
		defer cancel()
	}
	// 读取设置本身也受默认整轮约束，不能在候选开始前无限等待。
	// 计划尚未取得也可能最终失败，必须先安装默认限制，不能绕过连续失败保护。
	limit := 3
	defer func() {
		if failure != nil {
			failure = &cooperativeFailure{err: failure, limit: limit}
		}
	}()
	planCtx, planCancel := context.WithTimeout(ctx, 20*time.Second)
	plan, err := src.PlaybackPlan(planCtx, track.ID, quality, roundID)
	planCancel()
	if err != nil {
		return nil, nil, err
	}
	if plan == nil {
		return nil, nil, music.ErrUnavailable
	}
	// 阈值随本轮快照传播；旧插件的既有跳过行为不受聚合配置影响。
	if plan.ConsecutiveFailureLimit > 0 {
		limit = plan.ConsecutiveFailureLimit
	}
	policy := playbackPolicy{
		candidate:    positiveBudget(plan.CandidateTimeoutMs, 5*time.Second),
		stall:        positiveBudget(plan.StallTimeoutMs, 5*time.Second),
		recovery:     positiveBudget(plan.RecoveryTimeoutMs, 20*time.Second),
		failureLimit: limit,
	}
	deadline := started.Add(positiveBudget(plan.RoundTimeoutMs, 20*time.Second))
	if timing != nil {
		policy = timing.policy
		limit = policy.failureLimit
		deadline = timing.started.Add(policy.recovery)
	}
	scheduled, _ := ctx.Value(scheduledCandidateKey{}).(bool)
	round := ctx
	roundCancel := func() {}
	if !scheduled {
		round, roundCancel = context.WithDeadline(ctx, deadline)
	}
	defer roundCancel()
	candidateBudget := policy.candidate
	last := error(music.ErrUnavailable)
	seen := map[[5]string]bool{}
	// 同曲同档仍可有多个 provider；去重及本轮低档后备都必须隔离来源，不能冒用地址。
	fallbacks := map[[5]string]music.Stream{}
	for _, candidate := range plan.Candidates {
		if scheduled && !time.Now().Before(deadline) {
			return nil, nil, context.DeadlineExceeded
		}
		if err := round.Err(); err != nil {
			return nil, nil, err
		}
		identity := [5]string{candidate.Platform, candidate.AccountID, candidate.TrackID, candidate.SourceID, candidate.Quality}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		if rank := qualityRank(candidate.Quality); rank > 0 && rank < qualityRank(plan.MinimumQuality) {
			continue
		}
		var attempt context.Context
		var cancel context.CancelFunc
		if scheduled {
			var waiting *candidateWait
			attempt, waiting = newCandidateWait(round, minTime(deadline, time.Now().Add(candidateBudget)))
			cancel = func() { deadline = deadline.Add(waiting.finish()) }
		} else {
			attempt, cancel = context.WithTimeout(round, candidateBudget)
		}
		guard := guardCandidate(attempt, src, track.ID, roundID, candidate)
		guarded := guard.ctx
		var info *music.Stream
		var s stream
		var err error
		if err = guarded.Err(); err != nil {
			err = context.Cause(guarded)
			guard.stop()
			cancel()
			last = err
			// 全局停止同样可能来自资格校验，不能将其当作单个来源失效后继续。
			if errors.Is(err, music.ErrPlaybackBlocked) {
				return nil, nil, err
			}
			continue
		}
		if cached, ok := fallbacks[identity]; ok {
			delete(fallbacks, identity)
			info, s, err = openCandidateStream(guarded, track, candidate, plan.MinimumQuality, cached)
		} else {
			info, s, err = openCandidate(guarded, src, track, roundID, candidate, plan.MinimumQuality)
		}
		var lower *lowerQualityResult
		if errors.As(err, &lower) && guarded.Err() == nil {
			fallbacks[[5]string{candidate.Platform, candidate.AccountID, candidate.TrackID, candidate.SourceID, lower.info.Quality}] = lower.info
		}
		if err == nil && commit != nil {
			// 准备仍受原截止；主线程成功交接时同步解除下载取消，而非等后台回执。
			dl, _ := s.source()
			guard.protect(dl.Close)
			err = commit(guarded, info, s, policy, guard.commit)
			if guard.committed() {
				err = nil
			} else if err != nil {
				s.Close()
			}
		}
		if err != nil && guarded.Err() != nil {
			err = context.Cause(guarded)
		}
		guard.stop()
		cancel()
		if err != nil {
			last = err
			if errors.Is(err, music.ErrPlaybackBlocked) {
				return nil, nil, err
			}
			continue
		}
		return info, s, nil
	}
	return nil, nil, last
}

// 取消与成功交接以同一把锁确定先后；已交接的流不再属于候选守卫。
type candidateGuard struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	stopWatch   context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	accepted    bool
	stopClose   func() bool
	src         music.CooperativeSource
	id, roundID string
	candidate   music.PlaybackCandidate
}

func guardCandidate(parent context.Context, src music.CooperativeSource, id, roundID string, candidate music.PlaybackCandidate) *candidateGuard {
	ctx, cancel := context.WithCancelCause(parent)
	watch, stopWatch := context.WithCancel(ctx)
	g := &candidateGuard{ctx: ctx, cancel: cancel, stopWatch: stopWatch, done: make(chan struct{}), src: src, id: id, roundID: roundID, candidate: candidate}
	validity, ok := src.(music.CandidateValiditySource)
	if !ok {
		close(g.done)
		return g
	}
	changes := validity.CandidateChanges()
	if valid, err := validity.CandidateValid(ctx, id, roundID, candidate); err != nil || !valid {
		if err == nil {
			err = errCandidateInvalidated
		}
		g.invalidate(err)
		close(g.done)
		return g
	}
	go func() {
		defer close(g.done)
		for {
			select {
			case <-watch.Done():
				return
			case <-changes:
				// 先重订阅再查询，查询期间的新撤销也不会被漏掉。
				changes = validity.CandidateChanges()
				if valid, err := validity.CandidateValid(watch, id, roundID, candidate); err != nil || !valid {
					if err == nil {
						err = errCandidateInvalidated
					}
					g.invalidate(err)
					return
				}
			}
		}
	}()
	return g
}

func (g *candidateGuard) invalidate(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.accepted {
		g.cancel(err)
	}
}

func (g *candidateGuard) protect(closePending func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopClose = context.AfterFunc(g.ctx, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.accepted {
			closePending()
		}
	})
}

func (g *candidateGuard) commit(start func() error) error {
	finish := func() error {
		g.mu.Lock()
		defer g.mu.Unlock()
		if err := g.ctx.Err(); err != nil {
			return context.Cause(g.ctx)
		}
		if err := start(); err != nil {
			return err
		}
		g.accepted = true
		if g.stopClose != nil {
			g.stopClose()
		}
		g.stopWatch()
		return nil
	}
	if gate, ok := g.src.(music.CandidateCommitSource); ok {
		valid, err := gate.CommitCandidate(g.ctx, g.id, g.roundID, g.candidate, finish)
		if err != nil {
			return err
		}
		if !valid {
			g.invalidate(errCandidateInvalidated)
			return errCandidateInvalidated
		}
		return nil
	}
	return finish()
}

func (g *candidateGuard) committed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.accepted
}

func (g *candidateGuard) stop() {
	g.stopWatch()
	<-g.done
	g.mu.Lock()
	if g.stopClose != nil {
		g.stopClose()
	}
	g.cancel(context.Canceled)
	g.mu.Unlock()
}

func positiveBudget(ms int64, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func openCandidate(ctx context.Context, src music.CooperativeSource, track music.Track, roundID string, candidate music.PlaybackCandidate, minimum string) (*music.Stream, stream, error) {
	resolved, err := src.ResolveCandidate(ctx, track.ID, roundID, candidate)
	if err != nil {
		return nil, nil, err
	}
	if resolved == nil {
		return nil, nil, music.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return openCandidateStream(ctx, track, candidate, minimum, resolved.Stream)
}

func openCandidateStream(ctx context.Context, track music.Track, candidate music.PlaybackCandidate, minimum string, info music.Stream) (*music.Stream, stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var err error
	var dl *download
	if path, local := music.PathOf(info.URL); local {
		dl, err = openFile(path)
	} else {
		dl = startDownload(info.URL, info.Size, info.Headers, nil, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	// 候选超时须真正终止下载并唤醒解码器；成功后仅解除截止，不中断播放下载。
	stop := context.AfterFunc(ctx, dl.Close)
	s, err := openStream(dl, track.Duration)
	if err != nil {
		stop()
		dl.Close()
		return nil, nil, err
	}
	actual, codec, bitrate := mediaQuality(s)
	if actual == "" || qualityRank(actual) < qualityRank(minimum) {
		stop()
		s.Close()
		return nil, nil, errors.New("player: audio quality could not be verified")
	}
	if !stop() || ctx.Err() != nil {
		s.Close()
		return nil, nil, ctx.Err()
	}
	// 不采用解析响应中的来源和音质标签，实际来源取本轮冻结候选。
	info.Type, info.Quality, info.BitRate, info.Platform = codec, actual, bitrate, candidate.Platform
	info.PlaybackID = candidate.PlaybackID
	// 高级名称没有参数映射时也只能留作普通后备，不能抢在普通 FLAC 路径前开播。
	if qualityRank(candidate.Quality) == 0 || qualityRank(actual) < qualityRank(candidate.Quality) {
		s.Close()
		return nil, nil, &lowerQualityResult{info: info}
	}
	return &info, s, nil
}

func mediaQuality(s stream) (quality, codec string, bitrate int) {
	dl, _ := s.source()
	off := id3Size(dl)
	if head := dl.peek(off, 4); len(head) == 4 && string(head) == "fLaC" {
		if s.SampleRate() > 0 && s.Channels() > 0 {
			// 无损静音的压缩码率很低，不能用有损码率门槛误拒。
			return "flac", "flac", 0
		}
	}
	if mp3, ok := s.(*mp3Stream); ok {
		bitrate = frameBitRate(dl.peek(mp3.start, 4))
		return lossyQuality(bitrate), "mp3", bitrate
	}
	if adts, ok := s.(*adtsStream); ok {
		return adtsQuality(adts)
	}
	if _, ok := s.(*decodedStream); ok {
		if head := dl.peek(0, 8); len(head) == 8 && string(head[4:]) == "ftyp" {
			return mp4Quality(dl)
		}
	}
	return "", "", 0
}

// 常规档位是准入下限，不给未知高级名称伪造跨平台等价证据。
func qualityRank(q string) int {
	switch q {
	case "flac", "lossless":
		return 7
	case "320k":
		return 6
	case "256k":
		return 5
	case "128k", "standard", "":
		return 4
	case "64k":
		return 3
	case "32k":
		return 2
	}
	return 0
}

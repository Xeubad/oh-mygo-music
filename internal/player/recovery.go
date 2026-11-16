package player

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/egoist/mygo/audio"

	"mygo-music/internal/music"
)

// 一次歌曲播放与一次加载不同；恢复须沿用本次机会和进入歌曲时的播放意图。
type playbackAttempt struct {
	dir, skipped int
	recoveryUsed bool
	completed    bool // 只有正常结束才允许随后从头播放领取新机会。
	policy       playbackPolicy
	recovery     *recoveryTiming
}

// 时间参数属于本次检测和恢复快照，后续重新取计划不能延长正在等待的流程。
type playbackPolicy struct {
	candidate, stall, recovery time.Duration
	failureLimit               int
}

type recoveryTiming struct {
	started time.Time
	policy  playbackPolicy
}

func currentPlaybackPolicy(ctx context.Context, source music.Source, previous playbackPolicy) playbackPolicy {
	src, ok := source.(music.PlaybackTimingSource)
	if !ok {
		return previous
	}
	// 可选配置读取也必须可取消；失败时保留最近一次有效设置，不能无限等待读设置。
	read, cancel := context.WithTimeout(ctx, previous.candidate)
	defer cancel()
	timing, err := src.PlaybackTiming(read)
	if err != nil || timing == nil {
		return previous
	}
	next := previous
	next.candidate = positiveBudget(timing.CandidateTimeoutMs, previous.candidate)
	next.stall = positiveBudget(timing.StallTimeoutMs, previous.stall)
	next.recovery = positiveBudget(timing.RecoveryTimeoutMs, previous.recovery)
	if next.stall >= next.recovery {
		return previous
	}
	return next
}

func (p *Player) monitorDownload(gen int, out *audio.Player, s stream) {
	if p.monitorCancel != nil {
		p.monitorCancel()
	}
	dl, _ := s.source()
	watch, previous := p.watch, p.playback.policy
	source := p.sourceOf(p.track)
	ctx, cancel := context.WithCancel(context.Background())
	p.monitorCancel = cancel
	go func() {
		interval := min(50*time.Millisecond, previous.stall/4)
		if interval <= 0 {
			interval = time.Millisecond
		}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		finished := dl.finished
		var observed int64
		var timing *recoveryTiming
		var position time.Duration
		begin := func(started time.Time) *recoveryTiming {
			return &recoveryTiming{started: started, policy: currentPlaybackPolicy(ctx, source, previous)}
		}
		interrupt := func(err error) {
			// Read 因错误返回不是有效播放进展，不能借 reading 清零重取设置或重置预算。
			if timing == nil || out.Position() != position {
				timing = begin(time.Now())
			}
			frozen := timing
			p.post(func() {
				if ctx.Err() == nil && gen == p.gen && p.out == out && p.playing {
					p.interruptedAt(err, frozen)
				}
			})
		}
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-watch.failed:
				interrupt(err)
				return
			case <-finished:
				finished = nil
				dl.mu.Lock()
				err := dl.err
				dl.mu.Unlock()
				if err == nil || errors.Is(err, context.Canceled) {
					continue
				}
				interrupt(err)
				return
			case <-tick.C:
				reading := watch.reading.Load()
				if reading == 0 {
					// 错误入队后 Read 才清零；tick 先被选中时也不能抹掉原检测快照。
					if err := watch.failure(); err != nil {
						interrupt(err)
						return
					}
					observed, timing = 0, nil
					continue
				}
				if watch.positioning.Load() {
					observed, timing = 0, nil
					continue
				}
				now, current := time.Now(), out.Position()
				if observed != reading || current != position {
					// 缓冲音频仍推进时不是同一次停滞；下一次等待重新固定当前设置。
					observed, position = reading, current
					timing = begin(now)
				}
				if time.Since(timing.started) < timing.policy.stall {
					continue
				}
				resume := make(chan struct{})
				frozen := timing
				p.post(func() {
					defer close(resume)
					if ctx.Err() == nil && gen == p.gen && p.out == out && p.playing && !p.seeking && !watch.positioning.Load() && watch.reading.Load() == reading && out.Position() == position {
						p.interruptedAt(io.ErrNoProgress, frozen)
					}
				})
				select {
				case <-ctx.Done():
					return
				case <-resume:
				}
				observed, timing = 0, nil
			}
		}
	}()
}

func (p *Player) interruptedAt(err error, timing *recoveryTiming) {
	if p.playback == nil || p.track == nil {
		return
	}
	playback, from := p.playback, p.Position()
	if playback.recoveryUsed {
		// 失败不是正常播完，不得通过循环或重新加载返还恢复机会。
		source := p.track.Source
		p.stop()
		p.pending = from
		p.loadFailed(source, &cooperativeFailure{err: err, limit: playback.policy.failureLimit}, p.at, playback.skipped, playback.dir)
		return
	}
	playback.recoveryUsed = true
	playback.recovery = timing
	p.loadPlayback(p.at, playback.skipped, playback.dir, true, from, playback)
}

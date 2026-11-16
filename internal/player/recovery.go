package player

import (
	"context"
	"errors"

	"io"
	"time"

	"github.com/egoist/mygo/audio"
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

func (p *Player) monitorDownload(gen int, out *audio.Player, s stream) {
	if p.monitorCancel != nil {
		p.monitorCancel()
	}
	dl, _ := s.source()
	watch, policy := p.watch, p.playback.policy
	ctx, cancel := context.WithCancel(context.Background())
	p.monitorCancel = cancel
	go func() {
		interval := min(50*time.Millisecond, policy.stall/4)
		if interval <= 0 {
			interval = time.Millisecond
		}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		finished := dl.finished
		var observed int64
		var started time.Time
		var position time.Duration
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-watch.failed:
				detected := time.Now()
				p.post(func() {
					if ctx.Err() == nil && gen == p.gen && p.out == out && p.playing {
						p.interruptedAt(err, detected)
					}
				})
				return
			case <-finished:
				finished = nil
				dl.mu.Lock()
				err := dl.err
				dl.mu.Unlock()
				if err == nil || errors.Is(err, context.Canceled) {
					continue
				}
				detected := time.Now()
				p.post(func() {
					if ctx.Err() == nil && gen == p.gen && p.out == out && p.playing {
						p.interruptedAt(err, detected)
					}
				})
				return
			case <-tick.C:
				reading := watch.reading.Load()
				if reading == 0 || watch.positioning.Load() {
					observed = 0
					continue
				}
				now, current := time.Now(), out.Position()
				if observed != reading {
					observed, started, position = reading, now, current
				} else if current != position {
					// 预解码等待网络时，缓冲音频仍可推进，不能提前消耗恢复预算。
					started, position = now, current
				}
				if now.Sub(started) < policy.stall {
					continue
				}
				resume := make(chan struct{})
				p.post(func() {
					defer close(resume)
					if ctx.Err() == nil && gen == p.gen && p.out == out && p.playing && !p.seeking && !watch.positioning.Load() && watch.reading.Load() == reading && out.Position() == position {
						p.interruptedAt(io.ErrNoProgress, started)
					}
				})
				select {
				case <-ctx.Done():
					return
				case <-resume:
				}
				observed = 0
			}
		}
	}()
}

func (p *Player) interruptedAt(err error, started time.Time) {
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
	playback.recovery = &recoveryTiming{started: started, policy: playback.policy}
	p.loadPlayback(p.at, playback.skipped, playback.dir, true, from, playback)
}

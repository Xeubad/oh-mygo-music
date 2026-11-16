package player

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"mygo-music/internal/music"
)

var roundSequence atomic.Uint64

type cooperativeFailure struct {
	err   error
	limit int
}

func (f *cooperativeFailure) Error() string { return f.err.Error() }
func (f *cooperativeFailure) Unwrap() error { return f.err }

// 仅保存已经打开核验的低档地址；流立即关闭，后续档位须重新打开并核验。
type lowerQualityResult struct{ info music.Stream }

func (r *lowerQualityResult) Error() string { return "player: audio is below requested quality" }

func (p *Player) openCooperative(ctx context.Context, src music.CooperativeSource, track music.Track, quality string, commit func(context.Context, *music.Stream, stream) error) (info *music.Stream, result stream, failure error) {
	roundID := fmt.Sprintf("playback-%d", roundSequence.Add(1))
	started := time.Now()
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
	roundBudget := positiveBudget(plan.RoundTimeoutMs, 20*time.Second)
	round, roundCancel := context.WithDeadline(ctx, started.Add(roundBudget))
	defer roundCancel()
	candidateBudget := positiveBudget(plan.CandidateTimeoutMs, 5*time.Second)
	last := error(music.ErrUnavailable)
	seen := map[[4]string]bool{}
	// 后备只能活在当前轮次；用完整身份及实际档位索引，不改变计划的候选顺序。
	fallbacks := map[[4]string]music.Stream{}
	for _, candidate := range plan.Candidates {
		if err := round.Err(); err != nil {
			return nil, nil, err
		}
		identity := [4]string{candidate.Platform, candidate.AccountID, candidate.TrackID, candidate.Quality}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		if rank := qualityRank(candidate.Quality); rank > 0 && rank < qualityRank(plan.MinimumQuality) {
			continue
		}
		attempt, cancel := context.WithTimeout(round, candidateBudget)
		var info *music.Stream
		var s stream
		var err error
		if cached, ok := fallbacks[identity]; ok {
			delete(fallbacks, identity)
			info, s, err = openCandidateStream(attempt, track, candidate, plan.MinimumQuality, cached)
		} else {
			info, s, err = openCandidate(attempt, src, track, roundID, candidate, plan.MinimumQuality)
		}
		var lower *lowerQualityResult
		if errors.As(err, &lower) && attempt.Err() == nil {
			fallbacks[[4]string{candidate.Platform, candidate.AccountID, candidate.TrackID, lower.info.Quality}] = lower.info
		}
		if err == nil && commit != nil {
			// 地址和解码成功尚未完成本轮；主线程排队及启动仍用原候选截止。
			dl, _ := s.source()
			stop := context.AfterFunc(attempt, dl.Close)
			err = commit(attempt, info, s)
			stop()
			if err != nil {
				s.Close()
			}
		}
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
		for _, level := range []int{320, 256, 128, 64, 32} {
			if bitrate >= level {
				return fmt.Sprintf("%dk", level), "mp3", bitrate
			}
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

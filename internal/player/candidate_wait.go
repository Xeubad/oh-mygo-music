package player

import (
	"context"
	"sync"
	"time"
)

type scheduledCandidateKey struct{}
type candidateWaitKey struct{}

func minTime(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

// 就绪等待不消耗解析预算；取消与资格撤销仍始终有效，失败后只返还实际等待的时间。
type candidateWait struct {
	mu     sync.Mutex
	timer  *time.Timer
	ctx    context.Context
	cancel context.CancelCauseFunc
	paused time.Time
}

func newCandidateWait(parent context.Context, deadline time.Time) (context.Context, *candidateWait) {
	base, cancel := context.WithCancelCause(parent)
	wait := &candidateWait{ctx: base, cancel: cancel}
	wait.timer = time.AfterFunc(time.Until(deadline), func() {
		wait.mu.Lock()
		defer wait.mu.Unlock()
		if wait.paused.IsZero() {
			cancel(context.DeadlineExceeded)
		}
	})
	return context.WithValue(base, candidateWaitKey{}, wait), wait
}

func pausePreparedCandidate(ctx context.Context) error {
	wait, _ := ctx.Value(candidateWaitKey{}).(*candidateWait)
	if wait == nil {
		return ctx.Err()
	}
	wait.mu.Lock()
	defer wait.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if wait.paused.IsZero() {
		wait.paused = time.Now()
		wait.timer.Stop()
	}
	return nil
}

func (wait *candidateWait) finish() time.Duration {
	wait.mu.Lock()
	defer wait.mu.Unlock()
	wait.timer.Stop()
	wait.cancel(context.Canceled)
	if wait.paused.IsZero() {
		return 0
	}
	return time.Since(wait.paused)
}

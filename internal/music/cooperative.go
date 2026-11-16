package music

import (
	"context"
	"errors"
)

// ErrPlaybackBlocked 仅由服务明确给出的全局停止原因触发，不能由裸 HTTP 状态推断。
var ErrPlaybackBlocked = errors.New("automatic playback is blocked")

// PlaybackCandidate 只引用已确认同录音的既有来源，不授予额外搜索权限。
type PlaybackCandidate struct {
	ID        string `json:"id"`
	Platform  string `json:"platform"`
	AccountID string `json:"accountID,omitempty"`
	TrackID   string `json:"trackID"`
	Quality   string `json:"quality"`
}

// PlaybackPlan 是本轮固定的设置和候选顺序；进行中的播放不随设置改写。
type PlaybackPlan struct {
	TargetQuality           string              `json:"targetQuality"`
	MinimumQuality          string              `json:"minimumQuality"`
	CandidateTimeoutMs      int64               `json:"candidateTimeoutMs,omitempty"`
	RoundTimeoutMs          int64               `json:"roundTimeoutMs,omitempty"`
	StallTimeoutMs          int64               `json:"stallTimeoutMs,omitempty"`
	RecoveryTimeoutMs       int64               `json:"recoveryTimeoutMs,omitempty"`
	ConsecutiveFailureLimit int                 `json:"consecutiveFailureLimit,omitempty"`
	Candidates              []PlaybackCandidate `json:"candidates"`
}

// CandidateStream 中服务声明只用于诊断，不能代替实际媒体证据。
type CandidateStream struct {
	Stream           Stream `json:"stream"`
	Platform         string `json:"platform"`
	RequestedQuality string `json:"requestedQuality"`
}

// CooperativeSource 是可选能力；调用方还须检查两个 JS 导出，旧插件继续使用 Stream。
type CooperativeSource interface {
	PlaybackPlan(ctx context.Context, id, quality, roundID string) (*PlaybackPlan, error)
	ResolveCandidate(ctx context.Context, id, roundID string, candidate PlaybackCandidate) (*CandidateStream, error)
}

// CandidateValiditySource 只收紧已冻结的候选，不重建计划或重置本轮截止时间。
// 变更通道关闭后须重新订阅；不具备该能力的旧来源保持原播放行为。
type CandidateValiditySource interface {
	CandidateChanges() <-chan struct{}
	CandidateValid(ctx context.Context, id, roundID string, candidate PlaybackCandidate) (bool, error)
}

// CandidateCommitSource 将最终资格校验与开播串行化，消除查询通过后再撤销的窗口。
// start 只能进行最后的开播交接，不得再次调用来源控制接口；耗时准备须在门禁外完成。
type CandidateCommitSource interface {
	CommitCandidate(ctx context.Context, id, roundID string, candidate PlaybackCandidate, start func() error) (valid bool, err error)
}

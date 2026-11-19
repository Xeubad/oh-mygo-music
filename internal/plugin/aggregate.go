package plugin

import (
	"context"
	"encoding/json"

	"mygo-music/internal/music"
)

// Aggregate 是聚合来源的可选控制接口；普通来源不需要实现这些本机设置操作。
// 返回公开状态，避免界面读取插件普通存储或凭据来拼装自己的业务状态。
func (p *Plugin) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	if action == "setResolverKey" {
		// 临时密钥不经过凭据库，必须在脚本有机会记录输入之前保护诊断。
		if value, ok := input["value"].(string); ok {
			p.diagnostics.remember(value)
		}
	}
	if action == "addSource" || action == "replaceSource" {
		// 链接可能带密，连校验失败的输入也须在脚本读取、记录之前保护。
		if value, ok := input["url"].(string); ok {
			p.diagnostics.remember(value)
		}
	}
	// 新增、更新、重排不能扩展旧计划；替换、删除及启停则与最终开播串行收紧资格。
	tightens := action == "revokeLink" || action == "logout" || action == "replaceSource" || action == "removeSource" || action == "setSourceEnabled"
	if tightens {
		unlock, err := p.lockCandidateGate(context.Background())
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	var out json.RawMessage
	err := p.call("aggregate", map[string]any{"action": action, "input": input}, &out)
	if err == nil && tightens {
		// 仅收紧操作广播变化；播放端自行判断当前候选，不能因别的来源变化停掉音频。
		p.mu.Lock()
		if p.candidateChanges != nil {
			close(p.candidateChanges)
		}
		p.candidateChanges = make(chan struct{})
		p.mu.Unlock()
	}
	return out, err
}

func (p *Plugin) CandidateChanges() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.candidateChanges == nil {
		p.candidateChanges = make(chan struct{})
	}
	return p.candidateChanges
}

func (p *Plugin) CandidateValid(ctx context.Context, id, roundID string, candidate music.PlaybackCandidate) (bool, error) {
	if !p.Can("candidateValid") {
		return true, nil
	}
	var valid bool
	err := p.callContext(ctx, "candidateValid", map[string]any{"id": id, "roundID": roundID, "candidate": candidate}, &valid)
	return valid, err
}

// CommitCandidate 与收紧资格的动作互斥；不能使用宿主存储锁调用 JS。
func (p *Plugin) CommitCandidate(ctx context.Context, id, roundID string, candidate music.PlaybackCandidate, start func() error) (bool, error) {
	unlock, err := p.lockCandidateGate(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	valid, err := p.CandidateValid(ctx, id, roundID, candidate)
	if err != nil || !valid {
		return valid, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, start()
}

func (p *Plugin) lockCandidateGate(ctx context.Context) (func(), error) {
	p.mu.Lock()
	if p.candidateGate == nil {
		p.candidateGate = make(chan struct{}, 1)
	}
	gate := p.candidateGate
	p.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

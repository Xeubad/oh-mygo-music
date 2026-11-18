package player

import (
	"context"
	"time"

	"mygo-music/internal/automix"
)

func (p *Player) AutoMixStatus() string { return p.autoMixStatus }

func (p *Player) CancelAutoMixEnhancement() {
	p.autoMixCloudEpoch++
	if p.autoMixCloudCancel != nil {
		p.autoMixCloudCancel()
		p.autoMixCloudCancel = nil
	}
}

func (p *Player) prepareAutoMix() {
	preparation, output := p.transitionNext, p.transition
	if !p.transitionSettings().AutoMix || p.repeat == RepeatOne || preparation == nil || !preparation.ready || preparation.autoMixDone || preparation.autoMixPlanning || output == nil || !p.playing {
		return
	}
	if p.AutoMixEngine != nil {
		p.prepareAdaptiveAutoMix()
		return
	}
	current, next, currentID, position := output.analysisPair(preparation.id)
	if current == nil || next == nil || current.analyzer == nil || next.analyzer == nil {
		return
	}
	if preparation.autoMixChecked > 0 && position-preparation.autoMixChecked < 24000 {
		return
	}
	preparation.autoMixChecked = position
	preparation.autoMixPlanning = true
	p.autoMixStatus = "analyzing"
	generation := p.gen
	var advisor automix.Planner
	if p.AutoMixAdvisor != nil {
		advisor = p.AutoMixAdvisor()
	}
	parent := preparation.context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	p.autoMixCloudCancel = cancel
	p.autoMixCloudEpoch++
	cloudEpoch := p.autoMixCloudEpoch
	post := p.post
	go func() {
		defer cancel()
		currentFeatures, end, _ := current.analysis()
		nextFeatures, _, prefix := next.analysis()
		// 只在真实可用的尾段进入预读后决策；未知时长必须先看到正常 EOF。
		completeTail := end > 0 && currentFeatures.End >= end-automix.FeatureHop
		if !completeTail || len(prefix) < 4*automix.SampleRate*2 {
			post(func() {
				if p.gen == generation && p.transitionNext == preparation {
					preparation.autoMixPlanning = false
					p.autoMixStatus = "waiting"
				}
			})
			return
		}
		nextFeatures = nextFeatures.Until(int64(len(prefix) / 2))
		earliest := current.sourceFrame(position) + 2*automix.SampleRate
		request := automix.BuildRequest(currentFeatures, nextFeatures, end, earliest)
		decision, valid := request.LocalDecision()
		if !valid {
			post(func() {
				if p.gen == generation && p.transitionNext == preparation {
					preparation.autoMixPlanning, preparation.autoMixDone = false, true
					p.autoMixStatus = "fallback"
				}
			})
			return
		}
		install := func(decision automix.Decision, mode string) {
			candidate, err := request.Resolve(decision)
			if err != nil || candidate.Entry*2 >= int64(len(prefix)) {
				return
			}
			// 实测对齐最多前移320ms，额外半秒保留锁拍速度，避免混音尚未结束就恢复原速。
			intro, mapping, err := automix.StretchIntro(prefix[candidate.Entry*2:], candidate.Ratio, candidate.End-candidate.Start+automix.SampleRate/2)
			if err != nil {
				return
			}
			candidate = automix.RefineRenderedAlignment(currentFeatures, intro, candidate, earliest)
			for index := range mapping.Anchors {
				mapping.Anchors[index].Source += candidate.Entry
			}
			post(func() {
				if p.gen != generation || p.transition != output || p.transitionNext != preparation || !p.transitionSettings().AutoMix {
					return
				}
				// 取消时连已投递到主线程的云结果也要失效；取消不影响本机计划。
				if mode == "ai" && (p.autoMixCloudEpoch != cloudEpoch || p.AutoMixAdvisor == nil || p.AutoMixAdvisor() == nil) {
					return
				}
				preparation.autoMixPlanning, preparation.autoMixDone = false, true
				if output.installAutoMix(currentID, preparation.id, candidate, intro, mapping, len(prefix)) {
					p.autoMixStatus = mode
				}
			})
		}
		// 本机先交付可执行方案；AI 慢、离线或拒绝建议时，不得取消已经可用的本机结果。
		install(decision, "local")
		if advisor == nil {
			return
		}
		if enhanced, err := advisor.Plan(ctx, request); err == nil && ctx.Err() == nil {
			install(enhanced, "ai")
		}
	}()
}

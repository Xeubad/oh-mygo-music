package player

import (
	"slices"

	"mygo-music/internal/music"
)

// PlayNext inserts a new queue entry immediately after the current track.
// Without a current track it starts playback instead.
func (p *Player) PlayNext(track music.Track) {
	p.commitTransitions()
	if track.Unavailable {
		return
	}
	p.cancelTransitionPreparation()
	// 新入队是独立位置，不能继承另一个位置已经成功开播的版本。
	track.PlaybackID = ""
	if p.track == nil || p.at < 0 || p.at >= len(p.order) {
		p.PlayTrack([]music.Track{track}, 0)
		return
	}
	insert := p.order[p.at] + 1
	p.queue = slices.Insert(p.queue, insert, track)
	for index, queued := range p.order {
		if queued >= insert {
			p.order[index]++
		}
	}
	// 同时调整原队列与实际播放顺序，随机模式下也只改变紧邻的下一项。
	p.order = slices.Insert(p.order, p.at+1, insert)
	if p.playback != nil && len(p.playback.failed) > 0 {
		failed := make(map[int]bool, len(p.playback.failed))
		for queued, value := range p.playback.failed {
			if queued >= insert {
				queued++
			}
			failed[queued] = value
		}
		p.playback.failed = failed
	}
	if p.remote != nil {
		// 来源可能已接收旧队列；等其自然切歌时接管，而不是重启当前歌曲。
		p.remote.queueChanged = true
	}
	p.dropAhead()
	if p.awaiting {
		p.load(p.at + 1)
		return
	}
	if p.out != nil {
		quality := music.Qualities[len(music.Qualities)/2]
		if p.Quality != nil {
			quality = p.Quality()
		}
		p.getAhead(quality)
	}
	p.sync()
	p.prepareTransition()
}

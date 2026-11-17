package app

import (
	"log"
	"time"

	"github.com/egoist/mygo"

	"mygo-music/internal/betterlyrics"
)

func (a *app) setBetterLyricsIntegration() {
	if !a.settings.BetterLyricsIntegration {
		a.stopBetterLyricsIntegration()
		return
	}
	if a.betterLyricsServer != nil {
		return
	}
	server := betterlyrics.NewServer()
	if err := server.Start(betterlyrics.DefaultAddress); err != nil {
		a.betterLyricsStatus = Tf("Could not start BetterLyrics integration: %s", err)
		log.Printf("starting BetterLyrics integration: %v", err)
		a.redraw()
		return
	}
	a.betterLyricsStatus = ""
	a.betterLyricsServer = server
	a.publishBetterLyrics()
	a.scheduleBetterLyricsSync()
	a.redraw()
}

func (a *app) stopBetterLyricsIntegration() {
	if a.betterLyricsTimer != nil {
		a.betterLyricsTimer.Stop()
		a.betterLyricsTimer = nil
	}
	if a.betterLyricsServer == nil {
		return
	}
	if err := a.betterLyricsServer.Close(); err != nil {
		log.Printf("stopping BetterLyrics integration: %v", err)
	}
	a.betterLyricsServer = nil
	a.redraw()
}

func (a *app) scheduleBetterLyricsSync() {
	server := a.betterLyricsServer
	if server == nil {
		return
	}
	delay := 250 * time.Millisecond
	if a.pl != nil && !a.pl.Playing() {
		// 排空设备缓冲后的最终位置可能晚于最后一次播放状态回调。
		delay = 500 * time.Millisecond
		if a.betterLyricsTimer != nil {
			a.betterLyricsTimer.Stop()
		}
	} else if a.betterLyricsTimer != nil {
		return
	}
	var scheduled *time.Timer
	scheduled = time.AfterFunc(delay, func() {
		mygo.RunOnMain(func() {
			if a.betterLyricsServer != server || a.betterLyricsTimer != scheduled {
				return
			}
			a.betterLyricsTimer = nil
			a.publishBetterLyrics()
			// 暂停先排空音频缓冲，补发最终实际位置后停止定时更新。
			if a.pl != nil && a.pl.Playing() {
				a.scheduleBetterLyricsSync()
			}
		})
	})
	a.betterLyricsTimer = scheduled
}

func (a *app) publishBetterLyrics() {
	if a.betterLyricsServer == nil || a.pl == nil {
		return
	}
	status := betterlyrics.Status{}
	var lyrics *betterlyrics.Lyrics
	if track := a.pl.Current(); track != nil {
		key := track.Source + "\x00" + track.ID + "\x00" + track.PlaybackID + "\x00" + track.Name + "\x00" + track.ArtistNames() + "\x00" + track.Album.Name
		if key != a.betterLyricsTrackKey {
			a.betterLyricsTrackID++
			a.betterLyricsTrackKey = key
		}
		status = betterlyrics.Status{
			TrackID:    a.betterLyricsTrackID,
			Title:      track.Name,
			Artist:     track.ArtistNames(),
			Album:      track.Album.Name,
			Playing:    a.pl.Playing(),
			PositionMs: a.pl.Position().Milliseconds(),
			DurationMs: a.pl.Duration().Milliseconds(),
		}
		if source, loaded := a.pl.LyricsSource(); loaded && source != nil {
			lyrics = &betterlyrics.Lyrics{
				TrackID:     status.TrackID,
				LRC:         source.LRC,
				Translation: source.Translation,
				Lines:       source.Lines,
			}
		}
	} else {
		a.betterLyricsTrackKey = ""
	}
	a.betterLyricsServer.Publish(status, lyrics)
}

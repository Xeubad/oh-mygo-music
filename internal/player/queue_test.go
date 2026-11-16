package player

import (
	"reflect"
	"testing"
	"time"

	"mygo-music/internal/music"
)

// 下一首插入保留当前进度及其余播放顺序，随机播放时也不能重洗整个队列。
func TestPlayNextKeepsCurrentAndRemainingOrder(t *testing.T) {
	for _, shuffled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "shuffled"}[shuffled], func(t *testing.T) {
			p := &Player{at: -1}
			p.Cue([]music.Track{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}})
			p.at = 1
			want := []string{"a", "b", "picked", "c", "d"}
			if shuffled {
				p.order = []int{2, 0, 3, 1}
				want = []string{"c", "a", "picked", "d", "b"}
			}
			p.shuffle = shuffled
			p.track = &p.queue[p.order[p.at]]
			current := p.Current().ID
			p.Hold(42 * time.Second)
			p.repeat = RepeatOne
			changed := 0
			p.Changed = func() { changed++ }
			p.PlayNext(music.Track{ID: "picked", Source: "other", PlaybackID: "old-playback"})
			order, at := p.PlayOrder()
			var got []string
			for _, queued := range order {
				got = append(got, p.Queue()[queued].ID)
			}
			if !reflect.DeepEqual(got, want) || at != 1 {
				t.Fatalf("play order %v at %d, want %v at 1", got, at, want)
			}
			if p.Current().ID != current || !p.Playing() || p.Position() != 42*time.Second || p.Shuffle() != shuffled || p.Repeat() != RepeatOne {
				t.Fatalf("insertion interrupted playback: current %+v, position %s", p.Current(), p.Position())
			}
			if queued := p.Queue()[order[at+1]]; queued.Source != "other" || queued.PlaybackID != "" {
				t.Fatalf("new queue position inherited another playback: %+v", queued)
			}
			if _, ok := p.Session(); !ok || changed != 1 {
				t.Fatal("queue insertion was not announced for session persistence")
			}
		})
	}
}

// 空队列直接播放；已有队列则保留原曲目，下一次切歌实际播放新插入的位置。
func TestPlayNextStartsOrAdvancesToInsertedTrack(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "empty"}[empty], func(t *testing.T) {
			p, posted, queue := pickyQueue()
			defer p.stop()
			if !empty {
				p.PlayTrack(queue, 0)
				settle(t, posted, p)
			}
			p.PlayNext(queue[2])
			if !empty {
				if p.Current().ID != "a" || len(p.Queue()) != 4 {
					t.Fatal("insertion replaced the original queue or current track")
				}
				p.Next()
			}
			settle(t, posted, p)
			if p.Current().ID != "c" || !p.Playing() || p.Err() != "" {
				t.Fatalf("inserted track did not play: %+v, error %q", p.Current(), p.Err())
			}
		})
	}
}

// 重复入队是独立位置；插入不能把旧位置的失败记录或已播身份错误转移给新项。
func TestPlayNextKeepsFailedPositionsDistinct(t *testing.T) {
	p := &Player{at: -1}
	p.Cue([]music.Track{{ID: "a"}, {ID: "b"}, {ID: "b", PlaybackID: "played-b"}})
	p.playback = &playbackAttempt{failed: map[int]bool{2: true}, recoveryUsed: true}
	p.PlayNext(p.Queue()[2])
	if !reflect.DeepEqual(p.playback.failed, map[int]bool{3: true}) || !p.playback.recoveryUsed {
		t.Fatalf("insertion lost playback history: %+v", p.playback)
	}
	if p.Queue()[1].PlaybackID != "" || p.Queue()[3].PlaybackID != "played-b" {
		t.Fatalf("repeated track inherited or lost a played version: %+v", p.Queue())
	}
}

// 来源仍持有旧队列时，等其自然切歌再接管下一项，插入本身不能重启当前曲目。
func TestPlayNextOverridesOldRemoteQueueWithoutRestartingCurrent(t *testing.T) {
	rig := newRemoteRig(t, 3)
	defer rig.p.stop()
	rig.src.set(playingState("a", 42000), nil)
	rig.settled()
	rig.poll(time.Second)
	position := rig.p.Position()
	rig.p.PlayNext(music.Track{ID: "picked", Source: "remote", Duration: 200000})
	if rig.p.Current().ID != "a" || rig.p.Position() != position || !rig.p.Playing() {
		t.Fatal("insertion restarted remote playback")
	}
	select {
	case command := <-rig.src.cmd:
		t.Fatalf("insertion sent an unexpected remote command: %s", command)
	default:
	}
	rig.src.set(playingState("b", 1000), nil)
	rig.poll(time.Second)
	rig.await("play")
	rig.settle()
	if rig.p.Current().ID != "picked" || !rig.p.Playing() {
		t.Fatalf("remote source bypassed inserted track: %+v", rig.p.Current())
	}
}

// 电台已经等待后续歌曲时，手动插入可继续播放，不必等待旧电台请求返回。
func TestPlayNextResumesRadioWaitingForFeed(t *testing.T) {
	p, posted, queue := pickyQueue()
	defer p.stop()
	p.PlayRadio("station", queue[:1], func(func([]music.Track)) {})
	settle(t, posted, p)
	p.Next()
	if !p.Loading() {
		t.Fatal("radio did not wait for its feed")
	}
	p.PlayNext(queue[2])
	settle(t, posted, p)
	if p.Current().ID != "c" || !p.Playing() || p.Radio() != "station" {
		t.Fatalf("manual insertion did not resume the radio: %+v", p.Current())
	}
}

package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 同版候选的重播身份指向该候选实际来源；拆分后原入队项和已播项各保留自己的版本。
func TestAggregateCandidateCarriesPlayedRecordingIdentity(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "two"}
	right := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "one"}
	for _, source := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	pair := map[string]any{"left": left, "right": right}
	if _, err := p.Aggregate("confirmLink", pair); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	id := page.Sections[0].Tracks[0].ID
	raw, err := p.Aggregate("playbackPlan", map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Candidates []struct {
			Platform   string
			AccountID  string
			TrackID    string
			PlaybackID string
		}
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("缺少同版候选：%s", raw)
	}
	for _, candidate := range plan.Candidates {
		var frozen struct {
			Version  int
			Identity music.AggregateSource
			Sources  []music.AggregateSource
		}
		if !strings.HasPrefix(candidate.PlaybackID, "aggregate:") {
			t.Fatalf("候选缺少可恢复的实际版本身份：%+v", candidate)
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(candidate.PlaybackID, "aggregate:")), &frozen); err != nil {
			t.Fatal(err)
		}
		if frozen.Version != 1 || frozen.Identity.Platform != candidate.Platform || frozen.Identity.AccountID != candidate.AccountID || frozen.Identity.TrackID != candidate.TrackID || len(frozen.Sources) != 2 {
			t.Fatalf("实际版本身份或冻结来源丢失：%+v", frozen)
		}
	}
	if _, err := p.Aggregate("revokeLink", pair); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range plan.Candidates {
		replay, err := p.PlaybackPlan(context.Background(), candidate.PlaybackID, "flac", "replay")
		if err != nil {
			t.Fatal(err)
		}
		if len(replay.Candidates) != 1 || replay.Candidates[0].Platform != candidate.Platform || replay.Candidates[0].AccountID != candidate.AccountID || replay.Candidates[0].TrackID != candidate.TrackID {
			t.Fatalf("拆分后的重播改换了版本：%+v", replay)
		}
	}
}

// 在途资格只认可旧队列的冻结来源；撤销和退出立即收紧，新增关联不能给旧轮次加来源。
func TestAggregateCandidateValidityTracksFrozenRecordingAndLogout(t *testing.T) {
	p := loadAggregate(t)
	if !p.Can("candidateValid") {
		t.Fatal("聚合插件缺少在途候选资格校验")
	}
	left := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "two"}
	right := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "one"}
	for _, s := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": s.Platform, "accountID": s.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": s.TrackID, "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	pair := map[string]any{"left": left, "right": right}
	if _, err := p.Aggregate("confirmLink", pair); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	id := page.Sections[0].Tracks[0].ID
	var queued struct{ Identity music.AggregateSource }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(id, "aggregate:")), &queued); err != nil {
		t.Fatal(err)
	}
	candidate := func(s music.AggregateSource) music.PlaybackCandidate {
		return music.PlaybackCandidate{ID: "frozen", Platform: s.Platform, AccountID: s.AccountID, TrackID: s.TrackID, Quality: "flac"}
	}
	check := func(s music.AggregateSource, want bool) {
		t.Helper()
		valid, err := p.CandidateValid(context.Background(), id, "round-1", candidate(s))
		if err != nil || valid != want {
			t.Fatalf("候选资格不符：source=%+v valid=%v want=%v error=%v", s, valid, want, err)
		}
	}
	check(left, true)
	check(right, true)
	third := music.AggregateSource{Platform: "mg", AccountID: "c", TrackID: "three"}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": third}); err != nil {
		t.Fatal(err)
	}
	check(third, false)
	if _, err := p.Aggregate("revokeLink", pair); err != nil {
		t.Fatal(err)
	}
	for _, s := range []music.AggregateSource{left, right} {
		check(s, s == queued.Identity)
	}
	if _, err := p.Aggregate("logout", map[string]any{"platform": queued.Identity.Platform, "accountID": queued.Identity.AccountID}); err != nil {
		t.Fatal(err)
	}
	check(queued.Identity, false)
}

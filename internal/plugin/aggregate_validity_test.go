package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

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

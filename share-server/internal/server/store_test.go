package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testMetadata is a valid full-song snapshot with the fields the desktop sends.
func testMetadata() Metadata {
	return Metadata{
		Title: "曲名", Artists: "歌手", SourceID: "netease", TrackID: "track-1",
		Quality: "320k", Template: "card", Mode: "full", DurationMs: 12000,
	}
}

// newTestStore returns a store on a fake clock so expiry tests do not sleep.
func newTestStore(t *testing.T) (*Store, string, func(time.Duration)) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	return store, dir, func(d time.Duration) { now = now.Add(d) }
}

// publish drives a share through create, upload and finish.
func publish(t *testing.T, store *Store, metadata Metadata, idempotencyKey string) Share {
	t.Helper()
	share, created, err := store.Create(metadata, idempotencyKey, requestKey(metadata))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Fatalf("Create returned an existing share for a fresh key")
	}
	if _, err := store.BeginAudio(share.ID); err != nil {
		t.Fatalf("BeginAudio: %v", err)
	}
	source := filepath.Join(store.TempDir(), "transcoded-"+share.ID+".mp3")
	if err := os.WriteFile(source, []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	finished, err := store.FinishAudio(share.ID, source, metadata.DurationMs)
	if err != nil {
		t.Fatalf("FinishAudio: %v", err)
	}
	return finished
}

func TestCreateIsIdempotent(t *testing.T) {
	store, _, _ := newTestStore(t)
	metadata := testMetadata()
	first, created, err := store.Create(metadata, "key-000001", requestKey(metadata))
	if err != nil || !created {
		t.Fatalf("Create = (%v, %v), want a new share", created, err)
	}
	if first.Status != statusAwaitingUpload {
		t.Errorf("Status = %q, want %q", first.Status, statusAwaitingUpload)
	}
	repeat, created, err := store.Create(metadata, "key-000001", requestKey(metadata))
	if err != nil || created {
		t.Fatalf("repeat Create = (%v, %v), want the stored share", created, err)
	}
	if repeat.ID != first.ID {
		t.Errorf("repeat ID = %q, want %q", repeat.ID, first.ID)
	}
	conflict := testMetadata()
	conflict.Title = "另一首歌"
	if _, _, err := store.Create(conflict, "key-000001", requestKey(conflict)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("conflicting Create err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestReuseOnlyReturnsPlayableShares(t *testing.T) {
	store, _, _ := newTestStore(t)
	metadata := testMetadata()
	share, created, err := store.Create(metadata, "key-000002", requestKey(metadata))
	if err != nil || !created {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := store.Reuse(requestKey(metadata)); ok {
		t.Fatal("Reuse returned a share that has no audio yet")
	}
	if _, err := store.BeginAudio(share.ID); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(store.TempDir(), "audio.mp3")
	if err := os.WriteFile(source, []byte("ID3fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishAudio(share.ID, source, metadata.DurationMs); err != nil {
		t.Fatal(err)
	}
	reused, ok := store.Reuse(requestKey(metadata))
	if !ok || reused.ID != share.ID {
		t.Fatalf("Reuse = (%q, %v), want %q", reused.ID, ok, share.ID)
	}
}

func TestReuseRequiresTheRequestedCover(t *testing.T) {
	store, _, _ := newTestStore(t)
	metadata := testMetadata()
	metadata.CoverHash = "aa11"
	share, _, err := store.Create(metadata, "key-000003", requestKey(metadata))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAudio(share.ID); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(store.TempDir(), "audio.mp3")
	if err := os.WriteFile(source, []byte("ID3fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishAudio(share.ID, source, metadata.DurationMs); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Reuse(requestKey(metadata)); ok {
		t.Fatal("Reuse matched a share whose cover was never uploaded")
	}
	cover := filepath.Join(store.TempDir(), "cover.png")
	if err := os.WriteFile(cover, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCover(share.ID, cover, "aa11"); !errors.Is(err, ErrAlreadyReady) {
		t.Fatalf("SetCover after publishing = %v, want ErrAlreadyReady", err)
	}
}

func TestCoverHashMustMatchTheRequest(t *testing.T) {
	store, _, _ := newTestStore(t)
	metadata := testMetadata()
	metadata.CoverHash = "aa11"
	share, _, err := store.Create(metadata, "key-000010", requestKey(metadata))
	if err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(store.TempDir(), "cover.png")
	if err := os.WriteFile(cover, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCover(share.ID, cover, "bb22"); !errors.Is(err, ErrCoverMismatch) {
		t.Fatalf("mismatched SetCover = %v, want ErrCoverMismatch", err)
	}
	if err := store.SetCover(share.ID, cover, "aa11"); err != nil {
		t.Fatalf("matching SetCover: %v", err)
	}
	if _, err := store.BeginAudio(share.ID); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(store.TempDir(), "audio.mp3")
	if err := os.WriteFile(audio, []byte("ID3fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishAudio(share.ID, audio, metadata.DurationMs); err != nil {
		t.Fatal(err)
	}
	if reused, ok := store.Reuse(requestKey(metadata)); !ok || reused.ID != share.ID {
		t.Fatalf("Reuse = (%q, %v), want %q", reused.ID, ok, share.ID)
	}
}

func TestExpiryReclaimsMedia(t *testing.T) {
	store, dir, advance := newTestStore(t)
	metadata := testMetadata()
	share := publish(t, store, metadata, "key-000004")
	advance(time.Hour + time.Second)
	if _, ok := store.Published(share.ID); ok {
		t.Fatal("Published returned an expired share")
	}
	if got, _ := store.Get(share.ID); got.Status != statusExpired {
		t.Errorf("Status = %q, want %q", got.Status, statusExpired)
	}
	if err := store.Prune(store.Now()); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shares", share.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("share media still exists after expiry: %v", err)
	}
	advance(terminalRetention + time.Minute)
	if err := store.Prune(store.Now()); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if count := store.Count(); count != 0 {
		t.Errorf("Count = %d after retention, want 0", count)
	}
}

func TestRevokeRemovesMediaAndPersists(t *testing.T) {
	store, dir, _ := newTestStore(t)
	share := publish(t, store, testMetadata(), "key-000005")
	if ok, err := store.Revoke(share.ID); !ok || err != nil {
		t.Fatalf("Revoke = (%v, %v)", ok, err)
	}
	if _, ok := store.Published(share.ID); ok {
		t.Fatal("Published returned a revoked share")
	}
	if got, _ := store.Get(share.ID); got.Status != statusRevoked {
		t.Errorf("Status = %q, want %q", got.Status, statusRevoked)
	}
	if _, err := os.Stat(filepath.Join(dir, "shares", share.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("share media still exists after revoke: %v", err)
	}
	if ok, err := store.Revoke(share.ID); !ok || err != nil {
		t.Errorf("second Revoke = (%v, %v), want it to stay idempotent", ok, err)
	}
	reopened, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := reopened.Get(share.ID); !ok || got.Status != statusRevoked {
		t.Errorf("reopened status = %q, want revoked", got.Status)
	}
}

func TestReadyShareSurvivesRestart(t *testing.T) {
	store, dir, _ := newTestStore(t)
	share := publish(t, store, testMetadata(), "key-000006")
	reopened, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := reopened.Published(share.ID); !ok || got.DurationMs != 12000 {
		t.Errorf("reopened Published = (%q, %v), want the ready share", got.Status, ok)
	}
}

func TestInterruptedUploadBecomesFailedOnRestart(t *testing.T) {
	store, dir, _ := newTestStore(t)
	share, _, err := store.Create(testMetadata(), "key-000007", requestKey(testMetadata()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAudio(share.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get(share.ID)
	if !ok || got.Status != statusFailed || got.Error != "interrupted" {
		t.Errorf("reopened share = (%q, %q, %v), want failed/interrupted", got.Status, got.Error, ok)
	}
}

func TestBeginAudioStateTransitions(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.BeginAudio("missing-identifier"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	share := publish(t, store, testMetadata(), "key-000008")
	if _, err := store.BeginAudio(share.ID); !errors.Is(err, ErrAlreadyReady) {
		t.Errorf("ready share: err = %v, want ErrAlreadyReady", err)
	}
	if _, err := store.FinishAudio(share.ID, "/tmp/ignored.mp3", 1); !errors.Is(err, ErrNotUploadable) {
		t.Errorf("FinishAudio on ready share: err = %v, want ErrNotUploadable", err)
	}
	second, _, err := store.Create(testMetadata(), "key-000009", requestKey(testMetadata()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAudio(second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAudio(second.ID); !errors.Is(err, ErrUploadInProgress) {
		t.Errorf("processing share: err = %v, want ErrUploadInProgress", err)
	}
	store.ResetAudio(second.ID)
	if got, _ := store.Get(second.ID); got.Status != statusAwaitingUpload {
		t.Errorf("after ResetAudio status = %q, want awaiting_upload", got.Status)
	}
	if _, err := store.Revoke(second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAudio(second.ID); !errors.Is(err, ErrShareGone) {
		t.Errorf("revoked share: err = %v, want ErrShareGone", err)
	}
}

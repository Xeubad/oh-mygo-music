package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	statusAwaitingUpload = "awaiting_upload"
	statusProcessing     = "processing"
	statusReady          = "ready"
	statusFailed         = "failed"
	statusRevoked        = "revoked"
	statusExpired        = "expired"

	indexVersion       = 1
	maxShares          = 500
	terminalRetention  = 24 * time.Hour
	lastOpenedInterval = time.Minute
)

// Share is one published snapshot together with the state of its upload.
// The metadata is the exact snapshot the desktop submitted; media lives beside
// the index in data/shares/{id}/.
type Share struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	Metadata       Metadata   `json:"metadata"`
	RequestKey     string     `json:"requestKey"`
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	ReadyAt        *time.Time `json:"readyAt,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	TerminalAt     *time.Time `json:"terminalAt,omitempty"`
	LastOpenedAt   *time.Time `json:"lastOpenedAt,omitempty"`
	Error          string     `json:"error,omitempty"`
	DurationMs     int64      `json:"durationMs,omitempty"`
	HasAudio       bool       `json:"hasAudio,omitempty"`
	HasCover       bool       `json:"hasCover,omitempty"`
	CoverHash      string     `json:"coverHash,omitempty"`
}

// Store owns the share index and the media directory. Every mutation is
// persisted before it is acknowledged, so a restart never loses a published
// share or resurrects a revoked one.
type Store struct {
	dir       string
	sharesDir string
	tmpDir    string
	indexPath string
	ttl       time.Duration

	mu     sync.Mutex
	shares map[string]*Share
	now    func() time.Time
}

type indexFile struct {
	Version int     `json:"version"`
	Shares  []Share `json:"shares"`
}

// OpenStore loads or creates the data directory. Interrupted uploads are
// demoted to failed, missing media is noticed, expired shares are pruned, and
// orphaned directories are removed before the store is handed out.
func OpenStore(dir string, ttl time.Duration) (*Store, error) {
	store := &Store{
		dir:       dir,
		sharesDir: filepath.Join(dir, "shares"),
		tmpDir:    filepath.Join(dir, "tmp"),
		indexPath: filepath.Join(dir, "shares.json"),
		ttl:       ttl,
		shares:    make(map[string]*Share),
		now:       time.Now,
	}
	for _, path := range []string{store.dir, store.sharesDir, store.tmpDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	now := store.now()
	changed := store.reconcileLocked(now)
	pruned, dirs := store.pruneLocked(now)
	changed = changed || pruned
	if changed {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	for _, path := range dirs {
		_ = os.RemoveAll(path)
	}
	if err := store.removeOrphans(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read share index: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var index indexFile
	if err := json.Unmarshal(data, &index); err != nil {
		return fmt.Errorf("decode share index: %w", err)
	}
	if index.Version > indexVersion {
		return fmt.Errorf("share index version %d is newer than this server supports", index.Version)
	}
	for i := range index.Shares {
		share := index.Shares[i]
		if !validID(share.ID) {
			continue
		}
		copied := share
		s.shares[share.ID] = &copied
	}
	return nil
}

// reconcileLocked repairs state that a crash may have left inconsistent.
func (s *Store) reconcileLocked(now time.Time) bool {
	changed := false
	for _, share := range s.shares {
		if share.HasAudio {
			if _, err := os.Stat(s.audioPath(share.ID)); err != nil {
				share.HasAudio = false
				changed = true
			}
		}
		if share.HasCover {
			if _, err := os.Stat(s.coverPath(share.ID)); err != nil {
				share.HasCover = false
				share.CoverHash = ""
				changed = true
			}
		}
		switch share.Status {
		case statusProcessing:
			share.Status = statusFailed
			share.Error = "interrupted"
			share.UpdatedAt = now
			changed = true
		case statusReady:
			if !share.HasAudio {
				share.Status = statusFailed
				share.Error = "audio_missing"
				share.UpdatedAt = now
				changed = true
			}
		case statusRevoked:
			if share.RevokedAt == nil {
				share.RevokedAt = timePtr(now)
				changed = true
			}
			if share.TerminalAt == nil {
				share.TerminalAt = timePtr(now)
				changed = true
			}
		}
	}
	return changed
}

// pruneLocked expires shares past their deadline and deletes terminal records
// after the retention window. It returns the directories that the caller should
// remove once the index has been persisted.
func (s *Store) pruneLocked(now time.Time) (bool, []string) {
	changed := false
	var dirs []string
	for id, share := range s.shares {
		terminal := share.Status == statusRevoked || share.Status == statusExpired
		if !terminal && !now.Before(share.ExpiresAt) {
			share.Status = statusExpired
			share.TerminalAt = timePtr(now)
			share.HasAudio, share.HasCover = false, false
			share.CoverHash = ""
			share.UpdatedAt = now
			dirs = append(dirs, s.shareDir(id))
			changed = true
			terminal = true
		}
		if !terminal {
			continue
		}
		if share.TerminalAt == nil {
			share.TerminalAt = timePtr(now)
			changed = true
		}
		if now.Sub(*share.TerminalAt) >= terminalRetention {
			delete(s.shares, id)
			dirs = append(dirs, s.shareDir(id))
			changed = true
		}
	}
	return changed, dirs
}

func (s *Store) removeOrphans() error {
	entries, err := os.ReadDir(s.sharesDir)
	if err != nil {
		return fmt.Errorf("read share directory: %w", err)
	}
	s.mu.Lock()
	known := make(map[string]bool, len(s.shares))
	for id := range s.shares {
		known[id] = true
	}
	s.mu.Unlock()
	for _, entry := range entries {
		if known[entry.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.sharesDir, entry.Name())); err != nil {
			return fmt.Errorf("remove orphaned share media: %w", err)
		}
	}
	entries, err = os.ReadDir(s.tmpDir)
	if err != nil {
		return fmt.Errorf("read temporary directory: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(s.tmpDir, entry.Name())); err != nil {
			return fmt.Errorf("remove temporary upload: %w", err)
		}
	}
	return nil
}

// Create registers a new share, or returns the one already stored under the
// same idempotency key. The bool reports whether a new share was created.
func (s *Store) Create(metadata Metadata, idempotencyKey, requestKey string) (Share, bool, error) {
	if idempotencyKey == "" {
		return Share{}, false, errors.New("idempotency key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if changed, dirs := s.pruneLocked(now); changed {
		if err := s.saveLocked(); err != nil {
			return Share{}, false, err
		}
		for _, path := range dirs {
			_ = os.RemoveAll(path)
		}
	}
	for _, share := range s.shares {
		if share.IdempotencyKey != idempotencyKey {
			continue
		}
		if share.RequestKey != requestKey {
			return Share{}, false, ErrIdempotencyConflict
		}
		return s.effectiveLocked(share, now), false, nil
	}
	if len(s.shares) >= maxShares {
		return Share{}, false, ErrTooManyShares
	}
	id, err := newShareID()
	if err != nil {
		return Share{}, false, err
	}
	share := &Share{
		ID:             id,
		Status:         statusAwaitingUpload,
		Metadata:       metadata,
		RequestKey:     requestKey,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
		UpdatedAt:      now,
		ExpiresAt:      now.Add(s.ttl),
	}
	s.shares[id] = share
	if err := s.saveLocked(); err != nil {
		delete(s.shares, id)
		return Share{}, false, err
	}
	return *share, true, nil
}

func (s *Store) Get(id string) (Share, bool) {
	if !validID(id) {
		return Share{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	share, ok := s.shares[id]
	if !ok {
		return Share{}, false
	}
	return s.effectiveLocked(share, s.now()), true
}

// Published returns a share that a listener may actually play.
func (s *Store) Published(id string) (Share, bool) {
	share, ok := s.Get(id)
	if !ok || share.Status != statusReady || !share.HasAudio {
		return Share{}, false
	}
	return share, true
}

func (s *Store) List() []Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]Share, 0, len(s.shares))
	for _, share := range s.shares {
		out = append(out, s.effectiveLocked(share, now))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Reuse returns the newest playable share with the same snapshot.
func (s *Store) Reuse(requestKey string) (Share, bool) {
	if requestKey == "" {
		return Share{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var best *Share
	for _, share := range s.shares {
		effective := s.effectiveLocked(share, now)
		if effective.Status != statusReady || !effective.HasAudio || effective.RequestKey != requestKey {
			continue
		}
		if share.Metadata.CoverHash != "" && (!effective.HasCover || !strings.EqualFold(effective.CoverHash, share.Metadata.CoverHash)) {
			continue
		}
		if best == nil || share.CreatedAt.After(best.CreatedAt) {
			best = share
		}
	}
	if best == nil {
		return Share{}, false
	}
	return s.effectiveLocked(best, now), true
}

// BeginAudio marks an upload as in progress so a concurrent retry cannot start
// a second transcode for the same share.
func (s *Store) BeginAudio(id string) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	share, err := s.mutableLocked(id)
	if err != nil {
		return Share{}, err
	}
	switch s.effectiveLocked(share, s.now()).Status {
	case statusReady:
		return Share{}, ErrAlreadyReady
	case statusProcessing:
		return Share{}, ErrUploadInProgress
	case statusRevoked, statusExpired:
		return Share{}, ErrShareGone
	}
	previous := *share
	share.Status = statusProcessing
	share.Error = ""
	share.UpdatedAt = s.now()
	if err := s.saveLocked(); err != nil {
		*share = previous
		return Share{}, err
	}
	return *share, nil
}

// FinishAudio moves the transcoded file into place and publishes the share.
func (s *Store) FinishAudio(id, sourcePath string, durationMs int64) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	share, err := s.mutableLocked(id)
	if err != nil {
		return Share{}, err
	}
	switch s.effectiveLocked(share, s.now()).Status {
	case statusRevoked, statusExpired:
		return Share{}, ErrShareGone
	case statusProcessing:
	default:
		return Share{}, ErrNotUploadable
	}
	if err := os.MkdirAll(s.shareDir(id), 0o700); err != nil {
		return Share{}, err
	}
	destination := s.audioPath(id)
	if err := os.Rename(sourcePath, destination); err != nil {
		return Share{}, fmt.Errorf("store audio: %w", err)
	}
	now := s.now()
	previous := *share
	share.Status = statusReady
	share.HasAudio = true
	share.DurationMs = durationMs
	share.ReadyAt = timePtr(now)
	share.ExpiresAt = now.Add(s.ttl)
	share.Error = ""
	share.UpdatedAt = now
	if err := s.saveLocked(); err != nil {
		*share = previous
		_ = os.Remove(destination)
		return Share{}, err
	}
	return *share, nil
}

// FailAudio records a terminal transcode failure. The original upload is never
// kept, so a failed share can never fall back to unprocessed media.
func (s *Store) FailAudio(id, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	share, err := s.mutableLocked(id)
	if err != nil {
		return
	}
	switch s.effectiveLocked(share, s.now()).Status {
	case statusRevoked, statusExpired:
		return
	}
	share.Status = statusFailed
	share.Error = code
	share.HasAudio = false
	share.DurationMs = 0
	share.UpdatedAt = s.now()
	_ = os.Remove(s.audioPath(id))
	_ = s.saveLocked()
}

// ResetAudio returns an interrupted upload to the awaiting state so the desktop
// can retry after a transient failure such as ffmpeg being unavailable.
func (s *Store) ResetAudio(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	share, err := s.mutableLocked(id)
	if err != nil || share.Status != statusProcessing {
		return
	}
	share.Status = statusAwaitingUpload
	share.Error = ""
	share.UpdatedAt = s.now()
	_ = s.saveLocked()
}

// SetCover stores the cover image. Media may arrive before or after the audio.
func (s *Store) SetCover(id, sourcePath, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	share, err := s.mutableLocked(id)
	if err != nil {
		return err
	}
	status := s.effectiveLocked(share, s.now()).Status
	if status == statusRevoked || status == statusExpired {
		return ErrShareGone
	}
	if share.Metadata.CoverHash != "" && !strings.EqualFold(share.Metadata.CoverHash, hash) {
		return ErrCoverMismatch
	}
	if status == statusReady {
		if share.HasCover && strings.EqualFold(share.CoverHash, hash) {
			return nil
		}
		return ErrAlreadyReady
	}
	if err := os.MkdirAll(s.shareDir(id), 0o700); err != nil {
		return err
	}
	destination := s.coverPath(id)
	if err := os.Rename(sourcePath, destination); err != nil {
		return fmt.Errorf("store cover: %w", err)
	}
	previous := *share
	share.HasCover = true
	share.CoverHash = hash
	share.UpdatedAt = s.now()
	if err := s.saveLocked(); err != nil {
		*share = previous
		_ = os.Remove(destination)
		return err
	}
	return nil
}

// Revoke makes a share permanently unavailable. It reports whether the share
// existed; an unknown id is not an error because DELETE is idempotent.
func (s *Store) Revoke(id string) (bool, error) {
	if !validID(id) {
		return false, nil
	}
	s.mu.Lock()
	share, ok := s.shares[id]
	if !ok {
		s.mu.Unlock()
		return false, nil
	}
	now := s.now()
	previous := *share
	share.Status = statusRevoked
	if share.RevokedAt == nil {
		share.RevokedAt = timePtr(now)
	}
	share.TerminalAt = timePtr(now)
	share.HasAudio, share.HasCover = false, false
	share.CoverHash = ""
	share.UpdatedAt = now
	err := s.saveLocked()
	if err != nil {
		*share = previous
	}
	s.mu.Unlock()
	// Media is removed even when the index write failed: revocation must win.
	_ = os.RemoveAll(s.shareDir(id))
	if err != nil {
		return true, err
	}
	return true, nil
}

// TouchOpened records that the page was viewed, at most once a minute so a
// refresh loop cannot rewrite the index continuously.
func (s *Store) TouchOpened(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	share, ok := s.shares[id]
	if !ok || s.effectiveLocked(share, s.now()).Status != statusReady {
		return
	}
	now := s.now()
	if share.LastOpenedAt != nil && now.Sub(*share.LastOpenedAt) < lastOpenedInterval {
		return
	}
	share.LastOpenedAt = timePtr(now)
	_ = s.saveLocked()
}

// Prune expires due shares and reclaims media. It is safe to call on a timer.
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	changed, dirs := s.pruneLocked(now)
	var err error
	if changed {
		err = s.saveLocked()
	}
	s.mu.Unlock()
	for _, path := range dirs {
		_ = os.RemoveAll(path)
	}
	return err
}

func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shares)
}

func (s *Store) Now() time.Time { return s.now() }

func (s *Store) TempDir() string { return s.tmpDir }

// AudioPath and CoverPath expose the private media locations to the HTTP
// layer. Invalid identifiers return an empty string.
func (s *Store) AudioPath(id string) string {
	if !validID(id) {
		return ""
	}
	return s.audioPath(id)
}

func (s *Store) CoverPath(id string) string {
	if !validID(id) {
		return ""
	}
	return s.coverPath(id)
}

func (s *Store) mutableLocked(id string) (*Share, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	share, ok := s.shares[id]
	if !ok {
		return nil, ErrNotFound
	}
	switch share.Status {
	case statusRevoked:
		return nil, ErrShareGone
	}
	if !s.now().Before(share.ExpiresAt) {
		return nil, ErrShareGone
	}
	return share, nil
}

// effectiveLocked projects the time-based state without mutating the record.
func (s *Store) effectiveLocked(share *Share, now time.Time) Share {
	effective := *share
	switch {
	case share.RevokedAt != nil || share.Status == statusRevoked:
		effective.Status = statusRevoked
		effective.HasAudio, effective.HasCover = false, false
		effective.CoverHash = ""
	case !now.Before(share.ExpiresAt):
		effective.Status = statusExpired
		effective.HasAudio, effective.HasCover = false, false
		effective.CoverHash = ""
	case share.Status == statusReady && !share.HasAudio:
		effective.Status = statusFailed
	}
	return effective
}

func (s *Store) shareDir(id string) string  { return filepath.Join(s.sharesDir, id) }
func (s *Store) audioPath(id string) string { return filepath.Join(s.sharesDir, id, "audio.mp3") }
func (s *Store) coverPath(id string) string { return filepath.Join(s.sharesDir, id, "cover.png") }

func (s *Store) saveLocked() error {
	shares := make([]Share, 0, len(s.shares))
	for _, share := range s.shares {
		shares = append(shares, *share)
	}
	sort.Slice(shares, func(i, j int) bool {
		if !shares[i].CreatedAt.Equal(shares[j].CreatedAt) {
			return shares[i].CreatedAt.Before(shares[j].CreatedAt)
		}
		return shares[i].ID < shares[j].ID
	})
	data, err := json.Marshal(indexFile{Version: indexVersion, Shares: shares})
	if err != nil {
		return fmt.Errorf("encode share index: %w", err)
	}
	temp := s.indexPath + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write share index: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(temp)
		return fmt.Errorf("write share index: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(temp)
		return fmt.Errorf("sync share index: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return fmt.Errorf("close share index: %w", err)
	}
	if err := os.Rename(temp, s.indexPath); err != nil {
		os.Remove(temp)
		return fmt.Errorf("replace share index: %w", err)
	}
	if directory, err := os.Open(s.dir); err == nil {
		_ = directory.Sync()
		directory.Close()
	}
	return nil
}

func newShareID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func validID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func timePtr(value time.Time) *time.Time { return &value }

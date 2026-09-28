package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

type SettingsStore interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	SetIfAbsent(context.Context, string, string) (bool, error)
}

type Options struct {
	Backend   string
	LocalPath string
	// S3 is the public assets bucket. It backs artwork, branding, markers,
	// chapter thumbnails, and downloaded subtitles.
	S3 *s3client.Client
	// S3Private is the private operational bucket. It backs diagnostic bundles,
	// job artifacts, and profile avatars. A configured private bucket always
	// wins for those, including when Backend is local, so an install that has
	// been keeping avatars there keeps reading them after artwork moves to disk.
	S3Private *s3client.Client
	Settings  SettingsStore
}

// Stores are the blob stores a process owns. They are separate because the
// public bucket can serve browsers directly under token auth and the private
// one never does. A filesystem has no such distinction, so a local backend with
// no private bucket puts both in one root and the key prefixes each caller
// already uses keep the namespaces apart.
type Stores struct {
	// Assets backs artwork, branding, markers, chapter thumbnails, and (once
	// migrated) downloaded subtitles. It carries the recorded storage identity.
	Assets Store
	// Operational is for diagnostic bundles, job artifacts, and profile
	// avatars; those callers still take their storage directly until they
	// migrate. Nil only when there is nowhere to put them: an S3 backend with no
	// private bucket configured.
	Operational Store
}

// Local reports whether both stores are one filesystem root. False when a
// private bucket owns the operational store, even on a local backend.
func (s Stores) Local() bool { return s.Assets != nil && s.Assets == s.Operational }

func Open(ctx context.Context, opts Options) (Stores, string, error) {
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" || backend == "auto" {
		if opts.S3 != nil {
			backend = BackendS3
		} else {
			backend = BackendLocal
		}
	}
	var assets Store
	var err error
	switch backend {
	case BackendLocal:
		assets, err = NewFilesystem(opts.LocalPath)
	case BackendS3:
		if opts.S3 == nil {
			return Stores{}, "", fmt.Errorf("blob storage backend s3 is configured but no S3 client is available")
		}
		assets = NewS3(opts.S3)
	default:
		return Stores{}, "", fmt.Errorf("unknown blob storage backend %q", opts.Backend)
	}
	if err != nil {
		return Stores{}, "", err
	}
	// Availability is checked by readiness through Probe, allowing outage recovery.
	if opts.Settings != nil {
		recorded, _, recordErr := openRecorded(ctx, assets, opts.Settings)
		if recordErr != nil {
			return Stores{}, "", recordErr
		}
		assets = recorded
	}
	// Storage transitions pause writes before their final copy pass. Fence the
	// assets store before a local backend shares it as the operational store, so
	// subtitle, diagnostic, artifact, and avatar writes wait on the same fence.
	assets = WithMutationFence(assets)
	// A configured private bucket owns operational blobs whatever the backend
	// is. Avatars in particular have always lived there, so a catalog moving to
	// local artwork must not strand the profile-avatars keys already uploaded.
	// It stays unwrapped by the assets recorder. Bind its separate identity at
	// startup so buckets written by older releases are protected before another
	// private upload occurs.
	operational := assets
	if opts.S3Private != nil {
		operational = NewS3(opts.S3Private)
	} else if backend == BackendS3 {
		// The public bucket is never a substitute: it is world-readable in some
		// configurations, and these blobs are not.
		operational = nil
	}
	if opts.Settings != nil {
		if err := bindOperationalIdentity(ctx, opts.S3Private, opts.Settings); err != nil {
			return Stores{}, "", err
		}
	}
	return Stores{Assets: assets, Operational: operational}, backend, nil
}

// ErrLocationMoved reports recorded storage identities that no longer name the
// stores a running process opened: a managed transition committed elsewhere.
var ErrLocationMoved = errors.New("recorded storage location no longer matches this process")

// CheckRecordedLocation compares the recorded storage identities in a settings
// snapshot with the assets and private stores this process serves. An empty
// assets row means no artwork was written yet; the private row is bound at
// startup whenever a private bucket is configured, so it must match exactly.
func CheckRecordedLocation(recorded map[string]string, assetsIdentity, privateIdentity string) error {
	if assets := recorded[IdentitySettingKey]; assets != "" && assets != assetsIdentity && !legacyIdentityMatches(assets, assetsIdentity) {
		return fmt.Errorf("%w: artwork storage is recorded as %q", ErrLocationMoved, assets)
	}
	if private := recorded[OperationalIdentitySettingKey]; private != privateIdentity {
		return fmt.Errorf("%w: private storage is recorded as %q", ErrLocationMoved, private)
	}
	return nil
}

// bindOperationalIdentity protects a configured private bucket before serving
// requests. Older releases wrote private objects without recording this row,
// so waiting for the next write would leave existing data open to a direct
// settings change. A previously recorded identity must match at startup too.
func bindOperationalIdentity(ctx context.Context, client *s3client.Client, settings SettingsStore) error {
	identity := ""
	if client != nil {
		identity = NewS3(client).Identity()
	}
	active, err := settings.Get(ctx, OperationalIdentitySettingKey)
	if err != nil {
		return fmt.Errorf("read %s: %w", OperationalIdentitySettingKey, err)
	}
	if active != "" {
		if active != identity {
			return fmt.Errorf(
				"private storage identity mismatch: %s records %q but the configured location is %q; "+
					"stop all API writers and restore the effective private S3 endpoint, bucket, and key prefix in server_settings before restarting, then use a managed storage transition",
				OperationalIdentitySettingKey, active, identity,
			)
		}
		return nil
	}
	if identity == "" {
		return nil
	}
	inserted, err := settings.SetIfAbsent(ctx, OperationalIdentitySettingKey, identity)
	if err != nil {
		return fmt.Errorf("record private storage: %w", err)
	}
	if inserted {
		return nil
	}
	active, err = settings.Get(ctx, OperationalIdentitySettingKey)
	if err != nil {
		return fmt.Errorf("verify recorded private storage: %w", err)
	}
	if active != identity {
		return fmt.Errorf("private storage changed concurrently: recorded %q, configured %q", active, identity)
	}
	return nil
}

// openRecorded binds store to the identity recorded in settings: it refuses a
// store the catalog does not belong to and wraps the store so its first write
// records the identity. The middle return is the recorded identity after any
// legacy upgrade, for tests.
func openRecorded(ctx context.Context, store Store, settings SettingsStore) (Store, string, error) {
	// The catalog's keys belong to exactly one storage location. Opening a
	// different one, whether another backend or another bucket or root, would
	// serve a catalog whose objects live elsewhere.
	active, err := settings.Get(ctx, IdentitySettingKey)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", IdentitySettingKey, err)
	}
	if active != "" && active != store.Identity() {
		if !legacyIdentityMatches(active, store.Identity()) {
			return nil, "", fmt.Errorf("artwork storage is recorded as %q but configured as %q; use the managed storage transition in Admin settings", active, store.Identity())
		}
		// The row was translated from a release that lowercased the whole
		// endpoint. It names this store; rewrite it in the exact form so the
		// next start compares equal without this detour.
		if err := settings.Set(ctx, IdentitySettingKey, store.Identity()); err != nil {
			return nil, "", fmt.Errorf("upgrade %s: %w", IdentitySettingKey, err)
		}
		active = store.Identity()
	}
	wrapped := &recordingStore{Store: store, settings: settings}
	if direct, ok := store.(DirectURLer); ok {
		directStore := &recordingDirectStore{recordingStore: wrapped, DirectURLer: direct}
		if fencer, ok := store.(MutationFencer); ok {
			return &recordingFencedDirectStore{recordingDirectStore: directStore, fencer: fencer}, active, nil
		}
		return directStore, active, nil
	}
	if fencer, ok := store.(MutationFencer); ok {
		return &recordingFencedStore{recordingStore: wrapped, fencer: fencer}, active, nil
	}
	return wrapped, active, nil
}

// legacyIdentityMatches reports whether recorded is the pre-1.0 S3 fingerprint
// of current. Releases before the single identity row lowercased the entire
// endpoint, path included, and the migration carries that value over as
// "s3|<fingerprint>". Only the endpoint is compared that way: the bucket was
// always lowercased, and the key prefix always kept its case, so both must
// match exactly. A recorded lowercase endpoint path against a mixed-case
// configured one is ambiguous between "same store, older normalization" and
// "moved to a sibling tenant"; the upgrade path takes the first reading,
// because a move that differs only by path case while the bucket and prefix
// stay put is not a deployment anyone performs by accident.
func legacyIdentityMatches(recorded, current string) bool {
	recordedParts := strings.Split(recorded, "|")
	currentParts := strings.Split(current, "|")
	if len(recordedParts) != 4 || len(currentParts) != 4 || currentParts[0] != BackendS3 || recordedParts[0] != BackendS3 {
		return false
	}
	return recordedParts[1] == strings.ToLower(currentParts[1]) &&
		recordedParts[2] == currentParts[2] &&
		recordedParts[3] == currentParts[3]
}

type recordingStore struct {
	Store
	settings SettingsStore
	mu       sync.Mutex
	recorded bool
}

type recordingDirectStore struct {
	*recordingStore
	DirectURLer
}

type recordingFencedStore struct {
	*recordingStore
	fencer MutationFencer
}

func (s *recordingFencedStore) BeginMutationFence(ctx context.Context) (func(), error) {
	return s.fencer.BeginMutationFence(ctx)
}

type recordingFencedDirectStore struct {
	*recordingDirectStore
	fencer MutationFencer
}

func (s *recordingFencedDirectStore) BeginMutationFence(ctx context.Context) (func(), error) {
	return s.fencer.BeginMutationFence(ctx)
}

func (s *recordingDirectStore) ObjectAvailable(ctx context.Context, key string) (bool, error) {
	checker, ok := s.Store.(interface {
		ObjectAvailable(context.Context, string) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("artwork backend does not support external availability checks")
	}
	return checker.ObjectAvailable(ctx, key)
}

// Every write method the Store interface gains must be forwarded here. A write
// that reached the embedded store directly would publish an object without
// recording where it went, leaving the location editable and every key that
// references it orphaned by the next change. On a local backend the first write
// is often a diagnostic bundle or a job artifact, not artwork, so PutStream
// matters as much as Put.
func (s *recordingStore) Put(ctx context.Context, key string, data []byte) error {
	if err := s.Store.Put(ctx, key, data); err != nil {
		return err
	}
	return s.recordBackend(ctx)
}

func (s *recordingStore) PutStream(ctx context.Context, key string, r io.Reader, contentType string) error {
	if err := s.Store.PutStream(ctx, key, r, contentType); err != nil {
		return err
	}
	return s.recordBackend(ctx)
}

func (s *recordingStore) recordBackend(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorded {
		return nil
	}
	identity := s.Identity()
	inserted, err := s.settings.SetIfAbsent(ctx, IdentitySettingKey, identity)
	if err != nil {
		return fmt.Errorf("record artwork storage: %w", err)
	}
	if !inserted {
		active, err := s.settings.Get(ctx, IdentitySettingKey)
		if err != nil {
			return fmt.Errorf("verify recorded artwork storage: %w", err)
		}
		if active != identity {
			return fmt.Errorf("artwork storage changed concurrently: recorded %q, writing %q", active, identity)
		}
	}
	s.recorded = true
	return nil
}

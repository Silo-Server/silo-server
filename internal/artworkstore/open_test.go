package artworkstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSettings is an in-memory server_settings stand-in with the same
// set-if-absent semantics: only the first non-empty write to a key wins.
type fakeSettings struct {
	mu      sync.Mutex
	values  map[string]string
	getErr  error
	setErr  error
	setCall int
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{values: map[string]string{}}
}

func (s *fakeSettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.values[key], nil
}

func (s *fakeSettings) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCall++
	if s.setErr != nil {
		return false, s.setErr
	}
	if s.values[key] != "" {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

func (s *fakeSettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCall++
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func (s *fakeSettings) pin(t *testing.T) Pin {
	t.Helper()
	s.mu.Lock()
	raw := s.values[StorePinSettingKey]
	s.mu.Unlock()
	pin, err := decodePin(raw)
	if err != nil {
		t.Fatalf("decoding the recorded pin: %v", err)
	}
	return pin
}

func openLocal(t *testing.T, root string, settings SettingsStore) *Handle {
	t.Helper()
	handle, err := Open(context.Background(), Options{
		Backend:   BackendAuto,
		LocalPath: root,
		Settings:  settings,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle
}

func TestOpenRequiresSettings(t *testing.T) {
	if _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: t.TempDir()}); err == nil {
		t.Fatal("Open without a settings store succeeded")
	}
}

// auto selects the local filesystem when no bucket is configured. This is the
// whole point of the change: a deployment with no object storage must work
// without the operator choosing anything.
func TestOpenAutoSelectsLocalWithoutS3(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artwork")
	handle := openLocal(t, root, newFakeSettings())

	if handle.Backend != BackendLocal {
		t.Fatalf("Backend = %q, want %q", handle.Backend, BackendLocal)
	}
	if handle.Generation == "" {
		t.Fatal("no store generation for the filesystem backend")
	}
	if handle.LocalRoot() != root {
		t.Fatalf("LocalRoot = %q, want %q", handle.LocalRoot(), root)
	}
}

// An existing S3 installation keeps using its bucket under auto.
func TestOpenAutoSelectsS3WhenConfigured(t *testing.T) {
	handle, err := Open(context.Background(), Options{
		Backend:   BackendAuto,
		LocalPath: t.TempDir(),
		S3:        newFakeS3(),
		Settings:  newFakeSettings(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if handle.Backend != BackendS3 {
		t.Fatalf("Backend = %q, want %q", handle.Backend, BackendS3)
	}
	if handle.Generation == "" {
		t.Fatal("S3 store has no copy generation")
	}
	if handle.LocalRoot() != "" {
		t.Fatal("the S3 backend reported a local root")
	}
}

// An operator may keep a bucket for other public assets and still choose local
// artwork.
func TestOpenExplicitLocalIgnoresConfiguredS3(t *testing.T) {
	handle, err := Open(context.Background(), Options{
		Backend:   BackendLocal,
		LocalPath: t.TempDir(),
		S3:        newFakeS3(),
		Settings:  newFakeSettings(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if handle.Backend != BackendLocal {
		t.Fatalf("Backend = %q, want %q", handle.Backend, BackendLocal)
	}
}

func TestOpenExplicitS3WithoutBucketFails(t *testing.T) {
	_, err := Open(context.Background(), Options{
		Backend:   BackendS3,
		LocalPath: t.TempDir(),
		Settings:  newFakeSettings(),
	})
	if err == nil || !strings.Contains(err.Error(), "no public S3 bucket is configured") {
		t.Fatalf("Open = %v, want a configuration error", err)
	}
}

func TestOpenRejectsUnknownBackend(t *testing.T) {
	_, err := Open(context.Background(), Options{
		Backend:   "gcs",
		LocalPath: t.TempDir(),
		Settings:  newFakeSettings(),
	})
	if err == nil || !strings.Contains(err.Error(), "auto, local, s3") {
		t.Fatalf("Open = %v, want an unknown-backend error", err)
	}
}

// An unwritable canonical store is an operational outage: startup continues
// degraded, and the health wrapper rejects writes instead of choosing another
// configured backend.
func TestOpenReportsUnwritableLocalRootWithoutFallingBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	// A configured bucket is deliberately present: selecting local must fail
	// rather than quietly serving artwork from somewhere else.
	handle, err := Open(context.Background(), Options{
		Backend:   BackendLocal,
		LocalPath: filepath.Join(parent, "artwork"),
		S3:        newFakeS3(),
		Settings:  newFakeSettings(),
	})
	if err != nil {
		t.Fatalf("Open = %v, want an operational health state", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if state, _ := handle.Health(); state != HealthUnavailable {
		t.Fatalf("health = %q, want %q", state, HealthUnavailable)
	}
	if err := handle.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("WriteImmutable = %v, want ErrBackendUnavailable", err)
	}
}

func TestOpenSurfacesSettingsReadFailure(t *testing.T) {
	settings := newFakeSettings()
	settings.getErr = errors.New("settings unavailable")

	_, err := Open(context.Background(), Options{
		Backend:   BackendLocal,
		LocalPath: t.TempDir(),
		Settings:  settings,
	})
	if err == nil || !strings.Contains(err.Error(), "settings unavailable") {
		t.Fatalf("Open = %v, want the settings failure", err)
	}
}

func TestOpenRejectsACorruptPin(t *testing.T) {
	settings := newFakeSettings()
	settings.values[StorePinSettingKey] = `{"version":1,"backend":"tape"}`

	_, err := Open(context.Background(), Options{
		Backend:   BackendLocal,
		LocalPath: t.TempDir(),
		Settings:  settings,
	})
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("Open = %v, want a rejected pin", err)
	}
}

func TestOpenDoesNotPinBeforeMaterialization(t *testing.T) {
	settings := newFakeSettings()
	openLocal(t, t.TempDir(), settings)

	if pin := settings.pin(t); !pin.IsZero() {
		t.Fatalf("opening the store recorded pin %+v; auto must stay free until something is materialized", pin)
	}
}

func TestFirstWritePinsTheStore(t *testing.T) {
	settings := newFakeSettings()
	handle := openLocal(t, t.TempDir(), settings)
	ctx := context.Background()

	if err := handle.Store.WriteImmutable(ctx, testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	pin := settings.pin(t)
	if pin.Backend != BackendLocal || pin.Generation != handle.Generation {
		t.Fatalf("pin = %+v, want local/%s", pin, handle.Generation)
	}

	// Later writes must not keep hitting the settings store.
	before := settings.setCall
	if err := handle.Store.WriteImmutable(ctx, siblingKey, []byte("more"), ObjectMetadata{}); err != nil {
		t.Fatalf("second WriteImmutable: %v", err)
	}
	if settings.setCall != before {
		t.Fatalf("settings writes = %d, want no further pin attempts after %d", settings.setCall, before)
	}
}

func TestPinFailureFailsTheWrite(t *testing.T) {
	settings := newFakeSettings()
	handle := openLocal(t, t.TempDir(), settings)
	settings.setErr = errors.New("settings unavailable")

	err := handle.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{})
	if err == nil || !strings.Contains(err.Error(), "settings unavailable") {
		t.Fatalf("WriteImmutable = %v, want the pin failure", err)
	}
}

// The exact scenario the pin exists for: a local store that has been
// materialized into, and object storage configured months later for an
// unrelated feature. auto must not reinterpret live keys against the bucket —
// it keeps serving the pinned local store, without the fatal mismatch that
// used to take the whole server down over an unrelated bucket change.
func TestPinnedLocalStoreStaysLocalUnderAutoWithS3(t *testing.T) {
	settings := newFakeSettings()
	root := t.TempDir()
	handle := openLocal(t, root, settings)
	if err := handle.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	_ = handle.Close()

	reopened, err := Open(context.Background(), Options{
		Backend:   BackendAuto,
		LocalPath: root,
		S3:        newFakeS3(),
		Settings:  settings,
	})
	if err != nil {
		t.Fatalf("Open under auto with a pinned local store = %v, want success on the pinned backend", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.Backend != BackendLocal {
		t.Fatalf("Backend = %q, want the pinned %q", reopened.Backend, BackendLocal)
	}
	if _, err := reopened.Store.Stat(context.Background(), testKey); err != nil {
		t.Fatalf("Stat of the materialized key on the pinned store: %v", err)
	}
}

// An explicit conflicting backend remains fatal: it would split one catalog
// across divergent stores, and the settings API refuses to save it for the
// same reason.
func TestPinnedLocalStoreRefusesExplicitS3(t *testing.T) {
	settings := newFakeSettings()
	root := t.TempDir()
	handle := openLocal(t, root, settings)
	if err := handle.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	_ = handle.Close()

	_, err := Open(context.Background(), Options{
		Backend:   BackendS3,
		LocalPath: root,
		S3:        newFakeS3(),
		Settings:  settings,
	})
	var mismatch *PinMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Open = %v, want a PinMismatchError", err)
	}
	if !strings.Contains(err.Error(), "not supported in place") {
		t.Fatalf("error %q does not say that an in-place backend change is unsupported", err)
	}
	if !strings.Contains(err.Error(), "artwork.storage_backend=local") {
		t.Fatalf("error %q does not tell the operator how to keep the pinned backend", err)
	}
}

func TestPinnedLocalStoreDoesNotCreateMissingRootOnOpen(t *testing.T) {
	settings := newFakeSettings()
	handle := openLocal(t, t.TempDir(), settings)
	if err := handle.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	_ = handle.Close()

	missingRoot := filepath.Join(t.TempDir(), "missing-artwork")
	reopened, err := Open(context.Background(), Options{
		Backend:   BackendLocal,
		LocalPath: missingRoot,
		Settings:  settings,
	})
	if err != nil {
		t.Fatalf("Open = %v, want unavailable handle", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if state, _ := reopened.Health(); state != HealthUnavailable {
		t.Fatalf("health = %q, want %q", state, HealthUnavailable)
	}
	if reopened.Generation != handle.Generation {
		t.Fatalf("generation = %q, want pinned %q", reopened.Generation, handle.Generation)
	}
	if _, err := os.Stat(missingRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing pinned root was recreated: %v", err)
	}
}

func TestRunningPinnedLocalStoreRequiresExplicitRebuildAfterDeletedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artwork")
	settings := newFakeSettings()
	handle := openLocal(t, root, settings)
	if err := handle.Store.WriteImmutable(t.Context(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	oldGeneration := handle.Generation
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	handle.expireCheckCacheForTest()
	if err := handle.Check(t.Context()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Check after root deletion = %v, want unavailable", err)
	}
	if state, _ := handle.Health(); state != HealthUnavailable {
		t.Fatalf("health = %q, want unavailable", state)
	}
	if handle.Generation != oldGeneration {
		t.Fatalf("generation = %q, want pinned %q", handle.Generation, oldGeneration)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned root was recreated automatically: %v", err)
	}
	if err := handle.Store.WriteImmutable(t.Context(), testKey, []byte("blocked"), ObjectMetadata{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("write while unavailable = %v, want ErrBackendUnavailable", err)
	}
	intentRecorded := false
	if err := handle.RebuildEmpty(t.Context(), func(context.Context) error {
		intentRecorded = true
		// Intent must land before the durable rotation: the generation the
		// callback observes is still the pre-rebuild one.
		if got := handle.GenerationID(); got != oldGeneration {
			t.Fatalf("generation during recordIntent = %q, want pre-rebuild %q", got, oldGeneration)
		}
		return nil
	}); err != nil {
		t.Fatalf("RebuildEmpty: %v", err)
	}
	if !intentRecorded {
		t.Fatal("RebuildEmpty completed without recording the recovery intent")
	}
	if state, _ := handle.Health(); state != HealthEmptyRebuilding {
		t.Fatalf("health after rebuild = %q, want empty_rebuilding", state)
	}
	if handle.Generation == "" || handle.Generation == oldGeneration {
		t.Fatalf("generation after rebuild = %q, want rotation from %q", handle.Generation, oldGeneration)
	}
	if pin := settings.pin(t); pin.Generation != handle.Generation {
		t.Fatalf("pin after rebuild = %+v, want local/%s", pin, handle.Generation)
	}
	if err := handle.Store.WriteImmutable(t.Context(), testKey, []byte("rebuilt"), ObjectMetadata{}); err != nil {
		t.Fatalf("write into explicitly rebuilt root: %v", err)
	}
}

func TestRebuildEmptyRefusesRootWithArtworkObjects(t *testing.T) {
	root := t.TempDir()
	handle := openLocal(t, root, newFakeSettings())
	if err := handle.Store.WriteImmutable(t.Context(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	oldGeneration := handle.GenerationID()
	if err := handle.RebuildEmpty(t.Context(), func(context.Context) error {
		t.Fatal("recordIntent ran for a rejected rebuild; the durable intent would strand")
		return nil
	}); !errors.Is(err, ErrStoreNotEmpty) {
		t.Fatalf("RebuildEmpty = %v, want ErrStoreNotEmpty", err)
	}
	if state, _ := handle.Health(); state != HealthHealthy {
		t.Fatalf("health after refused rebuild = %q, want healthy", state)
	}
	if handle.GenerationID() != oldGeneration {
		t.Fatalf("generation after refused rebuild = %q, want %q", handle.GenerationID(), oldGeneration)
	}
}

func TestUnpinnedRunningLocalStoreWritesAfterGenerationRotation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artwork")
	settings := newFakeSettings()
	handle := openLocal(t, root, settings)
	oldGeneration := handle.GenerationID()
	if pin := settings.pin(t); !pin.IsZero() {
		t.Fatalf("pin before first write = %+v, want zero", pin)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	handle.expireCheckCacheForTest()
	if err := handle.Check(t.Context()); err != nil {
		t.Fatalf("Check after root deletion: %v", err)
	}
	newGeneration := handle.GenerationID()
	if newGeneration == "" || newGeneration == oldGeneration {
		t.Fatalf("generation = %q, want rotation from %q", newGeneration, oldGeneration)
	}
	if pin := settings.pin(t); pin.Generation != newGeneration {
		t.Fatalf("pin after recovery = %+v, want local/%s", pin, newGeneration)
	}
	if err := handle.Store.WriteImmutable(t.Context(), testKey, []byte("rebuilt"), ObjectMetadata{}); err != nil {
		t.Fatalf("first write after generation rotation: %v", err)
	}
}

func TestCleanTempFilesReachesFilesystemThroughHandleWrappers(t *testing.T) {
	root := t.TempDir()
	handle := openLocal(t, root, newFakeSettings())
	stale := filepath.Join(root, tempFilePrefix+"abandoned")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	cleaner, ok := handle.Store.(interface {
		CleanTempFiles(context.Context, time.Duration) (int, error)
	})
	if !ok {
		t.Fatal("wrapped filesystem store does not expose temp cleanup")
	}
	removed, err := cleaner.CleanTempFiles(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("CleanTempFiles through handle: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp file remains: %v", err)
	}
}

func TestPinnedLocalStoreReportsReachableDifferentCopyAsWrongMount(t *testing.T) {
	settings := newFakeSettings()
	first := openLocal(t, t.TempDir(), settings)
	if err := first.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	_ = first.Close()

	otherSettings := newFakeSettings()
	otherRoot := t.TempDir()
	other := openLocal(t, otherRoot, otherSettings)
	if err := other.Store.WriteImmutable(context.Background(), testKey, []byte("other"), ObjectMetadata{}); err != nil {
		t.Fatalf("other WriteImmutable: %v", err)
	}
	_ = other.Close()

	handle, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: otherRoot, Settings: settings})
	if err != nil {
		t.Fatalf("Open = %v, want wrong-mount health", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if state, _ := handle.Health(); state != HealthWrongMount {
		t.Fatalf("health = %q, want %q", state, HealthWrongMount)
	}
	if err := handle.Store.WriteImmutable(t.Context(), siblingKey, []byte("blocked"), ObjectMetadata{}); !errors.Is(err, ErrWrongMount) {
		t.Fatalf("write to wrong mount = %v, want ErrWrongMount", err)
	}
	if _, err := handle.Store.Stat(t.Context(), "artwork/v1/objects/poster/missing/original.webp"); !errors.Is(err, ErrWrongMount) {
		t.Fatalf("reconciler-style stat on wrong mount = %v, want ErrWrongMount instead of ErrNotFound", err)
	}
	if _, _, _, err := handle.Store.ListPage(t.Context(), "artwork/v1/", "", 10); !errors.Is(err, ErrWrongMount) {
		t.Fatalf("list on wrong mount = %v, want ErrWrongMount", err)
	}
}

func TestPinnedLocalStoreRefusesMissingRootWithoutWriting(t *testing.T) {
	settings := newFakeSettings()
	root := t.TempDir()
	first := openLocal(t, root, settings)
	if err := first.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	_ = first.Close()

	uncovered := filepath.Join(t.TempDir(), "missing-mount")
	handle, err := Open(context.Background(), Options{
		Backend: BackendLocal, LocalPath: uncovered, Settings: settings,
	})
	if err != nil {
		t.Fatalf("Open = %v, want unavailable health", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if state, _ := handle.Health(); state != HealthUnavailable {
		t.Fatalf("health = %q, want %q", state, HealthUnavailable)
	}
	if _, err := os.Stat(uncovered); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncovered mountpoint was created: %v", err)
	}
	if err := handle.Store.WriteImmutable(context.Background(), testKey, []byte("bad"), ObjectMetadata{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("WriteImmutable = %v, want ErrBackendUnavailable", err)
	}
	if _, err := handle.Store.Stat(context.Background(), testKey); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Stat = %v, want ErrBackendUnavailable", err)
	}
}

func TestRunningPinnedStoreRefusesToPopulateDroppedMount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artwork")
	settings := newFakeSettings()
	initial := openLocal(t, root, settings)
	if err := initial.Store.WriteImmutable(t.Context(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	handle, err := Open(t.Context(), Options{Backend: BackendLocal, LocalPath: root, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	handle.expireCheckCacheForTest()
	if err := handle.Check(t.Context()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Check = %v, want ErrBackendUnavailable", err)
	}
	if err := handle.Store.WriteImmutable(t.Context(), testKey, []byte("wrong disk"), ObjectMetadata{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("write = %v, want ErrBackendUnavailable", err)
	}
	if err := handle.Store.Probe(t.Context()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("wrapped probe = %v, want ErrBackendUnavailable", err)
	}
	if cleaner, ok := handle.Store.(interface {
		CleanTempFiles(context.Context, time.Duration) (int, error)
	}); !ok {
		t.Fatal("health store does not expose guarded temp cleanup")
	} else if _, err := cleaner.CleanTempFiles(t.Context(), time.Hour); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("temp cleanup = %v, want ErrBackendUnavailable", err)
	}
	if _, err := handle.local.Open(t.Context(), testKey); err == nil {
		t.Fatal("raw pinned store open unexpectedly succeeded")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned mountpoint was recreated: %v", err)
	}
}

func TestRunningPinnedStoreReopensRootAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "artwork")
	settings := newFakeSettings()
	initial := openLocal(t, root, settings)
	if err := initial.Store.WriteImmutable(t.Context(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	handle, err := Open(t.Context(), Options{Backend: BackendLocal, LocalPath: root, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	detached := filepath.Join(parent, "detached-artwork")
	if err := os.Rename(root, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}

	handle.expireCheckCacheForTest()
	if err := handle.Check(t.Context()); !errors.Is(err, ErrWrongMount) {
		t.Fatalf("Check after root replacement = %v, want ErrWrongMount", err)
	}
}

// Reopening the same store must succeed and must not re-pin.
func TestReopeningAPinnedStoreSucceeds(t *testing.T) {
	settings := newFakeSettings()
	root := t.TempDir()
	first := openLocal(t, root, settings)
	if err := first.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	generation := first.Generation
	_ = first.Close()

	second := openLocal(t, root, settings)
	if second.Generation != generation {
		t.Fatalf("generation changed across restarts: %q then %q", generation, second.Generation)
	}
	before := settings.setCall
	if err := second.Store.WriteImmutable(context.Background(), siblingKey, []byte("more"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable after restart: %v", err)
	}
	if settings.setCall != before {
		t.Fatalf("an already-pinned store attempted %d more pin writes", settings.setCall-before)
	}
}

// Two nodes racing on a fresh install: one wins the pin, and the loser must
// fail loudly instead of materializing into a second divergent store.
func TestConcurrentPinDisagreementFailsTheLoser(t *testing.T) {
	settings := newFakeSettings()
	local := openLocal(t, t.TempDir(), settings)

	remote, err := Open(context.Background(), Options{
		Backend:  BackendS3,
		S3:       newFakeS3(),
		Settings: settings,
	})
	if err != nil {
		t.Fatalf("Open s3: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })

	if err := local.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("local WriteImmutable: %v", err)
	}
	err = remote.Store.WriteImmutable(context.Background(), testKey, []byte("bytes"), ObjectMetadata{})
	var mismatch *PinMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("the losing node's write = %v, want a PinMismatchError", err)
	}
}

func TestCheckDetectsASwappedStore(t *testing.T) {
	settings := newFakeSettings()
	root := t.TempDir()
	handle := openLocal(t, root, settings)
	ctx := context.Background()

	if err := handle.Check(ctx); err != nil {
		t.Fatalf("Check on a healthy store: %v", err)
	}

	// Simulate the mount being replaced by a different store copy.
	if err := os.Remove(filepath.Join(root, markerFileName)); err != nil {
		t.Fatalf("removing the marker: %v", err)
	}
	if _, _, err := handle.Local().EnsureMarker(ctx); err != nil {
		t.Fatalf("re-creating the marker: %v", err)
	}
	// Within the cache window the previous healthy verdict is served; readiness
	// probing must not write to the store on every request.
	if err := handle.Check(ctx); err != nil {
		t.Fatalf("Check inside the cache window = %v, want the cached healthy verdict", err)
	}
	handle.expireCheckCacheForTest()
	var mismatch *PinMismatchError
	if err := handle.Check(ctx); !errors.As(err, &mismatch) {
		t.Fatalf("Check = %v, want a PinMismatchError", err)
	}
}

// A bucket outage is a readiness problem, not a configuration error: an
// existing S3 install must keep starting exactly as it does today and report
// the outage through /ready.
func TestOpenDoesNotFailOnAnUnreachableBucket(t *testing.T) {
	client := newFakeS3()
	client.headErr = errors.New("bucket unreachable")

	handle, err := Open(context.Background(), Options{Backend: BackendS3, S3: client, Settings: newFakeSettings()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := handle.Check(context.Background()); err == nil {
		t.Fatal("Check succeeded against an unreachable bucket")
	}
}

func TestCheckProbesTheBucket(t *testing.T) {
	client := newFakeS3()
	handle, err := Open(context.Background(), Options{Backend: BackendS3, S3: client, Settings: newFakeSettings()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if err := handle.Check(context.Background()); err != nil {
		t.Fatalf("Check on a healthy bucket: %v", err)
	}
	client.headErr = errors.New("bucket gone")
	handle.expireCheckCacheForTest()
	if err := handle.Check(context.Background()); err == nil {
		t.Fatal("Check succeeded against an unreachable bucket")
	}
}

func TestOpenUpgradesLegacyPinnedNonEmptyS3StoreMarkers(t *testing.T) {
	client := newFakeS3()
	client.objects[testKey] = []byte("existing artwork")
	settings := newFakeSettings()
	legacyPin, err := encodePin(Pin{Backend: BackendS3})
	if err != nil {
		t.Fatal(err)
	}
	settings.values[StorePinSettingKey] = legacyPin

	handle, err := Open(t.Context(), Options{Backend: BackendS3, S3: client, Settings: settings})
	if err != nil {
		t.Fatalf("Open legacy S3: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if handle.Generation == "" {
		t.Fatal("legacy S3 pin was not upgraded to a copy generation")
	}
	if got := settings.pin(t).Generation; got != handle.Generation {
		t.Fatalf("recorded generation = %q, want %q", got, handle.Generation)
	}
	if string(client.objects[formatMarkerFileName]) != formatMarkerContents {
		t.Fatal("legacy S3 upgrade did not create the format marker")
	}
	if len(client.objects[markerFileName]) == 0 {
		t.Fatal("legacy S3 upgrade did not create the copy marker")
	}
	if state, _ := handle.Health(); state != HealthHealthy {
		t.Fatalf("legacy S3 health = %q, want healthy", state)
	}
}

func TestOpenAdoptsZeroPinS3WithUnrelatedAndLegacyArtworkObjects(t *testing.T) {
	client := newFakeS3()
	client.objects["subtitles/movie/en.srt"] = []byte("subtitle")
	client.objects["tmdb/movie/1/poster/original.abc.webp"] = []byte("legacy artwork")
	settings := newFakeSettings()

	handle, err := Open(t.Context(), Options{Backend: BackendS3, S3: client, Settings: settings})
	if err != nil {
		t.Fatalf("Open upgrade store: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if handle.GenerationID() == "" || settings.pin(t).Generation != handle.GenerationID() {
		t.Fatalf("zero-pin store was not adopted: generation=%q pin=%+v", handle.GenerationID(), settings.pin(t))
	}
	if string(client.objects[formatMarkerFileName]) != formatMarkerContents || len(client.objects[markerFileName]) == 0 {
		t.Fatal("upgrade adoption did not create both sentinels")
	}
}

func TestNilHandleCheckFails(t *testing.T) {
	var handle *Handle
	if err := handle.Check(context.Background()); err == nil {
		t.Fatal("a nil handle reported ready")
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("closing a nil handle: %v", err)
	}
}

// TestResolveBackendHonorsPin pins auto's post-pin behavior: once a store is
// pinned, configuring a public bucket for other assets must not flip a
// pinned-local install to S3 (previously a fatal PinMismatchError at boot).
func TestResolveBackendHonorsPin(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		s3         bool
		pinned     string
		want       string
		wantErr    bool
	}{
		{name: "auto unpinned prefers s3", configured: BackendAuto, s3: true, want: BackendS3},
		{name: "auto unpinned falls back local", configured: BackendAuto, want: BackendLocal},
		{name: "auto honors local pin despite bucket", configured: BackendAuto, s3: true, pinned: BackendLocal, want: BackendLocal},
		{name: "auto honors s3 pin", configured: BackendAuto, s3: true, pinned: BackendS3, want: BackendS3},
		{name: "auto s3 pin without bucket errors", configured: BackendAuto, pinned: BackendS3, wantErr: true},
		{name: "explicit local ignores pin resolution", configured: BackendLocal, s3: true, pinned: BackendS3, want: BackendLocal},
	}
	for _, tc := range cases {
		got, err := resolveBackend(tc.configured, tc.s3, tc.pinned)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: resolveBackend = %q, want error", tc.name, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: resolveBackend = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

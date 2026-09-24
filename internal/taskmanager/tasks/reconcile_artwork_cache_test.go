package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeSettingsStore struct {
	values map[string]string
	getErr error
}

type transitionBeforeBaselineStore struct {
	*fakeSettingsStore
	committed bool
}

func (s *transitionBeforeBaselineStore) Get(ctx context.Context, key string) (string, error) {
	if key == ArtworkStorageIdentityKey && !s.committed {
		s.committed = true
		s.values[ArtworkStorageIdentityKey] = "new"
		s.values[config.StorageTransitionTargetKey] = `{"phase":"restart_pending","target_identity":"new"}`
	}
	return s.fakeSettingsStore.Get(ctx, key)
}

func (f *fakeSettingsStore) Get(_ context.Context, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.values[key], nil
}

func (f *fakeSettingsStore) Set(_ context.Context, key, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func (f *fakeSettingsStore) UpdateAtomic(_ context.Context, update func(map[string]string) (map[string]string, error)) error {
	writes, err := update(f.values)
	if err != nil {
		return err
	}
	for key, value := range writes {
		f.values[key] = value
	}
	return nil
}

type fakeReconcileRunner struct {
	stats metadata.ArtworkReconcileStats
	err   error
	runs  int
}

type blockingReconcileRunner struct {
	entered chan struct{}
	resume  chan struct{}
}

func (r *blockingReconcileRunner) Run(ctx context.Context, _ func(float64, string)) (metadata.ArtworkReconcileStats, error) {
	close(r.entered)
	select {
	case <-r.resume:
		return metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeVerify}, nil
	case <-ctx.Done():
		return metadata.ArtworkReconcileStats{}, ctx.Err()
	}
}

func (f *fakeReconcileRunner) Run(context.Context, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
	f.runs++
	return f.stats, f.err
}

type fakeResumableReconcileRunner struct {
	received         *metadata.ArtworkReconcileCheckpoint
	checkpointToSave *metadata.ArtworkReconcileCheckpoint
	stats            metadata.ArtworkReconcileStats
	err              error
	legacyRuns       int
	saveProvided     bool
}

func (f *fakeResumableReconcileRunner) Run(context.Context, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
	f.legacyRuns++
	return f.stats, f.err
}

func (f *fakeResumableReconcileRunner) RunResumable(
	_ context.Context,
	checkpoint *metadata.ArtworkReconcileCheckpoint,
	save func(metadata.ArtworkReconcileCheckpoint) error,
	_ func(float64, string),
) (metadata.ArtworkReconcileStats, error) {
	f.saveProvided = save != nil
	if checkpoint != nil {
		copied := *checkpoint
		copied.Totals = append([]int(nil), checkpoint.Totals...)
		copied.SurfaceCursor = append([]string(nil), checkpoint.SurfaceCursor...)
		f.received = &copied
	}
	if f.checkpointToSave != nil && save != nil {
		if err := save(*f.checkpointToSave); err != nil {
			return f.stats, err
		}
	}
	return f.stats, f.err
}

type fakeBrandingReconciler struct {
	checked int
	cleared int
	err     error
}

func (f *fakeBrandingReconciler) ReconcileMissingAssets(context.Context) (int, int, error) {
	return f.checked, f.cleared, f.err
}

type fakeProgress struct {
	lastMessage string
	resultData  json.RawMessage
}

func (f *fakeProgress) Report(_ float64, message string)   { f.lastMessage = message }
func (f *fakeProgress) SetResultData(data json.RawMessage) { f.resultData = data }

func TestReconcileArtworkCacheShouldRun(t *testing.T) {
	runner := &fakeReconcileRunner{}
	store := &fakeSettingsStore{values: map[string]string{}}
	task := NewReconcileArtworkCacheTask(runner, store, nil, "endpoint|bucket|prefix")

	// No stored fingerprint: first boot, seeding happens at wiring time; the
	// scheduled run must not sweep a catalog it has no baseline for.
	if run, err := task.ShouldRun(context.Background()); err != nil || run {
		t.Fatalf("ShouldRun with empty fingerprint = %v, %v; want false, nil", run, err)
	}

	store.values[ArtworkStorageIdentityKey] = "endpoint|bucket|prefix"
	if run, err := task.ShouldRun(context.Background()); err != nil || run {
		t.Fatalf("ShouldRun with matching fingerprint = %v, %v; want false, nil", run, err)
	}

	store.values[ArtworkStorageIdentityKey] = "old-endpoint|bucket|prefix"
	if run, err := task.ShouldRun(context.Background()); run || !errors.Is(err, ErrArtworkReconcileManualRunRequired) {
		t.Fatalf("ShouldRun with changed fingerprint = %v, %v; want false, manual-run-required", run, err)
	}
	if runner.runs != 0 {
		t.Fatalf("scheduled preflight ran reconciler %d times, want 0", runner.runs)
	}
}

func TestReconcileArtworkCacheRefusesManagedTransitionWithoutChangingCheckpoint(t *testing.T) {
	runner := &fakeReconcileRunner{}
	checkpoint := `{"baseline_identity":"old","target_identity":"new","checkpoint":{"done":25}}`
	store := &fakeSettingsStore{values: map[string]string{
		config.StorageTransitionTargetKey:    `{"phase":"restart_pending","public_reconcile":true}`,
		ArtworkStorageReconcileCheckpointKey: checkpoint,
	}}
	err := NewReconcileArtworkCacheTask(runner, store, nil, "new").Execute(t.Context(), &fakeProgress{})
	if !errors.Is(err, ErrArtworkReconcileManagedTransition) {
		t.Fatalf("Execute error = %v", err)
	}
	if runner.runs != 0 || store.values[ArtworkStorageReconcileCheckpointKey] != checkpoint {
		t.Fatalf("refused manual run mutated state: runs=%d checkpoint=%q", runner.runs, store.values[ArtworkStorageReconcileCheckpointKey])
	}
}

func TestReconcileArtworkCacheRefusesCommittedCopyBeforeSweep(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey:         "new",
		config.StorageTransitionTargetKey: `{"phase":"restart_pending","target_identity":"new"}`,
	}}
	runner := &fakeReconcileRunner{}
	err := NewReconcileArtworkCacheTask(runner, store, nil, "old").Execute(t.Context(), &fakeProgress{})
	if !errors.Is(err, ErrArtworkReconcileManagedTransition) {
		t.Fatalf("stale task after verified copy = %v, want managed transition error", err)
	}
	if runner.runs != 0 || store.values[ArtworkStorageIdentityKey] != "new" {
		t.Fatalf("stale task ran or changed committed identity: runs=%d identity=%q", runner.runs, store.values[ArtworkStorageIdentityKey])
	}
}

func TestReconcileArtworkCacheRefusesCommitBeforeBaselineRead(t *testing.T) {
	store := &transitionBeforeBaselineStore{fakeSettingsStore: &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey: "old",
	}}}
	runner := &fakeReconcileRunner{}
	err := NewReconcileArtworkCacheTask(runner, store, nil, "old").Execute(t.Context(), &fakeProgress{})
	if !errors.Is(err, ErrArtworkReconcileManagedTransition) {
		t.Fatalf("stale task after transition commit = %v, want managed transition error", err)
	}
	if runner.runs != 1 || store.values[ArtworkStorageIdentityKey] != "new" {
		t.Fatalf("stale task certification state: runs=%d identity=%q", runner.runs, store.values[ArtworkStorageIdentityKey])
	}
}

func TestReconcileArtworkCacheRunsWithoutManagedTransition(t *testing.T) {
	runner := &fakeReconcileRunner{}
	store := &fakeSettingsStore{values: map[string]string{ArtworkStorageIdentityKey: "current"}}
	if err := NewReconcileArtworkCacheTask(runner, store, nil, "current").Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if runner.runs != 1 {
		t.Fatalf("manual reconcile runs = %d, want 1", runner.runs)
	}
}

func TestReconcileArtworkCacheRejectsOldProcessAfterTransitionReceiptClears(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	oldIdentity, err := blobstore.LocalIdentity(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	newIdentity, err := blobstore.LocalIdentity(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey: newIdentity,
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      newRoot,
	}}
	runner := &fakeReconcileRunner{}
	err = NewReconcileArtworkCacheTask(runner, store, nil, oldIdentity).Execute(t.Context(), &fakeProgress{})
	if !errors.Is(err, ErrArtworkReconcileStaleStore) {
		t.Fatalf("manual reconcile on old process = %v, want stale store", err)
	}
	if runner.runs != 0 || store.values[ArtworkStorageIdentityKey] != newIdentity {
		t.Fatalf("old process ran or changed committed identity: runs=%d identity=%q", runner.runs, store.values[ArtworkStorageIdentityKey])
	}

	if err := NewReconcileArtworkCacheTask(runner, store, nil, newIdentity).Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatalf("manual reconcile on current process: %v", err)
	}
	if runner.runs != 1 {
		t.Fatalf("current process runs = %d, want 1", runner.runs)
	}
}

func TestReconcileArtworkCacheRejectsConfiguredMoveDuringSweep(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	oldIdentity, err := blobstore.LocalIdentity(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey: oldIdentity,
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      oldRoot,
	}}
	runner := &blockingReconcileRunner{entered: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- NewReconcileArtworkCacheTask(runner, store, nil, oldIdentity).Execute(t.Context(), &fakeProgress{})
	}()
	select {
	case <-runner.entered:
	case <-t.Context().Done():
		t.Fatal("manual reconcile did not start")
	}
	if err := store.UpdateAtomic(t.Context(), func(map[string]string) (map[string]string, error) {
		return map[string]string{"artwork.local_path": newRoot}, nil
	}); err != nil {
		t.Fatal(err)
	}
	close(runner.resume)
	select {
	case err := <-done:
		if !errors.Is(err, ErrArtworkReconcileStaleStore) {
			t.Fatalf("manual reconcile after configured move = %v, want stale store", err)
		}
	case <-t.Context().Done():
		t.Fatal("manual reconcile did not finish")
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != oldIdentity {
		t.Fatalf("storage identity = %q, want unchanged %q", got, oldIdentity)
	}
}

func TestConfiguredArtworkIdentityMatchesS3Location(t *testing.T) {
	identity, known, err := configuredArtworkIdentity(map[string]string{
		"artwork.storage_backend":   config.ArtworkBackendAuto,
		"s3.operational_endpoint":   "HTTPS://example.invalid/Tenant",
		"s3.operational_bucket":     "Artwork",
		"s3.operational_key_prefix": " /silo/dev/ ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "s3|https://example.invalid/Tenant|artwork|silo/dev"; !known || identity != want {
		t.Fatalf("configured S3 identity = %q, known=%t; want %q", identity, known, want)
	}
}

func TestReconcileArtworkCachePreservesTransitionCommitDuringSweep(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey:            "old",
		ArtworkStorageReconcileCheckpointKey: "manual-checkpoint",
	}}
	runner := &blockingReconcileRunner{entered: make(chan struct{}), resume: make(chan struct{})}
	task := NewReconcileArtworkCacheTask(runner, store, nil, "old")
	done := make(chan error, 1)
	go func() { done <- task.Execute(t.Context(), &fakeProgress{}) }()

	select {
	case <-runner.entered:
	case <-t.Context().Done():
		t.Fatal("manual reconcile did not start")
	}
	// A concurrent settings update changes the storage identity and
	// checkpoint while the manual sweep is running. The stale task must
	// preserve both when its sweep finishes.
	if err := store.UpdateAtomic(t.Context(), func(map[string]string) (map[string]string, error) {
		return map[string]string{
			ArtworkStorageIdentityKey:            "new",
			ArtworkStorageReconcileCheckpointKey: "transition-checkpoint",
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	close(runner.resume)
	select {
	case err := <-done:
		if !errors.Is(err, ErrArtworkReconcileIdentityChanged) {
			t.Fatalf("manual reconcile after transition commit = %v, want identity changed", err)
		}
	case <-t.Context().Done():
		t.Fatal("manual reconcile did not finish")
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "new" {
		t.Fatalf("storage identity = %q, want committed target", got)
	}
	if got := store.values[ArtworkStorageReconcileCheckpointKey]; got != "transition-checkpoint" {
		t.Fatalf("recovery checkpoint = %q, want committed transition checkpoint", got)
	}
}

func TestReconcileArtworkCacheRefusesHeldAdvisoryLockPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	held, acquired, err := pglock.TryAcquire(t.Context(), pool, pglock.ArtworkReconcileLockKey)
	if err != nil || !acquired {
		t.Fatalf("hold artwork reconcile lock: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = held.Release(context.Background()) })
	runner := &fakeReconcileRunner{}
	store := &fakeSettingsStore{values: map[string]string{ArtworkStorageIdentityKey: "current"}}
	err = NewReconcileArtworkCacheTask(runner, store, nil, "current", pool).Execute(t.Context(), &fakeProgress{})
	if !errors.Is(err, ErrArtworkReconcileManagedTransition) || runner.runs != 0 {
		t.Fatalf("Execute error=%v runs=%d", err, runner.runs)
	}
}

func TestReconcileArtworkCacheExecutePersistsFingerprintOnlyOnSuccess(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{ArtworkStorageIdentityKey: "old"}}
	failing := &fakeReconcileRunner{err: errors.New("storage unreachable")}
	task := NewReconcileArtworkCacheTask(failing, store, nil, "new")

	if err := task.Execute(context.Background(), &fakeProgress{}); err == nil {
		t.Fatal("Execute with failing runner returned nil error")
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "old" {
		t.Fatalf("fingerprint after failed run = %q, want unchanged %q", got, "old")
	}

	ok := &fakeReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: "verify", Verified: 3, Requeued: 2, Cleared: 1}}
	task = NewReconcileArtworkCacheTask(ok, store, nil, "new")
	progress := &fakeProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute = %v, want nil", err)
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "new" {
		t.Fatalf("fingerprint after successful run = %q, want %q", got, "new")
	}
	if progress.resultData == nil {
		t.Fatal("Execute did not record result data")
	}
}

func TestReconcileArtworkCacheExecuteResumesMatchingCheckpoint(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{ArtworkStorageIdentityKey: "old"}}
	checkpoint := metadata.ArtworkReconcileCheckpoint{
		Version:       1,
		Totals:        make([]int, 14),
		SurfaceIndex:  9,
		SurfaceCursor: []string{"128111764822294558"},
		SurfaceDone:   500,
		Done:          21500,
		Stats:         metadata.ArtworkReconcileStats{Mode: "verify", Verified: 500},
	}
	interrupted := &fakeResumableReconcileRunner{
		checkpointToSave: &checkpoint,
		stats:            checkpoint.Stats,
		err:              errors.New("interrupted"),
	}
	if err := NewReconcileArtworkCacheTask(interrupted, store, nil, "new").Execute(context.Background(), &fakeProgress{}); err == nil {
		t.Fatal("interrupted Execute returned nil error")
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "old" {
		t.Fatalf("fingerprint after interruption = %q, want old", got)
	}
	if strings.TrimSpace(store.values[ArtworkStorageReconcileCheckpointKey]) == "" {
		t.Fatal("interrupted Execute did not retain its checkpoint")
	}

	resumed := &fakeResumableReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: "verify", Verified: 1000}}
	resumeTask := NewReconcileArtworkCacheTask(resumed, store, nil, "new")
	rawCheckpoint := store.values[ArtworkStorageReconcileCheckpointKey]
	if run, err := resumeTask.ShouldRun(context.Background()); run || !errors.Is(err, ErrArtworkReconcileManualRunRequired) {
		t.Fatalf("scheduled resume preflight = %v, %v; want false, manual-run-required", run, err)
	}
	if got := store.values[ArtworkStorageReconcileCheckpointKey]; got != rawCheckpoint {
		t.Fatal("scheduled preflight changed the saved checkpoint")
	}
	if resumed.received != nil || resumed.legacyRuns != 0 {
		t.Fatal("scheduled preflight invoked the resumable runner")
	}
	if err := resumeTask.Execute(context.Background(), &fakeProgress{}); err != nil {
		t.Fatalf("resumed Execute: %v", err)
	}
	if resumed.received == nil || resumed.received.SurfaceIndex != checkpoint.SurfaceIndex ||
		len(resumed.received.SurfaceCursor) != 1 || resumed.received.SurfaceCursor[0] != checkpoint.SurfaceCursor[0] {
		t.Fatalf("resumed checkpoint = %#v, want %#v", resumed.received, checkpoint)
	}
	if resumed.legacyRuns != 0 {
		t.Fatalf("legacy Run called %d times, want 0", resumed.legacyRuns)
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "new" {
		t.Fatalf("fingerprint after resumed completion = %q, want new", got)
	}
	if got := store.values[ArtworkStorageReconcileCheckpointKey]; got != "" {
		t.Fatalf("checkpoint after completion = %q, want empty", got)
	}
}

func TestReconcileArtworkCacheCheckpointIsScopedToStorageMove(t *testing.T) {
	checkpoint := metadata.ArtworkReconcileCheckpoint{
		Version:      1,
		Totals:       make([]int, 14),
		SurfaceIndex: 3,
		Stats:        metadata.ArtworkReconcileStats{Mode: "verify"},
	}
	envelope, err := json.Marshal(artworkReconcileCheckpointEnvelope{
		BaselineIdentity: "older",
		TargetIdentity:   "different-target",
		Checkpoint:       checkpoint,
	})
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey:            "old",
		ArtworkStorageReconcileCheckpointKey: string(envelope),
	}}
	runner := &fakeResumableReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: "verify"}}
	if err := NewReconcileArtworkCacheTask(runner, store, nil, "new").Execute(context.Background(), &fakeProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.received != nil {
		t.Fatalf("received checkpoint from a different storage move: %#v", runner.received)
	}
}

func TestReconcileArtworkCacheIgnoresMalformedCheckpoint(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey:            "old",
		ArtworkStorageReconcileCheckpointKey: "{not-json",
	}}
	runner := &fakeResumableReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeVerify}}

	if err := NewReconcileArtworkCacheTask(runner, store, nil, "new").Execute(context.Background(), &fakeProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.received != nil {
		t.Fatalf("received malformed checkpoint: %#v", runner.received)
	}
	if !runner.saveProvided {
		t.Fatal("fresh storage-move sweep did not receive a checkpoint saver")
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "new" {
		t.Fatalf("fingerprint after completion = %q, want new", got)
	}
	if got := store.values[ArtworkStorageReconcileCheckpointKey]; got != "" {
		t.Fatalf("checkpoint after completion = %q, want empty", got)
	}
}

func TestReconcileArtworkCacheManualRunIgnoresSameIdentityCheckpoint(t *testing.T) {
	checkpoint := metadata.ArtworkReconcileCheckpoint{
		Version:      1,
		Totals:       make([]int, 14),
		SurfaceIndex: 3,
		Stats:        metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeVerify},
	}
	envelope, err := json.Marshal(artworkReconcileCheckpointEnvelope{
		BaselineIdentity: "current",
		TargetIdentity:   "current",
		Checkpoint:       checkpoint,
	})
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	store := &fakeSettingsStore{values: map[string]string{
		ArtworkStorageIdentityKey:            "current",
		ArtworkStorageReconcileCheckpointKey: string(envelope),
	}}
	runner := &fakeResumableReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: metadata.ArtworkReconcileModeVerify}}
	if err := NewReconcileArtworkCacheTask(runner, store, nil, "current").Execute(context.Background(), &fakeProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.received != nil {
		t.Fatalf("manual run resumed a stale same-identity checkpoint: %#v", runner.received)
	}
	if runner.saveProvided {
		t.Fatal("manual same-identity run received a checkpoint saver")
	}
}

func TestReconcileArtworkCacheExecuteDoesNotCertifyOnSweepErrors(t *testing.T) {
	// Rows skipped on storage errors were never verified, so the sweep did
	// not fully cover the catalog: the fingerprint must stay stale so the
	// next startup retries.
	store := &fakeSettingsStore{values: map[string]string{ArtworkStorageIdentityKey: "old"}}
	runner := &fakeReconcileRunner{stats: metadata.ArtworkReconcileStats{
		Mode: "verify", Verified: 10, Errors: 3, SweepErrors: 3,
	}}
	branding := &fakeBrandingReconciler{checked: 4}
	task := NewReconcileArtworkCacheTask(runner, store, branding, "new")
	if err := task.Execute(context.Background(), &fakeProgress{}); err == nil {
		t.Fatal("Execute with sweep errors returned nil error")
	}
	if got := store.values[ArtworkStorageIdentityKey]; got != "old" {
		t.Fatalf("fingerprint after sweep errors = %q, want unchanged %q", got, "old")
	}
}

func TestReconcileArtworkCacheExecuteIncludesBranding(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{}}
	runner := &fakeReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: "verify", Cleared: 1}}
	task := NewReconcileArtworkCacheTask(runner, store, &fakeBrandingReconciler{checked: 4, cleared: 2}, "id")
	progress := &fakeProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute = %v, want nil", err)
	}
	var stats metadata.ArtworkReconcileStats
	if err := json.Unmarshal(progress.resultData, &stats); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if stats.Cleared != 3 {
		t.Fatalf("Cleared = %d, want 3 (1 artwork + 2 branding)", stats.Cleared)
	}
	if stats.Checked != 4 {
		t.Fatalf("Checked = %d, want 4 (all probed branding assets, not just cleared ones)", stats.Checked)
	}

	// A branding failure must NOT discard the completed sweep: the
	// fingerprint is certified first and the failure is reported in the
	// message instead, so the full catalog sweep never repeats over a
	// transient error on a 4-object branding check.
	fpStore := &fakeSettingsStore{values: map[string]string{}}
	failing := NewReconcileArtworkCacheTask(runner, fpStore,
		&fakeBrandingReconciler{err: errors.New("storage unreachable")}, "id")
	failingProgress := &fakeProgress{}
	if err := failing.Execute(context.Background(), failingProgress); err != nil {
		t.Fatalf("Execute with failing branding reconcile = %v, want nil (non-fatal)", err)
	}
	if got := fpStore.values[ArtworkStorageIdentityKey]; got != "id" {
		t.Fatalf("fingerprint after branding failure = %q, want certified %q", got, "id")
	}
	if !strings.Contains(failingProgress.lastMessage, "branding asset check failed") {
		t.Fatalf("completion message %q does not surface the branding failure", failingProgress.lastMessage)
	}
}

func TestReconcileArtworkCacheIsManualOnly(t *testing.T) {
	task := NewReconcileArtworkCacheTask(&fakeReconcileRunner{}, &fakeSettingsStore{values: map[string]string{}}, nil, "endpoint|bucket|prefix")
	if !task.ManualOnly() || len(task.DefaultTriggers()) != 0 {
		t.Fatalf("ManualOnly() = %v, DefaultTriggers() = %#v; want manual-only with no schedule", task.ManualOnly(), task.DefaultTriggers())
	}

	moved := NewReconcileArtworkCacheTask(&fakeReconcileRunner{}, &fakeSettingsStore{values: map[string]string{ArtworkStorageIdentityKey: "old|bucket|prefix"}}, nil, "endpoint|bucket|prefix")
	if err := moved.CheckStorageIdentity(context.Background()); !errors.Is(err, ErrArtworkReconcileManualRunRequired) {
		t.Fatalf("CheckStorageIdentity() after a storage move = %v, want manual-run-required", err)
	}
}

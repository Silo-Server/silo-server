package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeSettingsStore struct {
	values map[string]string
	getErr error
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

type fakeReconcileRunner struct {
	stats metadata.ArtworkReconcileStats
	err   error
	runs  int
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

func TestArtworkStorageIdentityNormalizes(t *testing.T) {
	// Endpoint and bucket are case-insensitive; whitespace is trimmed.
	a := ArtworkStorageIdentity(" https://S3.Example.com ", "Assets", "silo/prod")
	b := ArtworkStorageIdentity("https://s3.example.com", "assets", "silo/prod")
	if a != b {
		t.Fatalf("identity not normalized: %q != %q", a, b)
	}
	// The key prefix is slash-insensitive (the s3client trims slashes, so
	// 'art' and '/art/' are the same storage location)...
	if ArtworkStorageIdentity("e", "b", "art") != ArtworkStorageIdentity("e", "b", " /art/ ") {
		t.Fatal("slash-only prefix differences must not change the identity")
	}
	// ...but case-SENSITIVE: S3 object keys are case-sensitive, so a
	// case-only prefix edit is a real storage move and must reconcile.
	if ArtworkStorageIdentity("e", "b", "Art") == ArtworkStorageIdentity("e", "b", "art") {
		t.Fatal("case-only prefix differences are real storage moves and must change the identity")
	}
	if a == ArtworkStorageIdentity("https://s3.example.com", "assets", "") {
		t.Fatal("key prefix must participate in the identity")
	}
	if a == ArtworkStorageIdentity("https://other.example.com", "assets", "silo/prod") {
		t.Fatal("endpoint must participate in the identity")
	}
}

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

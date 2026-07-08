package tasks

import (
	"context"
	"encoding/json"
	"errors"
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

type fakeBrandingReconciler struct {
	cleared int
	err     error
}

func (f *fakeBrandingReconciler) ReconcileMissingAssets(context.Context) (int, error) {
	return f.cleared, f.err
}

type fakeProgress struct {
	lastMessage string
	resultData  json.RawMessage
}

func (f *fakeProgress) Report(_ float64, message string)   { f.lastMessage = message }
func (f *fakeProgress) SetResultData(data json.RawMessage) { f.resultData = data }

func TestArtworkStorageIdentityNormalizes(t *testing.T) {
	a := ArtworkStorageIdentity(" https://S3.Example.com ", "Assets", " silo/Prod ")
	b := ArtworkStorageIdentity("https://s3.example.com", "assets", "silo/prod")
	if a != b {
		t.Fatalf("identity not normalized: %q != %q", a, b)
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
	if run, err := task.ShouldRun(context.Background()); err != nil || !run {
		t.Fatalf("ShouldRun with changed fingerprint = %v, %v; want true, nil", run, err)
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

func TestReconcileArtworkCacheExecuteIncludesBranding(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{}}
	runner := &fakeReconcileRunner{stats: metadata.ArtworkReconcileStats{Mode: "verify", Cleared: 1}}
	task := NewReconcileArtworkCacheTask(runner, store, &fakeBrandingReconciler{cleared: 2}, "id")
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

	// A branding failure must abort before the fingerprint is certified.
	failing := NewReconcileArtworkCacheTask(runner, &fakeSettingsStore{values: map[string]string{}},
		&fakeBrandingReconciler{err: errors.New("storage unreachable")}, "id")
	fpStore := failing.settings.(*fakeSettingsStore)
	if err := failing.Execute(context.Background(), &fakeProgress{}); err == nil {
		t.Fatal("Execute with failing branding reconcile returned nil error")
	}
	if got := fpStore.values[ArtworkStorageIdentityKey]; got != "" {
		t.Fatalf("fingerprint after failed branding reconcile = %q, want empty", got)
	}
}

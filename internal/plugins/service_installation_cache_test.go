package plugins

import (
	"context"
	"errors"
	"testing"
)

// countingInstallationStore wraps the shared fake store and counts GetByID
// calls so tests can assert the in-memory installation cache absorbs repeat
// reads.
type countingInstallationStore struct {
	*fakeServiceInstallationStore
	getByIDCalls int
	// onGetByID, when set, runs after the call is counted but before the row is
	// returned, so tests can simulate an invalidation racing an in-flight read.
	onGetByID func()
}

func (s *countingInstallationStore) GetByID(ctx context.Context, id int) (*Installation, error) {
	s.getByIDCalls++
	if s.onGetByID != nil {
		s.onGetByID()
	}
	return s.fakeServiceInstallationStore.GetByID(ctx, id)
}

// newCachedInstallationService builds a Service backed by the counting store and
// wires the installation-cache invalidation exactly as NewService does, so the
// OnLifecycleChange -> invalidate path is exercised.
func newCachedInstallationService(installations ...*Installation) (*Service, *countingInstallationStore) {
	store := &countingInstallationStore{
		fakeServiceInstallationStore: newFakeServiceInstallationStore(installations...),
	}
	svc := &Service{installations: store}
	svc.AddLifecycleHook(func(context.Context) { svc.invalidateInstallationCache() })
	return svc, store
}

func TestLoadInstallationCachesAndInvalidatesOnLifecycleChange(t *testing.T) {
	ctx := context.Background()
	svc, store := newCachedInstallationService(&Installation{ID: 7, PluginID: "silo.metadb", Enabled: true})

	// First read hits the store.
	if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
		t.Fatalf("first loadInstallation err = %v", err)
	}
	if store.getByIDCalls != 1 {
		t.Fatalf("after first read GetByID calls = %d, want 1", store.getByIDCalls)
	}

	// Subsequent reads are served from the cache.
	for i := 0; i < 5; i++ {
		if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
			t.Fatalf("cached loadInstallation err = %v", err)
		}
	}
	if store.getByIDCalls != 1 {
		t.Fatalf("after cached reads GetByID calls = %d, want still 1", store.getByIDCalls)
	}

	// A lifecycle change wipes the cache and forces a re-read.
	svc.OnLifecycleChange(ctx)
	if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
		t.Fatalf("post-invalidate loadInstallation err = %v", err)
	}
	if store.getByIDCalls != 2 {
		t.Fatalf("after lifecycle change GetByID calls = %d, want 2", store.getByIDCalls)
	}
}

func TestRefreshMarkerRuntimeInvalidatesReplicaState(t *testing.T) {
	svc, store := newCachedInstallationService(&Installation{ID: 7, PluginID: "silo.theintrodb", Version: "1.0.0", Enabled: true})
	host := &fakeServiceHost{}
	svc.host = host
	if _, err := svc.loadInstallation(t.Context(), 7, true); err != nil {
		t.Fatal(err)
	}
	version := "1.1.0"
	if err := store.Update(t.Context(), 7, UpdateInstallationInput{Version: &version}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RefreshMarkerRuntime(7); err != nil {
		t.Fatal(err)
	}
	current, err := svc.loadInstallation(t.Context(), 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != version || store.getByIDCalls != 2 {
		t.Fatalf("replica retained cached installation: version=%s reads=%d", current.Version, store.getByIDCalls)
	}
	if len(host.stopped) != 1 || host.stopped[0] != 7 {
		t.Fatalf("runtime was not discarded: stops=%v", host.stopped)
	}
}

func TestRefreshMarkerRuntimeKeepsResidentSupervised(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	f.service.StartResidents(t.Context())
	waitState(t, f.service, 5, "initial resident launch", running)
	before, err := f.host.Client(5)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.service.RefreshMarkerRuntime(5); err != nil {
		t.Fatal(err)
	}
	waitState(t, f.service, 5, "resident launch after marker refresh", running)
	after, err := f.host.Client(5)
	if err != nil {
		t.Fatalf("supervisor reports running without a resident process: %v", err)
	}
	if after == before {
		t.Fatal("marker refresh retained the previous resident process")
	}
}

func TestIsInstallationEnabledReflectsCacheAndInvalidation(t *testing.T) {
	ctx := context.Background()
	svc, store := newCachedInstallationService(&Installation{ID: 7, PluginID: "silo.metadb", Enabled: true})

	enabled, err := svc.IsInstallationEnabled(ctx, 7)
	if err != nil {
		t.Fatalf("IsInstallationEnabled err = %v", err)
	}
	if !enabled {
		t.Fatal("IsInstallationEnabled = false, want true")
	}
	if store.getByIDCalls != 1 {
		t.Fatalf("GetByID calls = %d, want 1", store.getByIDCalls)
	}

	// Disable the underlying row. Until a lifecycle change, the cache still
	// reports the stale (enabled) value and issues no further reads.
	falseVal := false
	if err := store.Update(ctx, 7, UpdateInstallationInput{Enabled: &falseVal}); err != nil {
		t.Fatalf("store.Update err = %v", err)
	}
	enabled, err = svc.IsInstallationEnabled(ctx, 7)
	if err != nil {
		t.Fatalf("cached IsInstallationEnabled err = %v", err)
	}
	if !enabled {
		t.Fatal("cached IsInstallationEnabled = false, want stale true before invalidation")
	}
	if store.getByIDCalls != 1 {
		t.Fatalf("GetByID calls = %d, want still 1 (served from cache)", store.getByIDCalls)
	}

	// After a lifecycle change the cache is wiped and the new value is read.
	svc.OnLifecycleChange(ctx)
	enabled, err = svc.IsInstallationEnabled(ctx, 7)
	if err != nil {
		t.Fatalf("post-invalidate IsInstallationEnabled err = %v", err)
	}
	if enabled {
		t.Fatal("post-invalidate IsInstallationEnabled = true, want false")
	}
	if store.getByIDCalls != 2 {
		t.Fatalf("GetByID calls = %d, want 2 after invalidation", store.getByIDCalls)
	}
}

// TestCachedInstallationSkipsWriteOnRacingInvalidation proves the generation
// guard: if a lifecycle invalidation lands while GetByID is in flight, the
// fetched (potentially pre-mutation) row is returned to the caller but is not
// written back into the freshly-cleared cache, so the next read re-fetches
// instead of serving a resurrected stale row.
func TestCachedInstallationSkipsWriteOnRacingInvalidation(t *testing.T) {
	ctx := context.Background()
	svc, store := newCachedInstallationService(&Installation{ID: 7, PluginID: "silo.metadb", Enabled: true})

	// Simulate the race by invalidating the cache from inside the store read,
	// i.e. between the generation capture and the write-back.
	store.onGetByID = func() { svc.invalidateInstallationCache() }

	if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
		t.Fatalf("racing loadInstallation err = %v", err)
	}
	if store.getByIDCalls != 1 {
		t.Fatalf("GetByID calls = %d, want 1", store.getByIDCalls)
	}

	// The racing read must not have populated the cache; the next read re-fetches.
	store.onGetByID = nil
	if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
		t.Fatalf("post-race loadInstallation err = %v", err)
	}
	if store.getByIDCalls != 2 {
		t.Fatalf("GetByID calls = %d, want 2 (racing write skipped, cache empty)", store.getByIDCalls)
	}

	// A subsequent read with no interference is served from the cache.
	if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
		t.Fatalf("cached loadInstallation err = %v", err)
	}
	if store.getByIDCalls != 2 {
		t.Fatalf("GetByID calls = %d, want still 2 (served from cache)", store.getByIDCalls)
	}
}

func TestLoadInstallationRequireEnabledGateAppliesAfterCache(t *testing.T) {
	ctx := context.Background()
	svc, store := newCachedInstallationService(&Installation{ID: 7, PluginID: "silo.metadb", Enabled: false})

	// requireEnabled=false caches the disabled row.
	if _, err := svc.loadInstallation(ctx, 7, false); err != nil {
		t.Fatalf("loadInstallation err = %v", err)
	}
	// requireEnabled=true returns ErrInstallationDisabled without a second read,
	// proving the gate is applied after the cache lookup.
	if _, err := svc.loadInstallation(ctx, 7, true); !errors.Is(err, ErrInstallationDisabled) {
		t.Fatalf("loadInstallation requireEnabled err = %v, want ErrInstallationDisabled", err)
	}
	if store.getByIDCalls != 1 {
		t.Fatalf("GetByID calls = %d, want 1 (gate applied after cache)", store.getByIDCalls)
	}
}

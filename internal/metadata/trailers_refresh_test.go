package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// waitForProcess blocks until the detached on-demand refresh has called
// Process, so a queued test does not leak a goroutine into the next one.
func waitForProcess(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the on-demand refresh to start")
	}
}

// waitForOnDemandIdle blocks until no on-demand refresh holds an in-process
// claim. The claim is released in the detached goroutine's defer, slightly
// after Process returns, and a still-held claim silently drops the next
// refresh — so a test that queues twice has to wait for it.
func waitForOnDemandIdle(t *testing.T, s *MetadataService) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.onDemandRefresh.mu.Lock()
		running := len(s.onDemandRefresh.running)
		s.onDemandRefresh.mu.Unlock()
		if running == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for on-demand refresh claims to clear")
}

// RequestTrailersRefresh reaches the cooldown gate through a runtime type
// assertion on itemRepo, so a drift in the repository's signature would turn
// every request into an error instead of failing the build.
func TestItemRepositorySatisfiesTrailerRefreshGate(t *testing.T) {
	var repo any = (*catalog.ItemRepository)(nil)
	if _, ok := repo.(metadataTrailerRefreshRepo); !ok {
		t.Fatal("*catalog.ItemRepository must satisfy metadataTrailerRefreshRepo")
	}
}

func TestRequestTrailersRefreshQueuesThenReportsCooldown(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("first status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	if outcome.NextAllowedAt != nil {
		t.Fatalf("queued outcome must not carry next_allowed_at, got %v", outcome.NextAllowedAt)
	}
	waitForProcess(t, started)

	// The second request inside the window loses the gate and reports when the
	// next one may win: the stored timestamp plus the cooldown.
	outcome, err = h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh second: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusCooldown {
		t.Fatalf("second status = %q, want %q", outcome.Status, TrailerRefreshStatusCooldown)
	}
	if outcome.NextAllowedAt == nil {
		t.Fatal("cooldown outcome must carry next_allowed_at")
	}
	want := now.Add(TrailerRefreshCooldown)
	if !outcome.NextAllowedAt.Equal(want) {
		t.Fatalf("next_allowed_at = %s, want %s", outcome.NextAllowedAt, want)
	}
	if got := h.itemRepo.trailersClaimCount(); got != 1 {
		t.Fatalf("cooldown slot consumed %d times, want 1", got)
	}
}

func TestRequestTrailersRefreshAllowsRetryAfterCooldownLapses(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	processed := make(chan string, 4)
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		processed <- req.ContentID
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("first request = %+v, err = %v", outcome, err)
	}
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first on-demand refresh")
	}
	waitForOnDemandIdle(t, h.service)

	// One second past the window the gate opens again.
	now = now.Add(TrailerRefreshCooldown + time.Second)
	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh after cooldown: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status after cooldown lapsed = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the second on-demand refresh")
	}
	if got := h.itemRepo.trailersClaimCount(); got != 2 {
		t.Fatalf("cooldown slot consumed %d times, want 2", got)
	}
}

// A library whose trailer_kinds allow-list is empty has remote videos turned
// off, so the request is answered "disabled" — and must not burn the item's
// weekly slot, or a user would be locked out for a week over a no-op.
func TestRequestTrailersRefreshDisabledDoesNotConsumeCooldownSlot(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	if err := h.libraryRepo.Upsert(ctx, "movie-1", 10, time.Now()); err != nil {
		t.Fatalf("seed library membership: %v", err)
	}
	folder := &models.MediaFolder{ID: 10, Type: "movies", Enabled: true, TrailerKinds: nil}
	h.service.folderRepo = &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{10: folder}}

	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		t.Errorf("disabled request must not start a refresh (content_id %s)", req.ContentID)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusDisabled {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusDisabled)
	}
	if got := h.itemRepo.trailersClaimCount(); got != 0 {
		t.Fatalf("disabled request consumed the cooldown slot %d times, want 0", got)
	}

	// Re-enabling the library lets the very next request through, proving the
	// slot really was untouched.
	folder.TrailerKinds = []string{string(models.ExtraKindTrailer)}
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	outcome, err = h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh after re-enabling: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status after re-enabling = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	waitForProcess(t, started)
}

// A nil allow-list is "allow all" — an unknown scope or a transient library
// lookup failure. It must not be mistaken for "disabled".
func TestRequestTrailersRefreshTreatsUnknownLibraryScopeAsAllowed(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	// The item has no library membership, so resolveAllowedVideoKinds returns
	// nil rather than an empty map.
	h.service.folderRepo = &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{}}

	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	waitForProcess(t, started)
}

func TestRequestTrailersRefreshPropagatesGateErrors(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	gateErr := errors.New("database is down")
	h.itemRepo.trailersClaimErr = gateErr

	if _, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); !errors.Is(err, gateErr) {
		t.Fatalf("err = %v, want %v", err, gateErr)
	}
}

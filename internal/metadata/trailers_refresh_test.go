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
	waitForOnDemandIdle(t, h.service)

	// The second request inside the window loses the gate and reports when the
	// next one may win: the stored timestamp plus the cooldown.
	outcome, err = h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh second: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusCooldown {
		t.Fatalf("second status = %q, want %q", outcome.Status, TrailerRefreshStatusCooldown)
	}
	if got := h.itemRepo.trailersReleaseCount(); got != 0 {
		t.Fatalf("a successful refresh must keep the slot, released %d times", got)
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
	// The failed gate call must not strand the in-process claim, or every
	// later request for this item would be silently deduped away.
	waitForOnDemandIdle(t, h.service)
}

// The weekly slot pays for work actually done. When the refresh it started
// fails — a provider outage, a timeout — the slot goes back so the viewer can
// retry now instead of waiting out a window in which nothing was fetched.
func TestRequestTrailersRefreshReleasesSlotWhenRefreshFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return nil, errors.New("tmdb is unreachable")
	}

	released := h.itemRepo.expectTrailersRelease()
	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the failed refresh to release the cooldown slot")
	}
	if stored := h.itemRepo.trailersStoredAt("movie-1"); stored != nil {
		t.Fatalf("failed refresh left the slot consumed until %s", stored)
	}
	waitForOnDemandIdle(t, h.service)

	// The very next request wins the gate again, with no clock movement.
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	outcome, err = h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("retry after a failed refresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("retry status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	waitForProcess(t, started)
}

// A refresh that succeeds but turns up nothing keeps the slot: "no trailers
// exist for this title" is an answer, and re-asking providers weekly is the
// accepted cost ceiling.
func TestRequestTrailersRefreshKeepsSlotWhenRefreshFindsNothing(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// A successful refresh that produced no videos is indistinguishable here
	// from any other success: Process returns Updated, and no video rows were
	// written.
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)

	if got := h.itemRepo.trailersReleaseCount(); got != 0 {
		t.Fatalf("successful refresh released the slot %d times, want 0", got)
	}
	if stored := h.itemRepo.trailersStoredAt("movie-1"); stored == nil {
		t.Fatal("successful refresh must keep the slot consumed")
	}
	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusCooldown {
		t.Fatalf("status after a successful empty refresh = %q, want %q",
			outcome.Status, TrailerRefreshStatusCooldown)
	}
}

// The release is guarded on the timestamp the failing request wrote, so a
// release that lands after the window lapsed and a newer request claimed the
// slot must leave that newer claim alone — otherwise the late write would hand
// out a free extra refresh.
//
// The newer claim is taken against the gate directly rather than through
// RequestTrailersRefresh: the point under test is the timestamp guard, and
// driving it through the public API would only exercise the in-process dedup
// that TestRequestTrailersRefreshInFlightRefreshQueuesWithoutConsumingSlot
// already covers.
func TestRequestTrailersRefreshReleaseDoesNotClobberNewerClaim(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// Hold the failing request's release until a newer claim is in place.
	gate := make(chan struct{})
	h.itemRepo.trailersReleaseGate = gate

	failed := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		close(failed)
		return nil, errors.New("tmdb is unreachable")
	}

	released := h.itemRepo.expectTrailersRelease()
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("first request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, failed)

	// The window lapses and a later request wins the gate afresh.
	now = now.Add(TrailerRefreshCooldown + time.Second)
	newClaimAt := now
	claimed, claimedAt, err := h.itemRepo.TryClaimTrailersRefresh(ctx, "movie-1", TrailerRefreshCooldown)
	if err != nil || !claimed || claimedAt == nil {
		t.Fatalf("newer claim = %v, at = %v, err = %v", claimed, claimedAt, err)
	}

	// Only now does the first request's release land.
	close(gate)
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the late release")
	}
	waitForOnDemandIdle(t, h.service)

	stored := h.itemRepo.trailersStoredAt("movie-1")
	if stored == nil {
		t.Fatal("the late release cleared a slot claimed by a newer request")
	}
	if !stored.Equal(newClaimAt) {
		t.Fatalf("stored timestamp = %s, want the newer claim %s", stored, newClaimAt)
	}
}

// The in-process claim is shared with the detail view's stale-metadata nudge.
// A trailer request that arrives while an equivalent refresh is already running
// is answered "queued" — one really is running — without consuming the weekly
// slot, so a failure of that refresh still leaves the viewer able to retry.
func TestRequestTrailersRefreshInFlightRefreshQueuesWithoutConsumingSlot(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// Hold an in-flight refresh open for the duration of the request under
	// test, exactly as the item-detail path's nudge would.
	inFlight := make(chan struct{})
	entered := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(entered)
		<-inFlight
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	h.service.startOnDemandMetadataRefresh(RefreshTargetItem, "movie-1")
	waitForProcess(t, entered)

	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	if got := h.itemRepo.trailersClaimCount(); got != 0 {
		t.Fatalf("in-flight refresh consumed the cooldown slot %d times, want 0", got)
	}

	close(inFlight)
	waitForOnDemandIdle(t, h.service)

	// The slot was untouched, so the next request still wins the gate.
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	outcome, err = h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("request after the in-flight refresh finished: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	if got := h.itemRepo.trailersClaimCount(); got != 1 {
		t.Fatalf("cooldown slot consumed %d times, want 1", got)
	}
	waitForProcess(t, started)
}

// The disabled short-circuit returns before the in-process claim is taken, so
// it must not leave one behind either.
func TestRequestTrailersRefreshDisabledLeavesNoInProcessClaim(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	if err := h.libraryRepo.Upsert(ctx, "movie-1", 10, time.Now()); err != nil {
		t.Fatalf("seed library membership: %v", err)
	}
	h.service.folderRepo = &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{
		10: {ID: 10, Type: "movies", Enabled: true, TrailerKinds: nil},
	}}

	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusDisabled {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}
	waitForOnDemandIdle(t, h.service)
}

// A cooldown answer takes and then hands back the in-process claim; leaking it
// would mute every subsequent refresh for the item until the process restarts.
func TestRequestTrailersRefreshCooldownLeavesNoInProcessClaim(t *testing.T) {
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
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("first request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)

	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusCooldown {
		t.Fatalf("second request = %+v, err = %v", outcome, err)
	}
	waitForOnDemandIdle(t, h.service)
}

// A refresh whose item_videos write failed is a failure for this action even
// though the pipeline reports success: the cooldown is a budget for fetching
// trailers, and charging a week for trailers that were fetched but not stored
// would strand the viewer.
func TestRequestTrailersRefreshReleasesSlotWhenVideoPersistFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// Stand in for mergeAndPersist: the pipeline succeeds overall while the
	// videos write fails and is only logged, which is exactly the shape the
	// observer exists to surface.
	persistErr := errors.New("replace item videos: connection reset")
	h.service.hooks.process = func(processCtx context.Context, req ProcessRequest) (*ProcessResult, error) {
		reportVideoPersistFailure(processCtx, persistErr)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	released := h.itemRepo.expectTrailersRelease()
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the slot to be released after a failed videos write")
	}
	waitForOnDemandIdle(t, h.service)

	if stored := h.itemRepo.trailersStoredAt("movie-1"); stored != nil {
		t.Fatalf("slot must be free after a failed videos write, stored %s", stored)
	}
	// The viewer can retry immediately rather than waiting out the window.
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("retry = %+v, want an immediately queued retry, err = %v", outcome, err)
	}
	waitForProcess(t, started)
}

// The detached goroutine does not survive a restart, so winning the gate also
// records durable debt: a process that dies mid-refresh leaves work the refresh
// worker picks up instead of an item locked out for the window having fetched
// nothing.
func TestRequestTrailersRefreshRecordsDurableDebt(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}

	debt, err := debts.GetTarget(ctx, RefreshTargetItem, "movie-1")
	if err != nil {
		t.Fatalf("queued request must leave durable debt behind: %v", err)
	}
	if !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonTrailersRequested) {
		t.Fatalf("reason mask = %d, want the trailers-requested reason set", debt.ReasonMask)
	}
	// Nothing is wrong with the item, so the row must not sit in a band that
	// front-runs genuine debt.
	if debt.Priority != refreshDebtPriority(0) {
		t.Fatalf("priority = %d, want the default band %d", debt.Priority, refreshDebtPriority(0))
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)
}

// A cooldown answer performs no work, so it must not enqueue debt either —
// otherwise repeated polling from a client would keep an item permanently due.
func TestRequestTrailersRefreshCooldownRecordsNoDebt(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	debts := newFakeRefreshDebtRepo()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("first request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)

	// Wire the debt repo only now, so anything it records can only have come
	// from the cooldown request below.
	h.service.refreshDebtRepo = debts
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusCooldown {
		t.Fatalf("second request = %+v, err = %v", outcome, err)
	}
	if _, err := debts.GetTarget(ctx, RefreshTargetItem, "movie-1"); !errors.Is(err, ErrRefreshDebtNotFound) {
		t.Fatalf("a cooldown answer must not enqueue debt, got err = %v", err)
	}
}

// A claim lost to a slot that keeps being freed underneath the repository is
// not a cooldown — there is no timestamp to report one with. Reporting it as
// queued matches the in-process-dedup answer: an equivalent refresh is running.
func TestRequestTrailersRefreshUndateableLostClaimReportsQueued(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	h.itemRepo.trailersClaimResult = &trailersClaimResult{}

	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		t.Errorf("a lost claim must not start a refresh (content_id %s)", req.ContentID)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	if outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusQueued)
	}
	if outcome.NextAllowedAt != nil {
		t.Fatalf("an undateable lost claim must not carry next_allowed_at, got %v", outcome.NextAllowedAt)
	}
	waitForOnDemandIdle(t, h.service)
}

// "Disabled" means every containing library turned remote videos off. That
// claim cannot be made from a partially-resolved set: an unreadable library
// might be the one that enables trailers, so any lookup failure degrades the
// answer to unknown scope (allow-all) rather than a guess the viewer sees as
// "trailers are disabled for this library".
func TestRequestTrailersRefreshUnreadableLibraryIsNotDisabled(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	for _, folderID := range []int{10, 11} {
		if err := h.libraryRepo.Upsert(ctx, "movie-1", folderID, time.Now()); err != nil {
			t.Fatalf("seed library membership %d: %v", folderID, err)
		}
	}
	// Folder 10 resolves with trailers off; folder 11 cannot be read at all.
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true, TrailerKinds: nil},
		},
		lookupErrs: map[int]error{11: errors.New("connection reset")},
	}

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
		t.Fatalf("status = %q, want %q — a failed library lookup is unknown scope, not disabled",
			outcome.Status, TrailerRefreshStatusQueued)
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)
}

// A library that no longer exists is not a failure: it cannot be the one
// enabling trailers, so it is skipped and the remaining libraries still decide.
func TestRequestTrailersRefreshMissingLibraryStillReportsDisabled(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	for _, folderID := range []int{10, 11} {
		if err := h.libraryRepo.Upsert(ctx, "movie-1", folderID, time.Now()); err != nil {
			t.Fatalf("seed library membership %d: %v", folderID, err)
		}
	}
	// Folder 11 is absent from the repo entirely (deleted library).
	h.service.folderRepo = &fakeMetadataFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true, TrailerKinds: nil},
		},
	}

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
}

// An admin lock on the videos field makes mergeAndPersist skip the item_videos
// write, so a refresh started for one would "succeed" having saved nothing and
// charge the viewer a week for it. The preflight has to catch that before the
// slot is consumed.
func TestRequestTrailersRefreshLockedVideosDoesNotConsumeCooldownSlot(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{
		ContentID:    "movie-1",
		Type:         "movie",
		Status:       "matched",
		LockedFields: []int{int(FieldVideos)},
	}

	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		t.Errorf("a videos-locked item must not start a refresh (content_id %s)", req.ContentID)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1")
	if err != nil {
		t.Fatalf("RequestTrailersRefresh: %v", err)
	}
	// Reuses "disabled" rather than a new status: clients treat unknown
	// statuses as a dead end, and "trailers cannot be fetched for this item"
	// is exactly what disabled already means to a viewer.
	if outcome.Status != TrailerRefreshStatusDisabled {
		t.Fatalf("status = %q, want %q", outcome.Status, TrailerRefreshStatusDisabled)
	}
	if got := h.itemRepo.trailersClaimCount(); got != 0 {
		t.Fatalf("a videos-locked item consumed the cooldown slot %d times, want 0", got)
	}
	waitForOnDemandIdle(t, h.service)

	// Unlocking lets the very next request through, proving the slot was never
	// touched.
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request after unlocking = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
}

// A lock on some *other* field says nothing about videos, so it must not block
// the action.
func TestRequestTrailersRefreshUnrelatedLockStillQueues(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.itemRepo.items["movie-1"] = &models.MediaItem{
		ContentID:    "movie-1",
		Type:         "movie",
		Status:       "matched",
		LockedFields: []int{int(FieldOverview), int(FieldImages)},
	}

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

// The recovery row is insurance against a process that dies mid-refresh, so it
// must not be claimable while the fast path could still be running: the refresh
// task calls RefreshScheduledTarget without consulting the in-process claim, so
// a due-now row would have the worker and the goroutine fetching the same item
// at once.
func TestRequestTrailersRefreshRecoveryDebtIsNotDueDuringTheFastPath(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// Hold the refresh open so the debt row is observed exactly while the fast
	// path is running — the window the reviewer's race lives in.
	inFlight := make(chan struct{})
	entered := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(entered)
		<-inFlight
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, entered)

	debt, err := debts.GetTarget(ctx, RefreshTargetItem, "movie-1")
	if err != nil {
		t.Fatalf("queued request must leave durable debt behind: %v", err)
	}
	if !debt.NextRefreshAt.After(time.Now().UTC().Add(metadataOnDemandRefreshTimeout)) {
		t.Fatalf("recovery debt is due at %s, which is within the on-demand refresh window (%s) — "+
			"the refresh worker could claim it alongside the running goroutine",
			debt.NextRefreshAt, metadataOnDemandRefreshTimeout)
	}

	close(inFlight)
	waitForOnDemandIdle(t, h.service)
}

// Once the fast path has done the work, the recovery row has nothing left to
// recover: leaving it would have the worker re-run a refresh that already
// happened as soon as the delay lapsed.
func TestRequestTrailersRefreshClearsRecoveryDebtOnSuccess(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// Process here stands in for a refresh whose own debt sync did not run
	// (hooks.process short-circuits processInternal), which is the case the
	// settle step exists to cover.
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)

	if _, err := debts.GetTarget(ctx, RefreshTargetItem, "movie-1"); !errors.Is(err, ErrRefreshDebtNotFound) {
		t.Fatalf("a completed fast path must leave no recovery debt, got err = %v", err)
	}
}

// Settling the recovery reason must not discard debt the item genuinely has:
// another reason in the mask means it still needs refreshing, and the queue
// should keep saying so.
func TestRequestTrailersRefreshSettleKeepsOtherDebtReasons(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}
	if err := debts.UpsertTargetDebt(ctx, RefreshTargetItem, "movie-1",
		refreshDebtPriority(RefreshDebtReasonCoreMetadataIncomplete),
		RefreshDebtReasonCoreMetadataIncomplete,
		time.Now().UTC()); err != nil {
		t.Fatalf("seed existing debt: %v", err)
	}

	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("request = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
	waitForOnDemandIdle(t, h.service)

	debt, err := debts.GetTarget(ctx, RefreshTargetItem, "movie-1")
	if err != nil {
		t.Fatalf("pre-existing debt must survive the settle: %v", err)
	}
	if hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonTrailersRequested) {
		t.Fatalf("reason mask = %d, want the trailers-requested bit cleared", debt.ReasonMask)
	}
	if !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonCoreMetadataIncomplete) {
		t.Fatalf("reason mask = %d, want the pre-existing core-metadata reason kept", debt.ReasonMask)
	}
}

// The durable recovery is the path taken when the process that consumed a slot
// died mid-refresh. It runs in a worker that never saw the claim, so without an
// explicit adoption a failed recovery would leave the viewer blocked for the
// whole window having stored nothing — the exact gap the release hook closes on
// the fast path.
func TestRefreshScheduledTargetReleasesInheritedTrailerClaimOnFailure(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	// Stand in for the state a dead process left behind: the slot is consumed
	// and the debt row carries the trailers-requested reason.
	claimed, claimedAt, err := h.itemRepo.TryClaimTrailersRefresh(ctx, "movie-1", TrailerRefreshCooldown)
	if err != nil || !claimed || claimedAt == nil {
		t.Fatalf("seed claim = %v, at = %v, err = %v", claimed, claimedAt, err)
	}
	if err := debts.UpsertTargetDebt(ctx, RefreshTargetItem, "movie-1",
		refreshDebtPriority(RefreshDebtReasonTrailersRequested),
		RefreshDebtReasonTrailersRequested,
		now); err != nil {
		t.Fatalf("seed recovery debt: %v", err)
	}

	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return nil, errors.New("tmdb is unreachable")
	}

	if err := h.service.RefreshScheduledTarget(ctx, RefreshTargetItem, "movie-1"); err == nil {
		t.Fatal("the recovery refresh was expected to fail")
	}
	if stored := h.itemRepo.trailersStoredAt("movie-1"); stored != nil {
		t.Fatalf("a failed recovery left the slot consumed until %s", stored)
	}

	// The viewer can retry immediately rather than waiting out a window in
	// which nothing was ever fetched.
	started := make(chan struct{})
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		close(started)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}
	if outcome, err := h.service.RequestTrailersRefresh(ctx, "movie-1"); err != nil ||
		outcome.Status != TrailerRefreshStatusQueued {
		t.Fatalf("retry after a failed recovery = %+v, err = %v", outcome, err)
	}
	waitForProcess(t, started)
}

// A recovery whose videos write failed and was only logged is a failure for the
// cooldown's purposes too: the pipeline reports success while none of the
// trailers the week was charged for were stored.
func TestRefreshScheduledTargetReleasesInheritedClaimWhenVideoPersistFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	if claimed, _, err := h.itemRepo.TryClaimTrailersRefresh(ctx, "movie-1", TrailerRefreshCooldown); err != nil || !claimed {
		t.Fatalf("seed claim = %v, err = %v", claimed, err)
	}
	if err := debts.UpsertTargetDebt(ctx, RefreshTargetItem, "movie-1",
		refreshDebtPriority(RefreshDebtReasonTrailersRequested),
		RefreshDebtReasonTrailersRequested,
		now); err != nil {
		t.Fatalf("seed recovery debt: %v", err)
	}

	persistErr := errors.New("replace item videos: connection reset")
	h.service.hooks.process = func(processCtx context.Context, req ProcessRequest) (*ProcessResult, error) {
		reportVideoPersistFailure(processCtx, persistErr)
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	// The refresh itself succeeded, so the queue must still see a success.
	if err := h.service.RefreshScheduledTarget(ctx, RefreshTargetItem, "movie-1"); err != nil {
		t.Fatalf("RefreshScheduledTarget: %v", err)
	}
	if stored := h.itemRepo.trailersStoredAt("movie-1"); stored != nil {
		t.Fatalf("slot must be free after a failed videos write, stored %s", stored)
	}
}

// A scheduled refresh for an item nobody asked trailers for owes no release:
// the item may hold a claim from an unrelated in-flight request, and clearing
// it would hand out a free extra refresh.
func TestRefreshScheduledTargetLeavesUnrelatedTrailerClaimsAlone(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	h.itemRepo.now = func() time.Time { return now }
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	h.itemRepo.items["movie-1"] = &models.MediaItem{ContentID: "movie-1", Type: "movie", Status: "matched"}

	if claimed, _, err := h.itemRepo.TryClaimTrailersRefresh(ctx, "movie-1", TrailerRefreshCooldown); err != nil || !claimed {
		t.Fatalf("seed claim = %v, err = %v", claimed, err)
	}
	// Debt without the trailers-requested reason: ordinary scheduled work.
	if err := debts.UpsertTargetDebt(ctx, RefreshTargetItem, "movie-1",
		refreshDebtPriority(RefreshDebtReasonCoreMetadataIncomplete),
		RefreshDebtReasonCoreMetadataIncomplete,
		now); err != nil {
		t.Fatalf("seed debt: %v", err)
	}

	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return nil, errors.New("tmdb is unreachable")
	}

	if err := h.service.RefreshScheduledTarget(ctx, RefreshTargetItem, "movie-1"); err == nil {
		t.Fatal("the scheduled refresh was expected to fail")
	}
	if h.itemRepo.trailersStoredAt("movie-1") == nil {
		t.Fatal("an unrelated scheduled refresh released a cooldown slot it does not own")
	}
	if got := h.itemRepo.trailersReleaseCount(); got != 0 {
		t.Fatalf("unrelated refresh released the slot %d times, want 0", got)
	}
}

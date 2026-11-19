package requests

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequestDownloadAggregatesLiveTargets(t *testing.T) {
	early := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	older, newer := early.Add(-2*time.Minute), early.Add(-time.Minute)
	req := Request{Targets: []Target{
		{Quality: Quality1080p, Status: StatusDownloading, Download: &DownloadProgress{
			Phase: DownloadPhaseDownloading, BytesTotal: 4000, BytesLeft: 1000, EstimatedCompletion: &early, Downloads: 1, UpdatedAt: newer,
		}},
		{Quality: Quality2160p, Status: StatusQueued, Download: &DownloadProgress{
			Phase: DownloadPhaseStalled, BytesTotal: 6000, BytesLeft: 6000, EstimatedCompletion: &late, Downloads: 2, UpdatedAt: older,
		}},
	}}
	got := req.Download()
	if got == nil {
		t.Fatal("Download() = nil, want the aggregate")
	}
	if got.Phase != DownloadPhaseStalled || got.BytesTotal != 10000 || got.BytesLeft != 7000 || got.Downloads != 3 {
		t.Fatalf("aggregate = %+v, want stalled, 10000/7000 bytes, 3 downloads", got)
	}
	if got.EstimatedCompletion == nil || !got.EstimatedCompletion.Equal(late) || !got.UpdatedAt.Equal(older) {
		t.Fatalf("aggregate = %+v, want the latest estimate and the oldest report", got)
	}
	// The aggregate is a copy: the targets keep their own figures.
	if req.Targets[0].Download.BytesTotal != 4000 || req.Targets[0].Download.Phase != DownloadPhaseDownloading {
		t.Fatalf("first target changed to %+v", req.Targets[0].Download)
	}
}

// sameProgress compares progress reports, ignoring UpdatedAt, which the
// store stamps.
func sameProgress(a, b *DownloadProgress) bool {
	if a == nil || b == nil {
		return a == b
	}
	if (a.EstimatedCompletion == nil) != (b.EstimatedCompletion == nil) ||
		(a.EstimatedCompletion != nil && !a.EstimatedCompletion.Equal(*b.EstimatedCompletion)) {
		return false
	}
	return a.Phase == b.Phase && a.BytesTotal == b.BytesTotal && a.BytesLeft == b.BytesLeft && a.Downloads == b.Downloads
}

// seedDownloadTarget stores an active request with one target on connection.
func seedDownloadTarget(t *testing.T, store *fakeStore, requestID, connection string, quality Quality, status Status, download *DownloadProgress) Target {
	t.Helper()
	if store.requests[requestID] == nil {
		store.requests[requestID] = &Request{ID: requestID, MediaType: MediaTypeMovie, TMDBID: 550, Status: status, Outcome: OutcomeActive}
	}
	target, err := store.CreateTarget(context.Background(), Target{
		RequestID: requestID, IntegrationID: connection, Quality: quality, Status: status, ExternalID: "123", Download: download,
	})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return target
}

func onlyTarget(t *testing.T, store *fakeStore, requestID string) Target {
	t.Helper()
	targets, _ := store.ListTargets(context.Background(), requestID)
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want one", targets)
	}
	return targets[0]
}

// downloadRefreshService serves two download servers: router-1 on a plugin
// that reports progress, router-2 on one that does not.
func downloadRefreshService(store *fakeStore) (*Service, *fakeRouterProvider) {
	store.integrations = []Integration{routerInstOn("router-1", 1), routerInstOn("router-2", 2)}
	router := &fakeRouterProvider{progressCapable: map[int]bool{1: true}}
	service := newTestService(store)
	service.SetRouterProvider(router)
	return service, router
}

func TestRefreshDownloadsPollsOnlyDownloadingTargetsOfProgressPlugins(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	earlier := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1}
	// req-mixed: 1080p downloading on the reporting plugin, 4K downloading
	// on the other. req-queued waits in the queue on the reporting plugin.
	// req-legacy downloads only through the other plugin.
	seedDownloadTarget(t, store, "req-mixed", "router-1", Quality1080p, StatusDownloading, earlier)
	seedDownloadTarget(t, store, "req-mixed", "router-2", Quality2160p, StatusDownloading, nil)
	seedDownloadTarget(t, store, "req-queued", "router-1", Quality1080p, StatusQueued, earlier)
	seedDownloadTarget(t, store, "req-legacy", "router-2", Quality1080p, StatusDownloading, nil)
	progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 400, Downloads: 1}
	router.statuses = []RouterTargetStatus{
		{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading, Progress: progress},
		{Quality: Quality2160p, ConnectionID: "router-2", Status: StatusCompleted},
	}
	presence := service.presence.(*fakePresence)

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("RefreshDownloads: %v", err)
	}
	if result != (DownloadRefreshResult{Checked: 1}) {
		t.Fatalf("result = %+v, want one request checked", result)
	}
	if len(router.statusLog) != 1 || router.statusLog[0].installationID != 1 ||
		len(router.statusLog[0].refs) != 1 || router.statusLog[0].refs[0].Quality != Quality1080p {
		t.Fatalf("status calls = %+v, want one call to installation 1 for the 1080p target", router.statusLog)
	}
	targets, _ := store.ListTargets(context.Background(), "req-mixed")
	for _, target := range targets {
		switch target.Quality {
		case Quality1080p:
			if !sameProgress(target.Download, progress) {
				t.Fatalf("1080p progress = %+v, want %+v", target.Download, progress)
			}
		case Quality2160p:
			// Not polled, so the completion reported for it was not applied.
			if target.Status != StatusDownloading || target.Download != nil {
				t.Fatalf("4K target = %+v, want it untouched", target)
			}
		}
	}
	if len(presence.got) != 0 || router.fulfillCalls != 0 {
		t.Fatalf("presence lookups = %d, submissions = %d; want neither", len(presence.got), router.fulfillCalls)
	}
}

// A pass ends within its budget even while a download server has stopped
// answering and each call to it would run to the router's deadline. The call
// in flight is cut, its target goes to the back of the rotation, and the
// requests left over wait for the next pass without failing this one.
func TestRefreshDownloadsStopsWhenItsBudgetRunsOut(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	store.integrations = append(store.integrations, routerInstOn("router-3", 3))
	router.progressCapable[3] = true
	router.statusHangFor = map[int]bool{1: true}
	now := time.Now().UTC()
	progress := func(age time.Duration) *DownloadProgress {
		return &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1, UpdatedAt: now.Add(-age)}
	}
	hungA := seedDownloadTarget(t, store, "req-hung-a", "router-1", Quality1080p, StatusDownloading, progress(3*time.Minute))
	seedDownloadTarget(t, store, "req-hung-b", "router-1", Quality1080p, StatusDownloading, progress(2*time.Minute))
	seedDownloadTarget(t, store, "req-healthy", "router-3", Quality1080p, StatusDownloading, progress(time.Minute))

	type outcome struct {
		result DownloadRefreshResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.RefreshDownloads(context.Background(), 200, 50*time.Millisecond)
		done <- outcome{result, err}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RefreshDownloads outlasted its budget while a server hung")
	}
	if got.err != nil || got.result != (DownloadRefreshResult{Checked: 1, Errors: 1}) {
		t.Fatalf("RefreshDownloads = %+v, %v; want the one cut call and no error", got.result, got.err)
	}
	if router.statusCalls != 1 {
		t.Fatalf("status calls = %d, want only the call the budget cut", router.statusCalls)
	}
	if !slices.Equal(store.downloadChecks, []int64{hungA.ID}) || onlyTarget(t, store, "req-hung-a").Download == nil {
		t.Fatalf("checks = %v; want the cut request's target marked asked about, its progress kept", store.downloadChecks)
	}
	next, err := store.ListDownloadingRequests(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := func() []string {
		ids := make([]string, 0, len(next))
		for _, req := range next {
			ids = append(ids, req.ID)
		}
		return ids
	}(), []string{"req-hung-b", "req-healthy", "req-hung-a"}; !slices.Equal(ids, want) {
		t.Fatalf("next pass order = %v, want %v", ids, want)
	}
}

// addDownloadTestTarget stores a target for a DB test, with progress reported
// and asked about refreshedAgo ago, or without progress when refreshedAgo is
// empty.
func addDownloadTestTarget(t *testing.T, repo *Repository, pool *pgxpool.Pool, requestID string, quality Quality, status Status, refreshedAgo string) Target {
	t.Helper()
	ctx := t.Context()
	target, err := repo.CreateTarget(ctx, Target{RequestID: requestID, Quality: quality, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	if refreshedAgo == "" {
		return target
	}
	if _, err := pool.Exec(ctx, `
		UPDATE media_request_targets
		SET download_phase = 'downloading', download_bytes_total = 1, download_bytes_left = 1,
		    download_count = 1, download_updated_at = now() - $2::interval,
		    download_checked_at = now() - $2::interval
		WHERE id = $1`, target.ID, refreshedAgo); err != nil {
		t.Fatal(err)
	}
	return target
}

func requestIDs(reqs []*Request) []string {
	ids := make([]string, 0, len(reqs))
	for _, req := range reqs {
		ids = append(ids, req.ID)
	}
	return ids
}

func TestListDownloadingRequestsOrderDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	addTarget := func(requestID string, quality Quality, status Status, refreshedAgo string) {
		t.Helper()
		_ = addDownloadTestTarget(t, repo, pool, requestID, quality, status, refreshedAgo)
	}
	for i, id := range []string{"dl-a", "dl-b", "dl-c", "dl-d", "dl-e", "dl-f", "dl-g", "dl-h"} {
		insertLifecycleRequest(t, repo, id, 1, 900+i, StatusDownloading)
	}
	addTarget("dl-a", Quality1080p, StatusDownloading, "1 minute")
	addTarget("dl-b", Quality1080p, StatusDownloading, "")      // no progress
	addTarget("dl-c", Quality1080p, StatusQueued, "30 minutes") // nothing downloading
	addTarget("dl-d", Quality1080p, StatusDownloading, "10 minutes")
	// One recent target does not hide a stale sibling.
	addTarget("dl-e", Quality1080p, StatusDownloading, "30 seconds")
	addTarget("dl-e", Quality2160p, StatusDownloading, "5 minutes")
	// A sibling without progress does not make the request look stale.
	addTarget("dl-f", Quality1080p, StatusDownloading, "2 minutes")
	addTarget("dl-f", Quality2160p, StatusDownloading, "")
	addTarget("dl-g", Quality1080p, StatusDownloading, "20 minutes")
	addTarget("dl-h", Quality1080p, StatusDownloading, "") // no progress
	// A closed request is not refreshed.
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'cancelled' WHERE id = 'dl-g'`); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListDownloadingRequests(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := requestIDs(got), []string{"dl-d", "dl-e", "dl-f", "dl-a"}; !slices.Equal(ids, want) {
		t.Fatalf("downloading requests = %v, want %v", ids, want)
	}
	limited, err := repo.ListDownloadingRequests(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := requestIDs(limited), []string{"dl-d", "dl-e"}; !slices.Equal(ids, want) {
		t.Fatalf("limited = %v, want the two refreshed longest ago", ids)
	}
}

package watchsync

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// drainExporterStub is a batched exporter whose every call is answered by
// respond, which sees the call's index and plays.
type drainExporterStub struct {
	watchedExporterStub
	batchSize int
	calls     *[][]LocalPlay
	tokens    *[]string // the access token each call received, when set
	respond   func(call int, plays []LocalPlay) (ExportResult, error)
}

func (p drainExporterStub) ExportBatchSize() int { return p.batchSize }

func (p drainExporterStub) ExportHistory(_ context.Context, _ ServerConfig, conn Connection, plays []LocalPlay) (ExportResult, error) {
	call := len(*p.calls)
	*p.calls = append(*p.calls, plays)
	if p.tokens != nil {
		*p.tokens = append(*p.tokens, conn.AccessToken)
	}
	return p.respond(call, plays)
}

func sendAllPlays(_ int, plays []LocalPlay) (ExportResult, error) {
	result := ExportResult{}
	for _, play := range plays {
		result.Sent = append(result.Sent, play.HistoryID)
	}
	return result, nil
}

// newDrainService returns a service over count completed movie plays, one
// minute apart.
func newDrainService(t *testing.T, count int) (*Service, *serviceFakeRepo, userstore.UserStore) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	start := time.Date(2020, time.November, 4, 14, 0, 0, 0, time.UTC)
	for i := range count {
		if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
			ID:              fmt.Sprintf("history-%03d", i),
			ProfileID:       "profile-1",
			MediaItemID:     testMovieMediaID,
			WatchedAt:       start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourcePlayback,
			Identity: userstore.WatchIdentity{
				StableType:  "movie",
				ProviderIDs: map[string]string{"tmdb": "603"},
			},
		}); err != nil {
			t.Fatalf("AddHistory: %v", err)
		}
	}
	repo := newServiceFakeRepo()
	repo.withConnection(drainConn)
	store := userdb.NewSQLiteUserStore(db)
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: store})
	return service, repo, store
}

var drainConn = Connection{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1"}

func exportStatusCounts(repo *serviceFakeRepo) map[string]int {
	counts := map[string]int{}
	for _, export := range repo.historyExports {
		counts[export.Status]++
	}
	return counts
}

func TestServiceExportWatchedDrainsBacklogInProviderBatches(t *testing.T) {
	service, repo, _ := newDrainService(t, 25)
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: sendAllPlays}

	result, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 25 {
		t.Fatalf("sent = %d, want 25", result.Sent)
	}
	sizes := make([]int, 0, len(calls))
	for _, plays := range calls {
		sizes = append(sizes, len(plays))
	}
	if want := []int{10, 10, 5}; !slices.Equal(sizes, want) {
		t.Fatalf("batch sizes = %v, want %v", sizes, want)
	}
	if calls[0][0].HistoryID != "history-000" || calls[2][4].HistoryID != "history-024" {
		t.Fatalf("batches are not oldest first: first %s, last %s", calls[0][0].HistoryID, calls[2][4].HistoryID)
	}
	if counts := exportStatusCounts(repo); counts[historyExportStatusSent] != 25 {
		t.Fatalf("export statuses = %v, want 25 sent", counts)
	}
	for _, size := range repo.historyPageSizes {
		if size != 10 {
			t.Fatalf("page sizes = %v, want each page one provider batch", repo.historyPageSizes)
		}
	}
}

func TestServiceExportWatchedTriesEachExportOncePerRun(t *testing.T) {
	service, repo, _ := newDrainService(t, 7)
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 3, calls: &calls, respond: func(_ int, plays []LocalPlay) (ExportResult, error) {
		result := ExportResult{Failed: map[string]string{}}
		for _, play := range plays {
			result.Failed[play.HistoryID] = "provider refused"
		}
		return result, nil
	}}

	result, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || result.Failed != 7 {
		t.Fatalf("calls = %d, failed = %d; want 3 calls and 7 failures", len(calls), result.Failed)
	}
	for _, export := range repo.historyExports {
		if export.Status != historyExportStatusFailed || export.AttemptCount != 1 {
			t.Fatalf("export %s: status %s, attempts %d; want failed after one attempt", export.HistoryID, export.Status, export.AttemptCount)
		}
	}

	// The next run retries the failures.
	calls = nil
	if _, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("second run calls = %d, want 3", len(calls))
	}
}

func TestServiceExportWatchedStopsDrainAtRateLimit(t *testing.T) {
	service, repo, _ := newDrainService(t, 25)
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: func(call int, plays []LocalPlay) (ExportResult, error) {
		if call == 1 {
			return ExportResult{}, RateLimitedError{Provider: "trakt", RetryAfter: time.Minute}
		}
		return sendAllPlays(call, plays)
	}}

	result, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter)
	if _, limited := AsRateLimited(err); !limited {
		t.Fatalf("error = %v, want rate limit", err)
	}
	if len(calls) != 2 || result.Sent != 10 {
		t.Fatalf("calls = %d, sent = %d; want 2 calls and 10 sent", len(calls), result.Sent)
	}
	counts := exportStatusCounts(repo)
	if counts[historyExportStatusSent] != 10 || counts[historyExportStatusPending] != 15 {
		t.Fatalf("export statuses = %v, want 10 sent and 15 pending", counts)
	}
}

func TestServiceExportWatchedStopsDrainWhenContextEnds(t *testing.T) {
	service, repo, _ := newDrainService(t, 25)
	ctx, cancel := context.WithCancel(t.Context())
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: func(call int, plays []LocalPlay) (ExportResult, error) {
		cancel()
		return sendAllPlays(call, plays)
	}}

	if _, err := service.ExportWatched(ctx, drainConn, ServerConfig{}, exporter); err == nil {
		t.Fatal("error = nil, want context cancellation")
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if counts := exportStatusCounts(repo); counts[historyExportStatusSent] != 10 || counts[historyExportStatusPending] != 15 {
		t.Fatalf("export statuses = %v, want 10 sent and 15 pending", counts)
	}
}

// A play watched during a run has an export the run's history snapshot does
// not hold. It stays pending for the next run; an export whose history entry
// is really gone is still retired.
func TestServiceExportWatchedLeavesExportsNewerThanItsHistoryReadPending(t *testing.T) {
	service, repo, _ := newDrainService(t, 3)
	now := time.Now().UTC()
	repo.historyExports = append(repo.historyExports,
		HistoryExport{ID: "export-live", ConnectionID: drainConn.ID, HistoryID: "history-live", MediaItemID: testMovieMediaID,
			WatchedAt: now, ProviderItemKey: testMovieProviderItemKey, Status: historyExportStatusPending, CreatedAt: now.Add(time.Hour)},
		HistoryExport{ID: "export-gone", ConnectionID: drainConn.ID, HistoryID: "history-gone", MediaItemID: testMovieMediaID,
			WatchedAt: now, ProviderItemKey: testMovieProviderItemKey, Status: historyExportStatusPending, CreatedAt: now.Add(-time.Hour)},
	)
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: sendAllPlays}

	if _, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, export := range repo.historyExports {
		statuses[export.HistoryID] = export.Status
	}
	if statuses["history-live"] != historyExportStatusPending || statuses["history-gone"] != historyExportStatusNotFound {
		t.Fatalf("statuses = %v, want history-live pending and history-gone not found", statuses)
	}
	if counts := exportStatusCounts(repo); counts[historyExportStatusSent] != 3 {
		t.Fatalf("export statuses = %v, want the 3 local plays sent", counts)
	}
}

// A run with a deadline sends at least one batch, then stops starting calls
// once the deadline is within exportDrainReserve.
func TestServiceExportWatchedStopsDrainNearTheRunDeadline(t *testing.T) {
	service, repo, _ := newDrainService(t, 25)
	ctx, cancel := context.WithTimeout(t.Context(), exportDrainReserve/2)
	defer cancel()
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: sendAllPlays}

	if _, err := service.ExportWatched(ctx, drainConn, ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if counts := exportStatusCounts(repo); counts[historyExportStatusSent] != 10 || counts[historyExportStatusPending] != 15 {
		t.Fatalf("export statuses = %v, want 10 sent and 15 pending", counts)
	}
}

// A play the user marks unwatched while the backlog drains is not sent as
// watched by a later batch; its export waits for the next run.
func TestServiceExportWatchedSkipsPlaysUnwatchedDuringTheDrain(t *testing.T) {
	service, repo, store := newDrainService(t, 25)
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: func(call int, plays []LocalPlay) (ExportResult, error) {
		if call == 0 {
			if err := store.RemoveHistoryItems(t.Context(), drainConn.ProfileID, []string{testMovieMediaID}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		return sendAllPlays(call, plays)
	}}

	if _, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want only the batch sent before the unwatch", len(calls))
	}
	if counts := exportStatusCounts(repo); counts[historyExportStatusSent] != 10 || counts[historyExportStatusPending] != 15 {
		t.Fatalf("export statuses = %v, want 10 sent and 15 pending", counts)
	}
}

// Credentials a plugin rotates during one call reach the next call and are
// not overwritten by the final connection save.
func TestServiceExportWatchedUsesCredentialsRotatedDuringTheDrain(t *testing.T) {
	service, repo, _ := newDrainService(t, 15)
	var calls [][]LocalPlay
	var tokens []string
	exporter := drainExporterStub{batchSize: 10, calls: &calls, tokens: &tokens, respond: func(call int, plays []LocalPlay) (ExportResult, error) {
		if call == 0 {
			rotated := drainConn
			rotated.AccessToken = "rotated"
			repo.withConnection(rotated)
		}
		return sendAllPlays(call, plays)
	}}

	if _, err := service.ExportWatched(t.Context(), drainConn, ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[1] != "rotated" {
		t.Fatalf("tokens per call = %q, want the second call to use the rotated token", tokens)
	}
	saved, ok, err := repo.GetConnectionByID(t.Context(), drainConn.ID)
	if err != nil || !ok || saved.AccessToken != "rotated" {
		t.Fatalf("saved connection token = %q (ok=%v, err=%v), want rotated", saved.AccessToken, ok, err)
	}
}

// countingHistoryLister records the queries it receives.
type countingHistoryLister struct {
	queries []userstore.CompletedHistoryQuery
}

func (l *countingHistoryLister) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	l.queries = append(l.queries, query)
	return nil, nil
}

// An empty page must not query history: an empty media filter reads it all.
func TestVisiblePlaysSkipsTheStoreForAnEmptyPage(t *testing.T) {
	lister := &countingHistoryLister{}
	plays, err := visiblePlays(t.Context(), lister, userstore.CompletedHistoryQuery{ProfileID: "profile-1"}, nil)
	if err != nil || len(plays) != 0 || len(lister.queries) != 0 {
		t.Fatalf("plays = %v, err = %v, queries = %d; want none and no store read", plays, err, len(lister.queries))
	}
	if _, err := visiblePlays(t.Context(), lister, userstore.CompletedHistoryQuery{ProfileID: "profile-1"}, []LocalPlay{{HistoryID: "h", MediaItemID: "m"}}); err != nil {
		t.Fatal(err)
	}
	if len(lister.queries) != 1 || !slices.Equal(lister.queries[0].MediaItemIDs, []string{"m"}) {
		t.Fatalf("queries = %+v, want one filtered to the page's item", lister.queries)
	}
}

// Once a batch has gone out, the deadline is checked on every page, so pages
// that end up with nothing to send cannot run the export past the reserve.
func TestServiceExportWatchedChecksTheDeadlineOnPagesWithNothingToSend(t *testing.T) {
	service, repo, store := newDrainService(t, 25)
	start := time.Now().UTC()
	clock := start
	service.now = func() time.Time { return clock }
	ctx, cancel := context.WithDeadline(t.Context(), start.Add(time.Hour))
	defer cancel()
	var calls [][]LocalPlay
	exporter := drainExporterStub{batchSize: 10, calls: &calls, respond: func(call int, plays []LocalPlay) (ExportResult, error) {
		// Hide the rest of the backlog, so later pages have nothing to send,
		// and move the clock inside the reserve.
		if err := store.RemoveHistoryItems(t.Context(), drainConn.ProfileID, []string{testMovieMediaID}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		clock = start.Add(time.Hour - exportDrainReserve/2)
		return sendAllPlays(call, plays)
	}}

	if _, err := service.ExportWatched(ctx, drainConn, ServerConfig{}, exporter); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(repo.historyPageSizes) != 1 {
		t.Fatalf("calls = %d, pages read = %d; want one batch and no further page reads", len(calls), len(repo.historyPageSizes))
	}
	if counts := exportStatusCounts(repo); counts[historyExportStatusSent] != 10 || counts[historyExportStatusPending] != 15 {
		t.Fatalf("export statuses = %v, want 10 sent and 15 pending", counts)
	}
}

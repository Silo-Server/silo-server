package historyimport

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// pagedProvider delivers one record per page and can cancel its service
// partway through, which is what a deploy looks like to a running import.
type pagedProvider struct {
	records []Record
	from    *StreamPosition
	stopAt  int
	stop    context.CancelFunc
}

func (p *pagedProvider) Stream(ctx context.Context, from *StreamPosition, sink RecordSink) error {
	p.from = from
	start := stageOffset(from, "listing")
	for i := start; i < len(p.records); i++ {
		if i == p.stopAt && p.stop != nil {
			p.stop()
			<-ctx.Done()
			return ctx.Err()
		}
		if err := sink(RecordBatch{Records: p.records[i : i+1], Resume: StreamPosition{Stage: "listing", Offset: i}}); err != nil {
			return err
		}
	}
	return nil
}

func checkpointService(ctx context.Context, repo *Repository) *Service {
	service := NewService(ctx, repo, pgstore.NewPostgresProvider(repo.pool))
	return service
}

// Two catalog items and a miss, with a page for each.
func checkpointRecords(at time.Time) []Record {
	stamp := at
	return []Record{
		{ExternalID: "movie", Kind: KindMovie, Title: "Movie", IMDbID: "tt1234567", Played: true, DurationSeconds: 100, UpdatedAt: at, LastPlayedAt: &stamp},
		{ExternalID: "missing", Kind: KindMovie, Title: "Missing", TMDBID: "999999", Played: true, UpdatedAt: at, LastPlayedAt: &stamp},
		{ExternalID: "episode", Kind: KindEpisode, Title: "Episode", SeriesTVDBID: "9000", SeasonNumber: 1, EpisodeNumber: 1, Played: true, DurationSeconds: 100, UpdatedAt: at, LastPlayedAt: &stamp},
	}
}

func runRow(t *testing.T, repo *Repository, id string) Run {
	t.Helper()
	run, err := repo.GetRunByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return *run
}

// A run stopped by a deploy goes back to the queue with its place kept, and
// finishing it leaves the totals and history a single uninterrupted run would.
func TestInterruptedRunResumesWithoutDuplicates(t *testing.T) {
	repo := limitsRepository(t)
	ctx := t.Context()
	records := checkpointRecords(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	queued, err := repo.EnqueueAdminRun(ctx, 1, AdminRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := repo.claimAdminRun(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}

	serverCtx, shutdown := context.WithCancel(ctx)
	defer shutdown()
	checkpointService(serverCtx, repo).executeRunWithClaim(claimed, &pagedProvider{records: records, stopAt: 2, stop: shutdown}, claim, true)

	if got := runRow(t, repo, queued.ID); got.Status != RunStatusQueued {
		t.Fatalf("after shutdown status = %s (%q), want queued", got.Status, got.ErrorMessage)
	}

	reclaimed, claim, err := repo.claimAdminRun(ctx)
	if err != nil || reclaimed == nil {
		t.Fatalf("reclaim: %v %v", reclaimed, err)
	}
	provider := &pagedProvider{records: records, stopAt: -1}
	checkpointService(ctx, repo).executeRunWithClaim(reclaimed, provider, claim, true)

	if provider.from == nil || provider.from.Offset != 1 {
		t.Fatalf("resumed from %+v, want the last applied page, offset 1", provider.from)
	}
	got := runRow(t, repo, queued.ID)
	if got.Status != RunStatusCompleted || got.Fetched != 3 || got.Matched != 2 || got.Unmatched != 1 {
		t.Fatalf("run = %s fetched=%d matched=%d unmatched=%d (%q)", got.Status, got.Fetched, got.Matched, got.Unmatched, got.ErrorMessage)
	}
	if history := limitsHistory(t, repo); len(history) != 2 {
		t.Fatalf("history = %v, want one row per matched item", history)
	}

	// A second import of the same source adds nothing.
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_user_mappings SET revision=revision WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	again, err := repo.EnqueueAdminRun(ctx, 1, AdminRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claimedAgain, claim, err := repo.claimAdminRun(ctx)
	if err != nil || claimedAgain == nil {
		t.Fatalf("claim again: %v %v", claimedAgain, err)
	}
	checkpointService(ctx, repo).executeRunWithClaim(claimedAgain, &pagedProvider{records: records, stopAt: -1}, claim, true)
	second := runRow(t, repo, again.ID)
	if second.Status != RunStatusCompleted || second.HistoryCreated != 0 || second.ProgressUpdated != 0 {
		t.Fatalf("re-run = %s created=%d updated=%d (%q)", second.Status, second.HistoryCreated, second.ProgressUpdated, second.ErrorMessage)
	}
	if history := limitsHistory(t, repo); len(history) != 2 {
		t.Fatalf("history after re-run = %v", history)
	}
}

func staleRun(t *testing.T, repo *Repository, mappingID int, checkpoint bool, attempts int) string {
	t.Helper()
	ctx := t.Context()
	run, err := repo.EnqueueAdminRun(ctx, mappingID, AdminRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.claimAdminRun(ctx); err != nil {
		t.Fatal(err)
	}
	var position any
	if checkpoint {
		position = `{"position":{"stage":"listing","offset":5},"summary":{}}`
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_runs SET checkpoint=$2::jsonb, resume_attempts=$3,
		last_heartbeat_at=now()-interval '1 hour' WHERE id=$1`, run.ID, position, attempts); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// A worker that died mid-run continues from its checkpoint, unless it has no
// place to continue from or it keeps dying at the same place.
func TestStaleSweepRequeuesOnlyRunsThatCanContinue(t *testing.T) {
	cases := []struct {
		name       string
		checkpoint bool
		attempts   int
		want       string
	}{
		{"with checkpoint", true, 0, RunStatusQueued},
		{"nothing to continue from", false, 0, RunStatusFailed},
		{"crash loop", true, maxRunResumesWithoutProgress, RunStatusFailed},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := limitsRepository(t)
			id := staleRun(t, repo, 1, tc.checkpoint, tc.attempts)
			service := checkpointService(t.Context(), repo)
			if err := service.reconcileStaleRuns(t.Context(), time.Now()); err != nil {
				t.Fatal(strconv.Itoa(i), err)
			}
			if got := runRow(t, repo, id); got.Status != tc.want {
				t.Fatalf("status = %s, want %s", got.Status, tc.want)
			}
		})
	}
}

// Only one very large run proceeds at a time, and its slot frees with it.
func TestOnlyOneLargeImportSlot(t *testing.T) {
	repo := limitsRepository(t)
	ctx := t.Context()
	first, err := repo.EnqueueAdminRun(ctx, 1, AdminRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.EnqueueAdminRun(ctx, 2, AdminRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, claimFirst, err := repo.claimAdminRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, claimSecond, err := repo.claimAdminRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimFirst.RunID != first.ID || claimSecond.RunID != second.ID {
		t.Fatalf("claims = %s, %s", claimFirst.RunID, claimSecond.RunID)
	}
	if ok, err := repo.tryLargeImportSlot(ctx, claimFirst); err != nil || !ok {
		t.Fatalf("first = %v %v", ok, err)
	}
	if ok, err := repo.tryLargeImportSlot(ctx, claimSecond); err != nil || ok {
		t.Fatalf("second while held = %v %v", ok, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_runs SET status='completed',completed_at=now() WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.tryLargeImportSlot(ctx, claimSecond); err != nil || !ok {
		t.Fatalf("second after release = %v %v", ok, err)
	}
}

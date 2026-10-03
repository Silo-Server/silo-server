package historyimport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// recordingSignalsNotifier records each signal change notification as the
// profile it named.
type recordingSignalsNotifier struct{ profiles []string }

func (n *recordingSignalsNotifier) NotifySignalsChanged(_ context.Context, _ int, profileID string) {
	n.profiles = append(n.profiles, profileID)
}

func TestRunChangedSignals(t *testing.T) {
	for name, tc := range map[string]struct {
		completed bool
		summary   ExecutionSummary
		want      bool
	}{
		"completed with nothing written": {completed: true, want: true},
		"failed after progress":          {summary: ExecutionSummary{ProgressUpdated: 1}, want: true},
		"failed after history":           {summary: ExecutionSummary{HistoryCreated: 1}, want: true},
		"failed after favorites":         {summary: ExecutionSummary{FavoritesImported: 1}, want: true},
		"failed after watchlist":         {summary: ExecutionSummary{WatchlistAdded: 1}, want: true},
		"failed having only matched":     {summary: ExecutionSummary{Fetched: 3, Matched: 2, Unmatched: 1, Skipped: 2}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runChangedSignals(tc.completed, tc.summary); got != tc.want {
				t.Fatalf("runChangedSignals = %v, want %v", got, tc.want)
			}
		})
	}
}

type failingFetchProvider struct{}

func (failingFetchProvider) Fetch(context.Context) ([]Record, []string, error) {
	return nil, nil, errors.New("media server unreachable")
}

// A completed run reports one signal change for its profile, however many
// records it wrote; a run that failed before writing anything reports none.
func TestImportRunReportsSignalChangesOncePostgres(t *testing.T) {
	ctx := trustLoopback(context.Background())
	pool := newPlexWatchlistImportTestPool(t)
	repo := NewRepository(pool, nil)
	notifier := &recordingSignalsNotifier{}
	service := &Service{
		repo:         repo,
		matcher:      NewMatcher(repo),
		stores:       pgstore.NewPostgresProvider(pool),
		bgContext:    ctx,
		runSemaphore: make(chan struct{}, maxConcurrentRuns),
		runCancels:   make(map[string]context.CancelFunc),
	}
	service.SetSignalsChangedNotifier(notifier)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, year, tmdb_id, status)
		VALUES ('movie-438631', $1, 'Dune', 2021, '438631', 'matched'),
		       ('movie-693134', $1, 'Dune: Part Two', 2024, '693134', 'matched')`, KindMovie); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	newRun := func(id string) *Run {
		t.Helper()
		run, err := repo.CreateRun(ctx, Run{
			ID: id, UserID: 42, ProfileID: "profile-1", SourceType: SourceTypePlex,
			ConnectionMode: ConnectionModePlexOAuth, Status: RunStatusQueued,
			Warnings: []string{}, UnmatchedSamples: []UnmatchedSample{},
		})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		return run
	}
	at := time.Date(2026, time.July, 2, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{Kind: KindMovie, Title: "Dune", Year: 2021, TMDBID: "438631", Watchlisted: true, UpdatedAt: at},
		{Kind: KindMovie, Title: "Dune: Part Two", Year: 2024, TMDBID: "693134", Watchlisted: true, UpdatedAt: at},
	}

	completed := newRun("signals-completed-run")
	service.executeRun(completed, staticWatchlistProvider{records: records})
	if got, err := repo.GetRunForUser(ctx, 42, completed.ID); err != nil || got.Status != RunStatusCompleted || got.WatchlistAdded != 2 {
		t.Fatalf("completed run = %+v, %v", got, err)
	}
	if len(notifier.profiles) != 1 || notifier.profiles[0] != "profile-1" {
		t.Fatalf("notifications after a completed run = %v, want one for profile-1", notifier.profiles)
	}

	failed := newRun("signals-failed-run")
	service.executeRun(failed, failingFetchProvider{})
	if got, err := repo.GetRunForUser(ctx, 42, failed.ID); err != nil || got.Status != RunStatusFailed {
		t.Fatalf("failed run = %+v, %v", got, err)
	}
	if len(notifier.profiles) != 1 {
		t.Fatalf("notifications after a run that wrote nothing = %d, want none", len(notifier.profiles)-1)
	}
}

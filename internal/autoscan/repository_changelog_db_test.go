package autoscan

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newChangeLogDBTest connects to SILO_TEST_DATABASE_URL (skipping when unset
// or when the change-log migration has not been applied) and seeds a poll
// source and a media folder for linked scan runs.
func newChangeLogDBTest(t *testing.T) (context.Context, *pgxpool.Pool, *Repository, Source, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var hasColumn bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'autoscan_events' AND column_name = 'change_log'
		)`).Scan(&hasColumn); err != nil {
		t.Fatalf("check change_log column: %v", err)
	}
	if !hasColumn {
		t.Skip("test database has not applied the autoscan change log migration")
	}

	repo := NewRepository(pool, nil)
	src, err := repo.CreateSource(ctx, Source{PluginID: "silo.autoscan.test", CapabilityID: "changelog", Enabled: true})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM autoscan_events WHERE source_id = $1`, src.ID)
		_ = repo.DeleteSource(context.Background(), src.ID)
	})

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', $1, true)
		RETURNING id`,
		fmt.Sprintf("autoscan-changelog-%d", time.Now().UnixNano()),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return ctx, pool, repo, src, folderID
}

func TestRepositoryPersistsEventChangeLogAndRunResults(t *testing.T) {
	ctx, pool, repo, src, folderID := newChangeLogDBTest(t)

	eventID, err := repo.CreateEvent(ctx, EventCreate{SourceID: src.ID, PluginID: src.PluginID, CapabilityID: src.CapabilityID})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	completedID := fmt.Sprintf("01CHANGELOGCOMPLETED%06d", folderID%1000000)
	skippedID := fmt.Sprintf("01CHANGELOGSKIPPED00%06d", folderID%1000000)
	runningID := fmt.Sprintf("01CHANGELOGRUNNING00%06d", folderID%1000000)
	for _, row := range []struct {
		id, path, status, payload string
	}{
		{completedID, "/movies/A", "completed", `{"new":4,"updated":1,"missing":2,"skipped":0,"phase":"done"}`},
		{skippedID, "/movies/B", "completed", `{"skipped":1}`},
		{runningID, "/movies/C", "running", `{"new":9,"message":"Processing files"}`},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO scan_runs (id, media_folder_id, mode, path, trigger, status, result_payload, autoscan_event_id)
			VALUES ($1, $2, 'subtree', $3, 'autoscan', $4, $5::jsonb, $6)`,
			row.id, folderID, row.path, row.status, row.payload, eventID,
		); err != nil {
			t.Fatalf("seed scan run %s: %v", row.id, err)
		}
	}

	changes := []ChangeRecord{
		{SourcePath: "/data/movies/A/a.mkv", RewrittenPath: "/movies/A/a.mkv", Scope: ChangeScopeFile, Outcome: ChangeOutcomeQueued, LibraryID: folderID, TargetMode: "subtree", TargetPath: "/movies/A", ScanRunID: completedID},
		{SourcePath: "/radarr/Film/Film.mkv", RewrittenPath: "/radarr/Film/Film.mkv", Scope: ChangeScopeFile, Outcome: ChangeOutcomeUnresolved, Reason: "no_library_match", Detail: "No library matches the given path"},
	}
	if err := repo.FinishEvent(ctx, EventFinish{
		ID:               eventID,
		Status:           EventStatusSuccess,
		ChangesReturned:  51,
		Changes:          changes,
		ChangesTruncated: true,
	}); err != nil {
		t.Fatalf("finish event: %v", err)
	}

	events, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	got := events[0]
	if !got.Event.ChangesTruncated || len(got.Event.Changes) != 2 {
		t.Fatalf("change log = %+v truncated=%v", got.Event.Changes, got.Event.ChangesTruncated)
	}
	for i := range changes {
		if got.Event.Changes[i] != changes[i] {
			t.Fatalf("change %d = %+v, want %+v", i, got.Event.Changes[i], changes[i])
		}
	}

	results := map[string]*ScanResult{}
	for _, run := range got.Runs {
		results[run.ID] = run.Result
	}
	if r := results[completedID]; r == nil || r.New != 4 || r.Updated != 1 || r.Missing != 2 || r.Skipped != 0 {
		t.Fatalf("completed run result = %+v", r)
	}
	if r := results[skippedID]; r == nil || r.Skipped != 1 || r.New != 0 {
		t.Fatalf("skipped run result = %+v", r)
	}
	if r, ok := results[runningID]; !ok || r != nil {
		t.Fatalf("running run result = %+v (present=%v), want nil", r, ok)
	}

	// A path that only appears in the change log is searchable.
	matched, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID, Search: "/radarr/film"})
	if err != nil {
		t.Fatalf("search events: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("search by change-log path = %d events, want 1", len(matched))
	}
	// Search matches path values, not the change log's key names or enums.
	for _, q := range []string{"source_path", "outcome", "queued"} {
		matched, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID, Search: q})
		if err != nil {
			t.Fatalf("search events %q: %v", q, err)
		}
		if len(matched) != 0 {
			t.Fatalf("search %q matched %d events, want 0", q, len(matched))
		}
	}

	// A NUL in a reported path is stored, so the event still finishes.
	nulEventID, err := repo.CreateEvent(ctx, EventCreate{SourceID: src.ID, PluginID: src.PluginID, CapabilityID: src.CapabilityID})
	if err != nil {
		t.Fatalf("create NUL event: %v", err)
	}
	nulChanges, _ := boundChangeRecords([]ChangeRecord{{SourcePath: "a\x00b", RewrittenPath: "a\x00b", Outcome: ChangeOutcomeUnresolved}})
	if err := repo.FinishEvent(ctx, EventFinish{ID: nulEventID, Status: EventStatusUnresolved, ChangesReturned: 1, Changes: nulChanges}); err != nil {
		t.Fatalf("finish event with NUL path: %v", err)
	}

	scans, err := repo.ListAutoscanScans(ctx, ScanListFilter{Search: completedID})
	if err != nil {
		t.Fatalf("list scans: %v", err)
	}
	if len(scans) != 1 || scans[0].Result == nil || scans[0].Result.New != 4 {
		t.Fatalf("scan rows = %+v", scans)
	}
}

func TestRepositoryEventWithoutChangeLogListsEmpty(t *testing.T) {
	ctx, _, repo, src, _ := newChangeLogDBTest(t)
	eventID, err := repo.CreateEvent(ctx, EventCreate{SourceID: src.ID, PluginID: src.PluginID, CapabilityID: src.CapabilityID})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := repo.FinishEvent(ctx, EventFinish{ID: eventID, Status: EventStatusSuccess}); err != nil {
		t.Fatalf("finish event: %v", err)
	}
	events, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Event.Changes == nil || len(events[0].Event.Changes) != 0 || events[0].Event.ChangesTruncated {
		t.Fatalf("event = %+v", events)
	}
}

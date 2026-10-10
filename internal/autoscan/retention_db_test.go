package autoscan

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestRepositoryPruneEventsKeepsRecentAndRunningEventsDB(t *testing.T) {
	ctx, pool, repo, src, folderID := newChangeLogDBTest(t)

	// The prune is table-wide, so the test passes a cutoff far in the past:
	// rows other tests write at the same time are all newer than it.
	cutoff := time.Date(2001, time.June, 1, 0, 0, 0, 0, time.UTC)
	old := cutoff.AddDate(0, 0, -60)

	var ids []int64
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM autoscan_events WHERE id = ANY($1)`, ids)
	})
	create := func(sourceID string, started time.Time) int64 {
		t.Helper()
		id, err := repo.CreateEvent(ctx, EventCreate{SourceID: sourceID, PluginID: src.PluginID, CapabilityID: src.CapabilityID, StartedAt: started})
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
		ids = append(ids, id)
		return id
	}
	finished := func(sourceID string, completed time.Time, status EventStatus) int64 {
		t.Helper()
		id := create(sourceID, completed.Add(-time.Second))
		changes := []ChangeRecord{{SourcePath: "/data/movies/A/a.mkv", RewrittenPath: "/movies/A/a.mkv", Outcome: ChangeOutcomeQueued}}
		if err := repo.FinishEvent(ctx, EventFinish{ID: id, Status: status, CompletedAt: completed, ChangesReturned: 1, Changes: changes}); err != nil {
			t.Fatalf("finish event: %v", err)
		}
		return id
	}

	oldSuccess := finished(src.ID, old, EventStatusSuccess)
	finished(src.ID, old.Add(time.Hour), EventStatusError)
	finished(src.ID, old.Add(2*time.Hour), EventStatusUnresolved)
	finished("", old.Add(3*time.Hour), EventStatusSuccess) // sourceless, as for a deleted source
	afterCutoff := finished(src.ID, cutoff.AddDate(0, 0, 1), EventStatusSuccess)
	current := finished(src.ID, time.Now(), EventStatusSuccess)
	// A running event's completed_at holds its start time until it finishes;
	// deleting it would make FinishEvent fail and let a second poll start.
	oldRunning := create(src.ID, old)

	runID := fmt.Sprintf("01PRUNEDEVENTRUN0000%06d", folderID%1000000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO scan_runs (id, media_folder_id, mode, path, trigger, status, result_payload, autoscan_event_id)
		VALUES ($1, $2, 'subtree', '/movies/A', 'autoscan', 'completed', '{"new":1}'::jsonb, $3)`,
		runID, folderID, oldSuccess,
	); err != nil {
		t.Fatalf("seed scan run: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM scan_runs WHERE id = $1`, runID)
	})

	// Four finished events predate the cutoff. One batch of two leaves two.
	result, err := repo.PruneEvents(ctx, cutoff, 2, 1)
	if err != nil {
		t.Fatalf("first prune: %v", err)
	}
	if result.Deleted != 2 || !result.LimitReached {
		t.Fatalf("first prune = %+v, want 2 deleted with the limit reached", result)
	}
	// The next batch empties the backlog exactly as the budget runs out, which
	// is not a truncated run.
	result, err = repo.PruneEvents(ctx, cutoff, 2, 1)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if result.Deleted != 2 || result.LimitReached {
		t.Fatalf("second prune = %+v, want 2 deleted without the limit", result)
	}
	result, err = repo.PruneEvents(ctx, cutoff, 2, 10)
	if err != nil {
		t.Fatalf("third prune: %v", err)
	}
	if result.Deleted != 0 || result.LimitReached {
		t.Fatalf("third prune = %+v, want nothing left", result)
	}

	rows, err := pool.Query(ctx, `SELECT id FROM autoscan_events WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		t.Fatalf("read remaining events: %v", err)
	}
	var remaining []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan remaining event: %v", err)
		}
		remaining = append(remaining, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read remaining events: %v", err)
	}
	want := []int64{afterCutoff, current, oldRunning}
	slices.Sort(want)
	if !slices.Equal(remaining, want) {
		t.Fatalf("remaining events = %v, want %v (after cutoff, current, running)", remaining, want)
	}

	// The Activity reads still work: the source's history lists what is left,
	// and the scan run of a pruned event stays, without its event link.
	events, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("listed events = %d, want 3", len(events))
	}
	scans, err := repo.ListAutoscanScans(ctx, ScanListFilter{Search: runID})
	if err != nil {
		t.Fatalf("list scans: %v", err)
	}
	if len(scans) != 1 || scans[0].AutoscanEventID != nil || scans[0].EventStatus != "" || scans[0].Result == nil || scans[0].Result.New != 1 {
		t.Fatalf("scan of a pruned event = %+v, want the run with no event link", scans)
	}
}

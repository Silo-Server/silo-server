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

	// Old events are written newest first, so deleting in table order instead
	// of oldest first would pick the wrong ones.
	oldSourceless := finished("", old.Add(3*time.Hour), EventStatusSuccess) // as for a deleted source
	oldUnresolved := finished(src.ID, old.Add(2*time.Hour), EventStatusUnresolved)
	finished(src.ID, old.Add(time.Hour), EventStatusError)
	oldSuccess := finished(src.ID, old, EventStatusSuccess)
	// The oldest event still has a queued follow-up scan run, which inherits
	// its event; it stays until that run ends.
	followUpEvent := finished(src.ID, old.Add(-time.Hour), EventStatusSuccess)
	afterCutoff := finished(src.ID, cutoff.AddDate(0, 0, 1), EventStatusSuccess)
	current := finished(src.ID, time.Now(), EventStatusSuccess)
	// A running event's completed_at holds its start time until it finishes;
	// deleting it would make FinishEvent fail and let a second poll start.
	oldRunning := create(src.ID, old)

	runID := fmt.Sprintf("01PRUNEDEVENTRUN0000%06d", folderID%1000000)
	followUpRunID := fmt.Sprintf("01PRUNEDFOLLOWUP0000%06d", folderID%1000000)
	for _, run := range []struct {
		id, path, status string
		eventID          int64
	}{
		{runID, "/movies/A", "completed", oldSuccess},
		{followUpRunID, "/movies/B", "accepted", followUpEvent},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO scan_runs (id, media_folder_id, mode, path, trigger, status, result_payload, autoscan_event_id)
			VALUES ($1, $2, 'subtree', $3, 'autoscan', $4, '{"new":1}'::jsonb, $5)`,
			run.id, folderID, run.path, run.status, run.eventID,
		); err != nil {
			t.Fatalf("seed scan run %s: %v", run.id, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM scan_runs WHERE id = ANY($1)`, []string{runID, followUpRunID})
	})

	wantRemaining := func(label string, want ...int64) {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT id FROM autoscan_events WHERE id = ANY($1) ORDER BY id`, ids)
		if err != nil {
			t.Fatalf("%s: read remaining events: %v", label, err)
		}
		defer rows.Close()
		var got []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("%s: scan remaining event: %v", label, err)
			}
			got = append(got, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: read remaining events: %v", label, err)
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: remaining events = %v, want %v", label, got, want)
		}
	}
	prune := func(label string, batchSize, maxBatches int, wantDeleted int64, wantLimit bool) {
		t.Helper()
		result, err := repo.PruneEvents(ctx, cutoff, batchSize, maxBatches)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if result.Deleted != wantDeleted || result.LimitReached != wantLimit {
			t.Fatalf("%s = %+v, want %d deleted, limit reached %v", label, result, wantDeleted, wantLimit)
		}
	}

	// Four old events can go now. One batch of two takes the two oldest.
	prune("first prune", 2, 1, 2, true)
	wantRemaining("after the first batch", oldSourceless, oldUnresolved, followUpEvent, afterCutoff, current, oldRunning)
	// The next batch empties the backlog exactly as the budget runs out, which
	// is not a truncated run.
	prune("second prune", 2, 1, 2, false)
	prune("third prune", 2, 10, 0, false)
	wantRemaining("while the follow-up is queued", followUpEvent, afterCutoff, current, oldRunning)

	// Once the follow-up run ends, its event goes too.
	if _, err := pool.Exec(ctx, `UPDATE scan_runs SET status = 'completed' WHERE id = $1`, followUpRunID); err != nil {
		t.Fatalf("complete follow-up run: %v", err)
	}
	prune("prune after the follow-up", 2, 10, 1, false)
	wantRemaining("after the follow-up ended", afterCutoff, current, oldRunning)

	// The Activity reads still work: the source's history lists what is left,
	// and the scan runs of pruned events stay, without their event link.
	events, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("listed events = %d, want 3", len(events))
	}
	for _, id := range []string{runID, followUpRunID} {
		scans, err := repo.ListAutoscanScans(ctx, ScanListFilter{Search: id})
		if err != nil {
			t.Fatalf("list scans: %v", err)
		}
		if len(scans) != 1 || scans[0].AutoscanEventID != nil || scans[0].EventStatus != "" || scans[0].Result == nil || scans[0].Result.New != 1 {
			t.Fatalf("scan %s of a pruned event = %+v, want the run with no event link", id, scans)
		}
	}
}

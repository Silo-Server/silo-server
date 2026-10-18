package scanqueue

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

func TestEnqueueAutoscanScansReportsRunPerTarget(t *testing.T) {
	ctx, pool, repo, folderID := openDirectRunTestRepository(t)

	var eventID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO autoscan_events (plugin_id, capability_id, started_at, completed_at, status)
		VALUES ('silo.autoscan.test', 'enqueue-outcomes', now(), now(), 'success')
		RETURNING id`).Scan(&eventID); err != nil {
		t.Fatalf("seed autoscan event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM scan_runs WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM autoscan_events WHERE id = $1`, eventID)
	})

	svc := NewService(repo, nil, nil, nil, ctx, 1, 1)
	folder := &models.MediaFolder{ID: folderID}
	first, err := svc.EnqueueAutoscanScans(ctx, []scantrigger.Target{
		{Folder: folder, Mode: ModeSubtree, Path: "/show/a", Trigger: "autoscan"},
	}, eventID)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if len(first) != 1 || !first[0].Created || first[0].RunID == "" {
		t.Fatalf("first outcomes = %+v", first)
	}

	second, err := svc.EnqueueAutoscanScans(ctx, []scantrigger.Target{
		{Folder: folder, Mode: ModeSubtree, Path: "/show/b", Trigger: "autoscan"},
		{Folder: folder, Mode: ModeSubtree, Path: "/show/a", Trigger: "autoscan"},
	}, eventID)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second outcomes = %+v", second)
	}
	if !second[0].Created || second[0].RunID == "" || second[0].RunID == first[0].RunID {
		t.Fatalf("new scope outcome = %+v", second[0])
	}
	if second[1].Created || second[1].RunID != first[0].RunID || second[1].FollowUp {
		t.Fatalf("coalesced outcome = %+v, want reuse of queued %s", second[1], first[0].RunID)
	}

	// Once the run is running it may have passed the change, so a request for
	// its scope is owed a follow-up scan instead.
	if _, err := pool.Exec(ctx, `UPDATE scan_runs SET status = $2, started_at = now() WHERE id = $1`, first[0].RunID, StatusRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	third, err := svc.EnqueueAutoscanScans(ctx, []scantrigger.Target{
		{Folder: folder, Mode: ModeSubtree, Path: "/show/a", Trigger: "autoscan"},
	}, eventID)
	if err != nil {
		t.Fatalf("third enqueue: %v", err)
	}
	if len(third) != 1 || third[0].Created || third[0].RunID != first[0].RunID || !third[0].FollowUp {
		t.Fatalf("running coalesced outcome = %+v, want follow-up on %s", third, first[0].RunID)
	}
}

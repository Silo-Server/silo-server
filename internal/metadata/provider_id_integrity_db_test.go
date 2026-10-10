package metadata

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/webhooksync/webhooksynctest"
)

func TestSyncMergeStepsAfterPlexTableDropPostgres(t *testing.T) {
	tx := webhooksynctest.BeginAfterPlexDrop(t)
	suffix := uuid.NewString()
	source, canonical := "drop-test-source-"+suffix, "drop-test-canonical-"+suffix
	if _, err := tx.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status) VALUES ($1,'movie','Source','pending'), ($2,'movie','Canonical','pending')`, source, canonical); err != nil {
		t.Fatal(err)
	}
	connectionID := webhooksynctest.SeedItemState(t, tx, source, 12)
	// Execute the sync steps from the production canonicalization sequence.
	// Including retired Plex steps here makes their removal a regression guard.
	for _, step := range mediaItemMergeSteps {
		if !strings.Contains(step.sql, "plex_sync_") && !strings.Contains(step.sql, "webhook_sync_") {
			continue
		}
		if _, err := tx.Exec(t.Context(), step.sql, mergeStepArgs(step.sql, source, canonical)...); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
	var id string
	var position float64
	if err := tx.QueryRow(t.Context(), `SELECT media_item_id,last_position_seconds FROM webhook_sync_item_state WHERE connection_id=$1`, connectionID).Scan(&id, &position); err != nil {
		t.Fatal(err)
	}
	if id != canonical || position != 12 {
		t.Fatalf("webhook state = %s, %v", id, position)
	}
}

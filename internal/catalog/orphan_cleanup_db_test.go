package catalog

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/webhooksync/webhooksynctest"
)

// Use the actual cleanup predicate against the migrated schema and live webhook
// references. A stale Plex-table reference makes this query fail with 42P01.
func TestOrphanCleanupAfterPlexTableDropPostgres(t *testing.T) {
	tx := webhooksynctest.BeginAfterPlexDrop(t)
	suffix := uuid.NewString()
	orphan, webhook := "drop-test-orphan-"+suffix, "drop-test-webhook-"+suffix
	if _, err := tx.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status) VALUES ($1,'movie','Orphan','pending'), ($2,'movie','Webhook','pending')`, orphan, webhook); err != nil {
		t.Fatal(err)
	}
	webhooksynctest.SeedItemState(t, tx, webhook, 0)
	rows, err := tx.Query(t.Context(), `SELECT mi.content_id FROM public.media_items mi `+orphanedProvisionalMediaItemPredicate+` AND mi.content_id IN ($1,$2) ORDER BY mi.content_id`, orphan, webhook)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{orphan}) {
		t.Fatalf("cleanup candidates = %v", got)
	}
}

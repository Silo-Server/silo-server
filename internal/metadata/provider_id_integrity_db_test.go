package metadata

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// beginMergeTestTx opens a transaction on SILO_TEST_DATABASE_URL that rolls
// back when the test ends, and seeds a movie source and canonical item.
func beginMergeTestTx(t *testing.T) (tx pgx.Tx, source, canonical string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err = conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	suffix := uuid.NewString()
	source, canonical = "merge-test-source-"+suffix, "merge-test-canonical-"+suffix
	if _, err := tx.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status) VALUES ($1,'movie','Source','pending'), ($2,'movie','Canonical','pending')`, source, canonical); err != nil {
		t.Fatal(err)
	}
	return tx, source, canonical
}

// A notification mute follows its series through a merge: a profile that
// muted both keeps one mute with the earlier time, and a profile that muted
// only the source is muted on the canonical item.
func TestCanonicalizeMediaItemReferencesMergesNotificationMutesPostgres(t *testing.T) {
	tx, source, canonical := beginMergeTestTx(t)
	var userID int
	if err := tx.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "merge-test-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(24 * time.Hour)
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO profile_series_notification_mutes (user_id, profile_id, series_id, muted_at)
		VALUES ($1, 'both', $2, $4), ($1, 'both', $3, $5), ($1, 'source-only', $2, $5)`,
		userID, source, canonical, early, late); err != nil {
		t.Fatal(err)
	}

	if err := canonicalizeMediaItemReferencesTx(t.Context(), tx, source, canonical); err != nil {
		t.Fatalf("merge: %v", err)
	}

	rows, err := tx.Query(t.Context(), `
		SELECT profile_id, series_id, muted_at FROM profile_series_notification_mutes
		WHERE user_id = $1 ORDER BY profile_id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	type mute struct {
		profile, series string
		at              time.Time
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mute, error) {
		var m mute
		err := row.Scan(&m.profile, &m.series, &m.at)
		m.at = m.at.UTC()
		return m, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []mute{{"both", canonical, early}, {"source-only", canonical, late}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutes after merge:\n got %+v\nwant %+v", got, want)
	}
}

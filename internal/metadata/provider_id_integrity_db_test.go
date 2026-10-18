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

// The whole merge runs every step against the migrated schema, so a conflict
// target that no longer matches a table's key fails here instead of in
// production.
func TestCanonicalizeMediaItemReferencesWithoutCollectionRowsPostgres(t *testing.T) {
	tx, source, canonical := beginMergeTestTx(t)
	if err := canonicalizeMediaItemReferencesTx(t.Context(), tx, source, canonical); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

func TestCanonicalizeMediaItemReferencesMergesPersonalCollectionItemsPostgres(t *testing.T) {
	tx, source, canonical := beginMergeTestTx(t)
	var userID int
	if err := tx.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "merge-test-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(24 * time.Hour)
	// "shared" holds both items as whole-item entries, and the canonical item
	// also as an episode entry. "moved" holds only the source item, as a
	// whole-item entry and an episode entry.
	seed := []struct {
		collection, item, sub string
		position              int
		addedAt               time.Time
	}{
		{"shared", source, "", 2, early},
		{"shared", canonical, "", 5, late},
		{"shared", canonical, "ep-1", 7, late},
		{"shared", source, "ep-2", 8, early},
		{"moved", source, "", 3, late},
		{"moved", source, "ep-1", 4, early},
	}
	for _, row := range seed {
		if _, err := tx.Exec(t.Context(), `INSERT INTO user_personal_collection_items(user_id,collection_id,media_item_id,sub_item_id,position,added_at) VALUES($1,$2,$3,$4,$5,$6)`,
			userID, row.collection, row.item, row.sub, row.position, row.addedAt); err != nil {
			t.Fatal(err)
		}
	}

	if err := canonicalizeMediaItemReferencesTx(t.Context(), tx, source, canonical); err != nil {
		t.Fatalf("merge: %v", err)
	}

	rows, err := tx.Query(t.Context(), `
		SELECT collection_id, media_item_id, sub_item_id, position, added_at
		FROM user_personal_collection_items
		WHERE user_id = $1
		ORDER BY collection_id, sub_item_id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		collection, item, sub string
		position              int
		addedAt               time.Time
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
		var it item
		err := row.Scan(&it.collection, &it.item, &it.sub, &it.position, &it.addedAt)
		it.addedAt = it.addedAt.UTC()
		return it, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []item{
		{"moved", canonical, "", 3, late},
		{"moved", canonical, "ep-1", 4, early},
		{"shared", canonical, "", 2, early},
		{"shared", canonical, "ep-1", 7, late},
		{"shared", canonical, "ep-2", 8, early},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collection items after merge:\n got %+v\nwant %+v", got, want)
	}
}

// A merge keeps the source's provider IDs for providers the canonical item has
// no ID for, and keeps the canonical item's own ID for the rest.
func TestCanonicalizeMediaItemReferencesMovesProviderIDsPostgres(t *testing.T) {
	tx, source, canonical := beginMergeTestTx(t)
	suffix := uuid.NewString()
	seed := []struct{ item, provider, id string }{
		{source, "tmdb", "src-tmdb-" + suffix},
		{source, "imdb", "src-imdb-" + suffix},
		{canonical, "tmdb", "canon-tmdb-" + suffix},
	}
	for _, row := range seed {
		if _, err := tx.Exec(t.Context(), `INSERT INTO media_item_provider_ids(content_id,item_type,provider,provider_id) VALUES($1,'movie',$2,$3)`,
			row.item, row.provider, row.id); err != nil {
			t.Fatal(err)
		}
	}

	if err := canonicalizeMediaItemReferencesTx(t.Context(), tx, source, canonical); err != nil {
		t.Fatalf("merge: %v", err)
	}

	rows, err := tx.Query(t.Context(), `
		SELECT content_id || ' ' || provider || ' ' || provider_id
		FROM media_item_provider_ids
		WHERE content_id = ANY($1::text[])
		ORDER BY provider`, []string{source, canonical})
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		canonical + " imdb src-imdb-" + suffix,
		canonical + " tmdb canon-tmdb-" + suffix,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider IDs after merge:\n got %q\nwant %q", got, want)
	}
}

// Deleting the source item cascades to its ebook annotations, so the merge
// must move them first.
func TestCanonicalizeMediaItemReferencesMovesEbookAnnotationsPostgres(t *testing.T) {
	tx, source, canonical := beginMergeTestTx(t)
	var userID int
	if err := tx.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "merge-test-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	annotationID := "merge-test-annotation-" + uuid.NewString()
	if _, err := tx.Exec(t.Context(), `INSERT INTO ebook_reader_annotations(id,user_id,profile_id,content_id,kind,location) VALUES($1,$2,'p1',$3,'bookmark','loc-1')`,
		annotationID, userID, source); err != nil {
		t.Fatal(err)
	}

	if err := canonicalizeMediaItemReferencesTx(t.Context(), tx, source, canonical); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM media_items WHERE content_id = $1`, source); err != nil {
		t.Fatal(err)
	}

	var owner string
	if err := tx.QueryRow(t.Context(), `SELECT content_id FROM ebook_reader_annotations WHERE id = $1`, annotationID).Scan(&owner); err != nil {
		t.Fatalf("annotation after merge: %v", err)
	}
	if owner != canonical {
		t.Fatalf("annotation belongs to %s, want %s", owner, canonical)
	}
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

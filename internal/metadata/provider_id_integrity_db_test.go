package metadata

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

// A drift repair of an item waits for an admin's ID edit holding that item,
// rather than deleting the item's provider rows first and deadlocking with
// the edit's own delete of one of them, and then leaves the edited ID alone
// instead of writing back the ID it read before the edit. Both repairs that
// write the item's rows do this: a clean insert, and moving the ID off a
// provisional owner.
func TestProviderIDDriftRepairWaitsForItemEditPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	repairer := NewProviderIDIntegrityRepairer(pool)
	for _, tc := range []struct {
		name   string
		repair func(ctx context.Context, owner string, row providerIDDriftRow) (bool, error)
	}{
		{"clean insert", func(ctx context.Context, _ string, row providerIDDriftRow) (bool, error) {
			return repairer.insertProviderIDsForDriftRow(ctx, row, providerEntriesFromDrift(row))
		}},
		{"provisional owner", func(ctx context.Context, owner string, row providerIDDriftRow) (bool, error) {
			return repairer.moveProvisionalProviderClaim(ctx, owner, row, providerEntriesFromDrift(row))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			suffix := uuid.NewString()
			contentID, owner := "drift-repair-edit-"+suffix, "drift-repair-owner-"+suffix
			tmdbID := "drift-" + suffix
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{contentID, owner})
			})
			if _, err := pool.Exec(ctx, `
				INSERT INTO media_items (content_id, type, title, genres, status, tmdb_id)
				VALUES ($1, 'movie', 'Drift Repair', '{}'::text[], 'matched', $3),
					($2, 'movie', 'Provisional Owner', '{}'::text[], 'pending', '')`,
				contentID, owner, tmdbID); err != nil {
				t.Fatalf("seed items: %v", err)
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
				VALUES ($1, 'movie', 'tmdb', $3), ($2, 'movie', 'tmdb', $4)`,
				contentID, owner, "stale-"+tmdbID, tmdbID); err != nil {
				t.Fatalf("seed durable ids: %v", err)
			}

			// The edit locks the item as lockCuratedProviderIDs does.
			edit, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin edit: %v", err)
			}
			defer edit.Rollback(ctx) //nolint:errcheck // a no-op once the edit commits
			if _, err := edit.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, contentID); err != nil {
				t.Fatalf("lock item: %v", err)
			}
			row := providerIDDriftRow{ContentID: contentID, ItemType: "movie", Status: "matched", TMDBID: tmdbID}
			repaired := make(chan error, 1)
			go func() {
				wrote, err := tc.repair(ctx, owner, row)
				if err == nil && wrote {
					err = errors.New("repair wrote provider ids read before the edit")
				}
				repaired <- err
			}()
			// Before the repair locked the item, it waited in its insert's
			// foreign-key check instead.
			waitForMediaItemLockWait(t, pool, repaired)
			// The edit changes the TMDB ID, then syncs its durable row the way
			// syncEditedProviderIDTx does.
			editedID := "edited-" + tmdbID
			if _, err := edit.Exec(ctx, `UPDATE media_items SET tmdb_id = $2 WHERE content_id = $1`, contentID, editedID); err != nil {
				t.Fatalf("edit column: %v", err)
			}
			if _, err := edit.Exec(ctx, `
				DELETE FROM media_item_provider_ids
				WHERE content_id = $1 AND lower(provider) = 'tmdb'`, contentID); err != nil {
				t.Fatalf("edit delete: %v", err)
			}
			if _, err := edit.Exec(ctx, `
				INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
				VALUES ($1, 'movie', 'tmdb', $2)`, contentID, editedID); err != nil {
				t.Fatalf("edit insert: %v", err)
			}
			if err := edit.Commit(ctx); err != nil {
				t.Fatalf("commit edit: %v", err)
			}
			if err := <-repaired; err != nil {
				t.Fatalf("repair: %v", err)
			}
			var durable string
			if err := pool.QueryRow(ctx, `
				SELECT provider_id FROM media_item_provider_ids
				WHERE content_id = $1 AND provider = 'tmdb'`, contentID).Scan(&durable); err != nil {
				t.Fatalf("read durable id: %v", err)
			}
			if durable != editedID {
				t.Fatalf("durable tmdb = %q, want the edited %q", durable, editedID)
			}
		})
	}
}

// An automatic rebind of a skeleton onto its matched item waits for an
// admin's ID edit holding the skeleton, rather than moving the skeleton's
// provider rows first and deadlocking with the edit's delete of one of them.
func TestRebindWaitsForItemEditPostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	suffix := uuid.NewString()
	source, target := "rebind-edit-source-"+suffix, "rebind-edit-target-"+suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{source, target})
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, status)
		VALUES ($1, 'movie', 'Skeleton', '{}'::text[], 'pending'), ($2, 'movie', 'Matched', '{}'::text[], 'matched')`,
		source, target); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
		VALUES ($1, 'movie', 'tmdb', $2)`, source, "rebind-"+suffix); err != nil {
		t.Fatalf("seed durable id: %v", err)
	}

	// The edit locks the skeleton as lockCuratedProviderIDs does.
	edit, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin edit: %v", err)
	}
	defer edit.Rollback(ctx) //nolint:errcheck // a no-op once the edit commits
	if _, err := edit.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, source); err != nil {
		t.Fatalf("lock item: %v", err)
	}
	rebound := make(chan error, 1)
	go func() { rebound <- identifyMoveService(pool).rebindItemOnto(ctx, source, target, false, false) }()
	// Before the rebind locked the skeleton, it waited at its final delete.
	waitForMediaItemLockWait(t, pool, rebound)
	// syncEditedProviderIDTx's delete of the edited provider's row.
	if _, err := edit.Exec(ctx, `
		DELETE FROM media_item_provider_ids
		WHERE content_id = $1 AND lower(provider) = 'tmdb'`, source); err != nil {
		t.Fatalf("edit delete: %v", err)
	}
	if err := edit.Commit(ctx); err != nil {
		t.Fatalf("commit edit: %v", err)
	}
	if err := <-rebound; err != nil {
		t.Fatalf("rebind: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_items WHERE content_id = $1`, source).Scan(&remaining); err != nil {
		t.Fatalf("read skeleton: %v", err)
	}
	if remaining != 0 {
		t.Fatal("rebind left the skeleton in place")
	}
}

// A repair that moves an ID off a provisional owner leaves the owner alone
// when an edit it waited for took the ID off the owner.
func TestProviderIDDriftRepairSkipsChangedOwnerPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	suffix := uuid.NewString()
	contentID, owner := "drift-repair-item-"+suffix, "drift-repair-owner-"+suffix
	tmdbID, editedID := "drift-"+suffix, "edited-"+suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{contentID, owner})
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, status, tmdb_id)
		VALUES ($1, 'movie', 'Drift Repair', '{}'::text[], 'matched', $3),
			($2, 'movie', 'Provisional Owner', '{}'::text[], 'pending', $3)`,
		contentID, owner, tmdbID); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
		VALUES ($1, 'movie', 'tmdb', $2)`, owner, tmdbID); err != nil {
		t.Fatalf("seed durable id: %v", err)
	}

	// An admin's edit holds the owner while it moves the owner to another ID.
	edit, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin edit: %v", err)
	}
	defer edit.Rollback(ctx) //nolint:errcheck // a no-op once the edit commits
	if _, err := edit.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, owner); err != nil {
		t.Fatalf("lock owner: %v", err)
	}
	row := providerIDDriftRow{ContentID: contentID, ItemType: "movie", Status: "matched", TMDBID: tmdbID}
	repaired := make(chan error, 1)
	go func() {
		moved, err := NewProviderIDIntegrityRepairer(pool).moveProvisionalProviderClaim(ctx, owner, row, providerEntriesFromDrift(row))
		if err == nil && moved {
			err = errors.New("repair moved an ID the owner no longer holds")
		}
		repaired <- err
	}()
	waitForMediaItemLockWait(t, pool, repaired)
	if _, err := edit.Exec(ctx, `UPDATE media_items SET tmdb_id = $2 WHERE content_id = $1`, owner, editedID); err != nil {
		t.Fatalf("edit column: %v", err)
	}
	if _, err := edit.Exec(ctx, `
		UPDATE media_item_provider_ids SET provider_id = $2
		WHERE content_id = $1 AND provider = 'tmdb'`, owner, editedID); err != nil {
		t.Fatalf("edit durable id: %v", err)
	}
	if err := edit.Commit(ctx); err != nil {
		t.Fatalf("commit edit: %v", err)
	}
	if err := <-repaired; err != nil {
		t.Fatalf("repair: %v", err)
	}
	var durable string
	if err := pool.QueryRow(ctx, `
		SELECT provider_id FROM media_item_provider_ids
		WHERE content_id = $1 AND provider = 'tmdb'`, owner).Scan(&durable); err != nil {
		t.Fatalf("read owner durable id: %v", err)
	}
	if durable != editedID {
		t.Fatalf("owner durable tmdb = %q, want the edited %q", durable, editedID)
	}
}

// waitForMediaItemLockWait returns once another session of this database
// waits on a lock in a statement on the media_item tables, and fails if done
// reports first.
func waitForMediaItemLockWait(t *testing.T, pool *pgxpool.Pool, done <-chan error) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND datname = current_database()
			  AND pid <> pg_backend_pid() AND strpos(query, 'media_item') > 0)`).Scan(&waiting); err != nil {
			t.Fatalf("read lock waits: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("finished without waiting on a lock: %v", err)
		case <-deadline:
			t.Fatal("never waited on a lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

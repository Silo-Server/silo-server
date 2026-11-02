package backup

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Advisory lock classes of the account locks (userstore/pgstore).
const (
	preferenceLockClass = 0x53494c4f // "SILO"
	sectionLockClass    = 0x5345434f // "SECO"
)

// holdHistoryLock takes a profile's history lock in a transaction of its
// own, as a mark watched would; the returned func releases it.
func holdHistoryLock(t *testing.T, pool *pgxpool.Pool, userID int, profileID string) func() {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1::integer, hashtext('imported-history:' || $2))`, userID, profileID); err != nil {
		t.Fatal(err)
	}
	release := func() { _ = tx.Rollback(context.Background()) }
	t.Cleanup(release) // a failed test still lets the waiters finish
	return release
}

// blockedImporter waits until a backend holding the account's preference
// lock waits on a lock, and returns its pid. It fails the test at the
// deadline instead of hanging.
func blockedImporter(t *testing.T, pool *pgxpool.Pool, userID int) int {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		var pid int
		err := pool.QueryRow(t.Context(), `
			SELECT l.pid FROM pg_locks l
			WHERE l.locktype = 'advisory' AND l.granted AND l.classid = $1 AND l.objid = $2
			  AND EXISTS (SELECT 1 FROM pg_locks w WHERE w.pid = l.pid AND NOT w.granted)
			LIMIT 1`, preferenceLockClass, userID).Scan(&pid)
		if err == nil {
			return pid
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_locks, the observable state, between checks
	}
	t.Fatal("the import never waited holding the account's preference lock")
	return 0
}

// checkHoldsAccountLocks requires pid to hold both account locks and to wait
// on the profile's history lock: the account locks come first.
func checkHoldsAccountLocks(t *testing.T, pool *pgxpool.Pool, pid, userID int, profileID string) {
	t.Helper()
	var section, waitsOnHistory bool
	if err := pool.QueryRow(t.Context(), `
		SELECT
			EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND granted AND classid = $2 AND objid = $3),
			EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted AND classid = $3
				AND objid = (hashtext('imported-history:' || $4::text))::oid)`,
		pid, sectionLockClass, userID, profileID).Scan(&section, &waitsOnHistory); err != nil {
		t.Fatal(err)
	}
	if !section || !waitsOnHistory {
		t.Fatalf("import pid %d: holds the section-override lock %v, waits on the history lock %v; want both", pid, section, waitsOnHistory)
	}
}

func waitDone(t *testing.T, errs <-chan error) {
	t.Helper()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the import did not finish: deadlocked")
	}
}

// TestProfileImportLockOrderDB: a profile import takes the account locks
// (preferences, then section overrides) before a profile's history lock,
// the order a selective restore uses, so the two cannot deadlock on one
// account. It also locks watchlist titles before writing a library
// watchlist entry: while it waits on a title, another connection can still
// insert that entry, as watchlist promotion does under the title lock.
func TestProfileImportLockOrderDB(t *testing.T) {
	pool, _ := testDB(t)
	// A deadline bounds every wait, so a deadlock fails the test.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	sfx := randomHex(t, 4)
	lib := testLibrary(t, pool, "lock-"+sfx)
	movie := "bklk-" + sfx
	testMovie(t, pool, movie, "Lock "+sfx, "", lib)
	user := testAccount(t, pool, "lock-"+sfx, "h", nil, nil)
	src, dst := "lsrc-"+sfx, "ldst-"+sfx
	testProfile(t, pool, user, src, "Src", true, "")
	testProfile(t, pool, user, dst, "Dst", false, "")
	dbExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 1, 10, now())`, user, src, movie)
	dbExec(t, pool, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, user, src, movie)
	tmdb := 800000000 + int(time.Now().UnixNano()%1000000)
	var titleID int64
	if err := pool.QueryRow(ctx, `INSERT INTO watchlist_titles (id, media_type, tmdb_id, title) VALUES ($1, 'movie', $2, 'Soon') RETURNING id`, int64(tmdb), tmdb).Scan(&titleID); err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id) VALUES ($1, 'movie', 'tmdb', $2)`, titleID, strconv.Itoa(tmdb))
	dbExec(t, pool, `INSERT INTO user_watchlist_titles (user_id, profile_id, title_id) VALUES ($1, $2, $3)`, user, src, titleID)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE id = $1`, titleID) })
	transfer := NewProfileTransfer(pool, func(context.Context) (string, error) { return "lock-server-" + sfx, nil })
	data, err := transfer.Export(ctx, ProfileImportScope{UserID: user, ProfileID: src})
	if err != nil {
		t.Fatal(err)
	}
	run := func() <-chan error {
		errs := make(chan error, 1)
		go func() {
			_, err := transfer.Import(ctx, ProfileImportScope{UserID: user, ProfileID: dst}, data)
			errs <- err
		}()
		return errs
	}

	t.Run("account locks before the history lock", func(t *testing.T) {
		release := holdHistoryLock(t, pool, user, dst)
		errs := run()
		pid := blockedImporter(t, pool, user)
		checkHoldsAccountLocks(t, pool, pid, user, dst)
		release()
		waitDone(t, errs)
	})

	t.Run("titles before library watchlist entries", func(t *testing.T) {
		dbExec(t, pool, `DELETE FROM user_watchlist WHERE user_id = $1 AND profile_id = $2`, user, dst)
		dbExec(t, pool, `DELETE FROM user_watchlist_titles WHERE user_id = $1 AND profile_id = $2`, user, dst)
		holder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.Background()) }()
		if _, err := holder.Exec(ctx, `SELECT 1 FROM watchlist_titles WHERE id = $1 FOR UPDATE`, titleID); err != nil {
			t.Fatal(err)
		}
		errs := run()
		blockedImporter(t, pool, user)
		// The promotion's library write, on its own connection while the
		// title lock is held, must not wait on the import.
		promoter, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := promoter.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
			t.Fatal(err)
		}
		if _, err := promoter.Exec(ctx, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, user, dst, movie); err != nil {
			_ = promoter.Rollback(context.Background())
			_ = holder.Rollback(context.Background())
			<-errs
			t.Fatalf("the library write waited on the import: %v", err)
		}
		if err := promoter.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		_ = holder.Rollback(ctx)
		waitDone(t, errs)
	})
}

// TestRestoreLockOrderDB: a selective restore of an existing account takes
// the account locks before its profiles' history locks, the same order a
// profile import uses.
func TestRestoreLockOrderDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	sfx := randomHex(t, 4)
	account := testAccount(t, f.pool, "rlock-"+sfx, "h", nil, nil)
	profile := "rlock-" + sfx
	testProfile(t, f.pool, account, profile, "Main", true, "")
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	selection := []UserSelection{{Account: strconv.Itoa(account), Target: strconv.Itoa(account),
		Profiles: []ProfileSelection{{Profile: profile, Target: profile, DataTypes: []string{userdata.WatchHistory, userdata.Preferences}}}}}
	payload, _ := json.Marshal(RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Users: selection})
	release := holdHistoryLock(t, f.pool, account, profile)
	errs := make(chan error, 1)
	go func() {
		_, err := f.svc.ExecuteRestore(ctx, RestoreRun{Payload: payload, Fenced: (&testFence{pool: f.pool}).run})
		errs <- err
	}()
	pid := blockedImporter(t, f.pool, account)
	checkHoldsAccountLocks(t, f.pool, pid, account, profile)
	release()
	waitDone(t, errs)
}

// TestProfileImportAndDeleteLockOrderDB: deleting a profile takes the
// account locks before it locks or deletes any row, as an import does, so a
// deletion running beside an import of the same account waits on the
// account lock instead of holding collection rows the import needs.
func TestProfileImportAndDeleteLockOrderDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	sfx := randomHex(t, 4)
	user := testAccount(t, pool, "dlock-"+sfx, "h", nil, nil)
	src, dst, other := "dsrc-"+sfx, "ddst-"+sfx, "doth-"+sfx
	testProfile(t, pool, user, src, "Src", true, "")
	testProfile(t, pool, user, dst, "Dst", false, "")
	testProfile(t, pool, user, other, "Other", false, "")
	dbExec(t, pool, `INSERT INTO user_personal_collections (id, user_id, profile_id, creator_profile_id, name, native) VALUES ($1, $2, $3, $3, 'Mine', true)`,
		"dcol-"+sfx, user, other)
	transfer := NewProfileTransfer(pool, func(context.Context) (string, error) { return "dlock-server-" + sfx, nil })
	data, err := transfer.Export(ctx, ProfileImportScope{UserID: user, ProfileID: src})
	if err != nil {
		t.Fatal(err)
	}
	release := holdHistoryLock(t, pool, user, dst)
	imported := make(chan error, 1)
	go func() {
		_, err := transfer.Import(ctx, ProfileImportScope{UserID: user, ProfileID: dst}, data)
		imported <- err
	}()
	importer := blockedImporter(t, pool, user)
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- store.DeleteProfile(ctx, other) }()
	var deleter int
	for deadline := time.Now().Add(20 * time.Second); deleter == 0; {
		err := pool.QueryRow(ctx, `
			SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = $1 AND objid = $2 AND pid <> $3 LIMIT 1`,
			preferenceLockClass, user, importer).Scan(&deleter)
		if err != nil && time.Now().After(deadline) {
			t.Fatal("the deletion never waited on the account lock")
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_locks, the observable state, between checks
	}
	var holdsCollections bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND granted AND relation = 'user_personal_collections'::regclass)`,
		deleter).Scan(&holdsCollections); err != nil {
		t.Fatal(err)
	}
	if holdsCollections {
		t.Fatal("the deletion locked collection rows before the account lock")
	}
	release()
	waitDone(t, imported)
	waitDone(t, deleted)
	if n := countRows(t, pool, `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND id = $2`, user, other); n != 0 {
		t.Fatalf("the profile was not deleted: %d", n)
	}
}

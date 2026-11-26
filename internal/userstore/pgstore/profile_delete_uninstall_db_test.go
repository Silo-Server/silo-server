package pgstore

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileUninstallFixture is an account with a profile "p" that two retained
// device sign-in requests were approved for, through the account's identity
// at a plugin installation. Deleting the profile clears both requests through
// the approved_profile foreign key; uninstalling the plugin deletes the
// identity and clears both through approved_identity_id.
//
// The two cascades are made to reach the requests in opposite orders. The
// first request was updated in place (a HOT update), so an index scan meets
// it at its old position, before the second, and a sequential scan at its
// new one, after the second. The profile delete runs on connections that
// plan index scans; the uninstall's lookup has no index and scans the table.
// A trigger holds each cascade, after it has locked the first request it
// reaches, on an advisory lock the test holds: the profile delete at the
// first request, the uninstall at the second.
type profileUninstallFixture struct {
	pool, indexPool *pgxpool.Pool
	userID          int
	installationID  int64
	identityID      int64
	requests        [2]string
	holdKeys        [2]int64
}

func newProfileUninstallFixture(t *testing.T) *profileUninstallFixture {
	t.Helper()
	pool := testPool(t)
	ctx := t.Context()
	cfg := pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["enable_seqscan"] = "off"
	cfg.ConnConfig.RuntimeParams["enable_bitmapscan"] = "off"
	indexPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(indexPool.Close)
	f := &profileUninstallFixture{pool: pool, indexPool: indexPool, userID: seedTestUser(t, pool, "profile-uninstall")}
	store := newStore(pool, f.userID)
	for _, id := range []string{"main", "p"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// A profile set removes only a profile it once listed.
	if _, err := pool.Exec(ctx, `UPDATE user_profiles SET sync_id = $2, sync_position = 1 WHERE user_id = $1 AND id = 'p'`,
		f.userID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	name := "profile-uninstall-" + uuid.NewString()
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '1.0.0', $1) RETURNING id`, name).Scan(&f.installationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE id = $1`, f.installationID)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, f.installationID, name, f.userID).Scan(&f.identityID); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash,
				match_code, device_name, status, client_purpose, temporary, approved_by_user_id, approved_profile_id,
				approved_identity_id, approved_at, expires_at)
			VALUES ($1, $2, $3, $4, '00', $5, 'approved', 'remote_playback', TRUE, $6, 'p', $7, NOW(), NOW() + interval '10 minutes')`,
			id, uuid.NewString(), uuid.NewString(), uuid.NewString(), name, f.userID, f.identityID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM device_login_requests WHERE id = ANY($1)`, ids)
	})
	// requests[0] is the one earlier in the table.
	var firstEarlier bool
	if err := pool.QueryRow(ctx, `SELECT (SELECT ctid FROM device_login_requests WHERE id = $1) <
		(SELECT ctid FROM device_login_requests WHERE id = $2)`, ids[0], ids[1]).Scan(&firstEarlier); err != nil {
		t.Fatal(err)
	}
	if !firstEarlier {
		ids[0], ids[1] = ids[1], ids[0]
	}
	f.requests = [2]string{ids[0], ids[1]}
	var page, newPage string
	if err := pool.QueryRow(ctx, `SELECT split_part(ltrim(ctid::text, '('), ',', 1) FROM device_login_requests WHERE id = $1`,
		f.requests[0]).Scan(&page); err != nil {
		t.Fatal(err)
	}
	// device_name is in no index, so this update stays on the page (HOT)
	// when the page has room, and index entries keep the old position. It
	// can land in a slot freed earlier on the page; it is repeated until it
	// lands past the second request.
	var movedPastSecond bool
	for range 50 {
		if err := pool.QueryRow(ctx, `UPDATE device_login_requests SET device_name = device_name || '.' WHERE id = $1
			RETURNING split_part(ltrim(ctid::text, '('), ',', 1),
				ctid > (SELECT ctid FROM device_login_requests WHERE id = $2)`,
			f.requests[0], f.requests[1]).Scan(&newPage, &movedPastSecond); err != nil {
			t.Fatal(err)
		}
		if newPage != page || movedPastSecond {
			break
		}
	}
	if newPage != page || !movedPastSecond {
		t.Fatalf("could not stage the scan orders: the first request moved from page %s to %s, past the second: %v", page, newPage, movedPastSecond)
	}

	f.holdKeys = [2]int64{int64(uuid.New().ID()), int64(uuid.New().ID())}
	fn := "test_hold_request_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.id = '`+f.requests[0]+`' AND OLD.approved_profile_id IS NOT NULL AND NEW.approved_profile_id IS NULL THEN
				PERFORM pg_advisory_xact_lock(`+strconv.FormatInt(f.holdKeys[0], 10)+`);
			ELSIF OLD.id = '`+f.requests[1]+`' AND OLD.approved_identity_id IS NOT NULL AND NEW.approved_identity_id IS NULL THEN
				PERFORM pg_advisory_xact_lock(`+strconv.FormatInt(f.holdKeys[1], 10)+`);
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER `+fn+` BEFORE UPDATE ON device_login_requests FOR EACH ROW
			WHEN (OLD.device_name LIKE '`+name+`%') EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DROP TRIGGER IF EXISTS `+fn+` ON device_login_requests; DROP FUNCTION IF EXISTS `+fn+`()`)
	})
	return f
}

// hold takes both advisory locks in a transaction of its own and returns it
// with its backend pid.
func (f *profileUninstallFixture) hold(ctx context.Context, t *testing.T) (pgx.Tx, int) {
	t.Helper()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock($1), pg_advisory_xact_lock($2)) l`,
		f.holdKeys[0], f.holdKeys[1]).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// uninstall starts the plugin uninstall's delete on its own connection and
// leaves its transaction open.
func (f *profileUninstallFixture) uninstall(ctx context.Context, t *testing.T) (pgx.Tx, int, <-chan error) {
	t.Helper()
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	var pid int
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	done := make(chan error, 1)
	go func() {
		_, err := tx.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, f.installationID)
		done <- err
	}()
	return tx, pid, done
}

// waiting waits until a backend other than the excluded ones waits on a lock
// one of blockers holds, and returns its pid and the pids blocking it.
func (f *profileUninstallFixture) waiting(ctx context.Context, t *testing.T, blockers []int, exclude ...int) (int, []int) {
	t.Helper()
	for {
		var pid int
		var by []int
		err := f.pool.QueryRow(ctx, `SELECT pid, pg_blocking_pids(pid) FROM pg_stat_activity
			WHERE datname = current_database() AND pg_blocking_pids(pid) && $1::int[] AND NOT pid = ANY($2::int[])
			LIMIT 1`, blockers, append([]int{0}, exclude...)).Scan(&pid, &by)
		if err == nil {
			return pid, by
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for a backend blocked by %v: %v", blockers, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (f *profileUninstallFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.WithoutCancel(t.Context()), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func await(ctx context.Context, t *testing.T, what string, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatalf("%s did not finish: %v", what, ctx.Err())
		return nil
	}
}

// deadlocked names the statement Postgres aborted, if it aborted one.
func deadlocked(what string, err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
		return what + " was aborted: " + pgErr.Message + "; " + pgErr.Detail + " (" + pgErr.Where + ")"
	}
	return ""
}

// A profile delete and a plugin uninstall of the identity the profile's
// device sign-ins were approved through both clear those requests. Each
// holds the first request it reached when the other reaches it: before the
// profile delete took the account's identities first, Postgres aborted one
// of them as a deadlock. Now the uninstall waits for the profile delete at
// the identity, or the profile delete waits for the uninstall there, and
// both finish. Runs once through each caller of deleteProfileRowsTx, with
// either one first.
func TestProfileDeleteDuringPluginUninstallDB(t *testing.T) {
	deletes := map[string]func(context.Context, *profileUninstallFixture) error{
		"profile delete": func(ctx context.Context, f *profileUninstallFixture) error {
			_, err := newStore(f.indexPool, f.userID).DeleteProfileAudited(ctx, "p",
				func(pgx.Tx, *userstore.Profile) error { return nil })
			return err
		},
		"profile set": func(ctx context.Context, f *profileUninstallFixture) error {
			return newStore(f.indexPool, f.userID).WithProfileSetTransaction(ctx, nil, func(w userstore.ProfileSetWriter) error {
				if removable := w.Removable(); !slices.Equal(removable, []string{"p"}) {
					return errors.New("the set does not remove p: " + strings.Join(removable, ","))
				}
				_, err := w.DeleteProfile(ctx, "p")
				return err
			})
		},
	}
	for _, name := range []string{"profile delete", "profile set"} {
		for _, uninstallFirst := range []bool{false, true} {
			first := name + " first"
			if uninstallFirst {
				first = "uninstall first"
			}
			t.Run(name+"/"+first, func(t *testing.T) {
				f := newProfileUninstallFixture(t)
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				holder, holderPID := f.hold(ctx, t)
				deleted := make(chan error, 1)
				var uninstallTx pgx.Tx
				var uninstallPID, deletePID int
				var uninstalled <-chan error
				var uninstallWaitsOn, deleteWaitsOn []int
				if uninstallFirst {
					uninstallTx, uninstallPID, uninstalled = f.uninstall(ctx, t)
					f.waiting(ctx, t, []int{holderPID})
					go func() { deleted <- deletes[name](ctx, f) }()
					deletePID, deleteWaitsOn = f.waiting(ctx, t, []int{holderPID, uninstallPID}, uninstallPID)
				} else {
					go func() { deleted <- deletes[name](ctx, f) }()
					deletePID, _ = f.waiting(ctx, t, []int{holderPID})
					uninstallTx, uninstallPID, uninstalled = f.uninstall(ctx, t)
					_, uninstallWaitsOn = f.waiting(ctx, t, []int{holderPID, deletePID}, deletePID)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				uninstallErr := await(ctx, t, "uninstall", uninstalled)
				if uninstallErr == nil {
					uninstallErr = uninstallTx.Commit(ctx)
				} else {
					_ = uninstallTx.Rollback(ctx)
				}
				deleteErr := await(ctx, t, name, deleted)
				for _, aborted := range []string{deadlocked("the uninstall", uninstallErr), deadlocked("the "+name, deleteErr)} {
					if aborted != "" {
						t.Errorf("deadlock: %s", aborted)
					}
				}
				if uninstallErr != nil || deleteErr != nil {
					t.Fatalf("uninstall = %v, %s = %v", uninstallErr, name, deleteErr)
				}
				// Whichever went second waited for the other at the
				// identity, before it reached a request.
				if uninstallFirst && !slices.Equal(deleteWaitsOn, []int{uninstallPID}) {
					t.Errorf("the %s waited on %v, want the uninstall (%d)", name, deleteWaitsOn, uninstallPID)
				}
				if !uninstallFirst && !slices.Equal(uninstallWaitsOn, []int{deletePID}) {
					t.Errorf("the uninstall waited on %v, want the %s (%d)", uninstallWaitsOn, name, deletePID)
				}
				if n := f.count(t, `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND id = 'p'`, f.userID) +
					f.count(t, `SELECT count(*) FROM plugin_auth_identities WHERE id = $1`, f.identityID) +
					f.count(t, `SELECT count(*) FROM device_login_requests WHERE id = ANY($1)
						AND (approved_profile_id IS NOT NULL OR approved_identity_id IS NOT NULL)`, f.requests[:]); n != 0 {
					t.Fatalf("%d of the profile, the identity and the requests' approvals remain", n)
				}
			})
		}
	}
}

package auth

import (
	"context"
	"errors"
	"os"
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
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// profileDeleteFixture is an account with a profile "p" that two retained
// device sign-in requests were approved for, through the account's identity
// at a plugin installation, and an administrator. Deleting the profile clears
// both requests through the approved_profile foreign key. A plugin uninstall
// clears both through approved_identity_id after deleting the identity; an
// administrator's sign-out everywhere withdraws both.
//
// The profile delete and its opponent are made to reach the requests in
// opposite orders. The first request was updated in place (a HOT update), so
// an index scan meets it at its old position, before the second, and a
// sequential scan at its new one, after the second. The profile delete runs
// on connections that plan index scans, its opponent on ones that scan the
// table. A trigger holds each, after it has locked the first request it
// reaches, on an advisory lock the test holds: the profile delete at the
// first request, the opponent at the second.
type profileDeleteFixture struct {
	pool, indexPool, scanPool *pgxpool.Pool
	userID, adminID           int
	installationID            int64
	identityID                int64
	requests                  [2]string
	holdKeys                  [2]int64
}

// poolWith is a pool on the test database whose connections set params.
func poolWith(t *testing.T, dsn string, params map[string]string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range params {
		cfg.ConnConfig.RuntimeParams[k] = v
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newProfileDeleteFixture(t *testing.T) *profileDeleteFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	f := &profileDeleteFixture{
		pool:      poolWith(t, dsn, nil),
		indexPool: poolWith(t, dsn, map[string]string{"enable_seqscan": "off", "enable_bitmapscan": "off"}),
		scanPool:  poolWith(t, dsn, map[string]string{"enable_indexscan": "off", "enable_bitmapscan": "off", "enable_indexonlyscan": "off"}),
	}
	pool := f.pool
	name := "profile-delete-" + uuid.NewString()
	t.Cleanup(func() {
		ctx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(ctx, `DELETE FROM device_login_requests WHERE device_name LIKE $1 || '%'`, name)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int{f.userID, f.adminID})
		_, _ = pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, f.installationID)
	})
	for _, u := range []struct {
		id   *int
		role string
	}{{&f.userID, "user"}, {&f.adminID, "admin"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
			VALUES ($1, $2, 'x', $3, true) RETURNING id`, name+u.role, name+u.role+"@example.test", u.role).Scan(u.id); err != nil {
			t.Fatal(err)
		}
	}
	store := f.store(t, pool)
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
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '1.0.0', $1) RETURNING id`, name).Scan(&f.installationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, f.installationID, name, f.userID).Scan(&f.identityID); err != nil {
		t.Fatal(err)
	}
	// device_name is in no index, so updating it keeps the row on its page
	// (HOT) when the page has room, and index entries keep the old position.
	// The update can land in a slot freed earlier on the page, so it is
	// repeated until it lands past the second request. A pair on a page with
	// no room is deleted and inserted again.
	staged := false
	for attempt := 0; attempt < 10 && !staged; attempt++ {
		var ids []string
		for range 2 {
			id := uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash,
					match_code, device_name, status, client_purpose, temporary, approved_by_user_id, approved_profile_id,
					approved_identity_id, approved_at, expires_at)
				VALUES ($1, $2, $3, $4, '00', $5, $6, 'remote_playback', TRUE, $7, 'p', $8, NOW(), NOW() + interval '10 minutes')`,
				id, uuid.NewString(), uuid.NewString(), uuid.NewString(), name, DeviceLoginStatusApproved, f.userID, f.identityID); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
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
		if staged = newPage == page && movedPastSecond; !staged {
			if _, err := pool.Exec(ctx, `DELETE FROM device_login_requests WHERE id = ANY($1)`, ids); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !staged {
		t.Fatal("could not stage the scan orders: no pair of requests shared a page with room for the update")
	}

	// The profile delete is held at the first request as it clears the
	// profile; the uninstall at the second as it clears the identity, and a
	// sign-out everywhere at the second as it withdraws the approval.
	f.holdKeys = [2]int64{int64(uuid.New().ID()), int64(uuid.New().ID())}
	fn := "test_hold_request_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.id = '`+f.requests[0]+`' AND OLD.approved_profile_id IS NOT NULL AND NEW.approved_profile_id IS NULL THEN
				PERFORM pg_advisory_xact_lock(`+strconv.FormatInt(f.holdKeys[0], 10)+`);
			ELSIF OLD.id = '`+f.requests[1]+`' AND ((OLD.approved_identity_id IS NOT NULL AND NEW.approved_identity_id IS NULL)
					OR (OLD.status = 'approved' AND NEW.status = 'denied')) THEN
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

// store is the account's profile store over pool.
func (f *profileDeleteFixture) store(t *testing.T, pool *pgxpool.Pool) userstore.UserStore {
	t.Helper()
	store, err := pgstore.NewPostgresProvider(pool).ForUser(t.Context(), f.userID)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// hold takes both advisory locks in a transaction of its own and returns it
// with its backend pid.
func (f *profileDeleteFixture) hold(ctx context.Context, t *testing.T) (pgx.Tx, int) {
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

// opponent starts, on a connection of the scanning pool, the transaction a
// profile delete races, and returns its backend pid, a channel that receives
// the opponent's first error or nil once it reached the point where the test
// decides its fate, and a function that finishes it.
type opponent func(ctx context.Context, t *testing.T, f *profileDeleteFixture) (pid int, done <-chan error, finish func() error)

// uninstall deletes the identity's installation and leaves its transaction
// open, so a test sees whether the profile delete waited for it.
func uninstallOpponent(ctx context.Context, t *testing.T, f *profileDeleteFixture) (int, <-chan error, func() error) {
	t.Helper()
	conn, err := f.scanPool.Acquire(ctx)
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
	return pid, done, func() error { return tx.Commit(ctx) }
}

// signOutOpponent is an administrator signing the account out everywhere,
// which withdraws its device sign-in approvals. It runs in one call, so its
// pid is found by waiting for it.
func signOutOpponent(ctx context.Context, t *testing.T, f *profileDeleteFixture) (int, <-chan error, func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := NewSessionRepository(f.scanPool).RevokeAsAdmin(ctx, f.adminID, f.userID, nil)
		done <- err
	}()
	return 0, done, func() error { return nil }
}

// waiting waits until a backend other than the excluded ones waits on a lock
// one of blockers holds, and returns its pid and the pids blocking it.
func (f *profileDeleteFixture) waiting(ctx context.Context, t *testing.T, blockers []int, exclude ...int) (int, []int) {
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

func awaitErr(ctx context.Context, t *testing.T, what string, done <-chan error) error {
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

// A profile delete clears the device sign-in requests approved for the
// profile. A plugin uninstall of the identity they were approved through
// clears the same requests, and so does signing the account out everywhere.
// Each is held after it has locked the first request it reached, the two in
// opposite orders. Before a profile delete locked the account row and then
// its identities, Postgres aborted one of the pair as a deadlock. Now the
// second waits for the first before it reaches a request (at the identity
// for the uninstall, at the account row for the sign-out) and both finish.
// Runs through each caller of deleteProfileRowsTx, with either one first.
func TestProfileDeleteLockOrderDB(t *testing.T) {
	deletes := map[string]func(context.Context, *testing.T, *profileDeleteFixture) error{
		"profile delete": func(ctx context.Context, t *testing.T, f *profileDeleteFixture) error {
			_, err := f.store(t, f.indexPool).(userstore.AuditedProfileDeleter).DeleteProfileAudited(ctx, "p",
				func(pgx.Tx, *userstore.Profile) error { return nil })
			return err
		},
		"profile set": func(ctx context.Context, t *testing.T, f *profileDeleteFixture) error {
			sets := f.store(t, f.indexPool).(userstore.ProfileSetStore)
			return sets.WithProfileSetTransaction(ctx, nil, func(w userstore.ProfileSetWriter) error {
				if removable := w.Removable(); !slices.Equal(removable, []string{"p"}) {
					return errors.New("the set does not remove p: " + strings.Join(removable, ","))
				}
				_, err := w.DeleteProfile(ctx, "p")
				return err
			})
		},
	}
	opponents := map[string]opponent{"uninstall": uninstallOpponent, "sign-out everywhere": signOutOpponent}
	for _, name := range []string{"profile delete", "profile set"} {
		for _, against := range []string{"uninstall", "sign-out everywhere"} {
			for _, opponentFirst := range []bool{false, true} {
				first := name + " first"
				if opponentFirst {
					first = against + " first"
				}
				t.Run(name+"/"+against+"/"+first, func(t *testing.T) {
					f := newProfileDeleteFixture(t)
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					holder, holderPID := f.hold(ctx, t)
					deleted := make(chan error, 1)
					var opponentPID, deletePID int
					var opponentDone <-chan error
					var finish func() error
					var opponentWaitsOn, deleteWaitsOn []int
					if opponentFirst {
						opponentPID, opponentDone, finish = opponents[against](ctx, t, f)
						pid, _ := f.waiting(ctx, t, []int{holderPID})
						if opponentPID == 0 {
							opponentPID = pid
						}
						go func() { deleted <- deletes[name](ctx, t, f) }()
						deletePID, deleteWaitsOn = f.waiting(ctx, t, []int{holderPID, opponentPID}, opponentPID)
					} else {
						go func() { deleted <- deletes[name](ctx, t, f) }()
						deletePID, _ = f.waiting(ctx, t, []int{holderPID})
						opponentPID, opponentDone, finish = opponents[against](ctx, t, f)
						var pid int
						pid, opponentWaitsOn = f.waiting(ctx, t, []int{holderPID, deletePID}, deletePID)
						if opponentPID == 0 {
							opponentPID = pid
						}
					}
					if err := holder.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					// An open uninstall commits once its statement ends; the
					// sign-out commits itself.
					opponentErr := awaitErr(ctx, t, against, opponentDone)
					if opponentErr == nil {
						opponentErr = finish()
					}
					deleteErr := awaitErr(ctx, t, name, deleted)
					for _, aborted := range []string{deadlocked("the "+against, opponentErr), deadlocked("the "+name, deleteErr)} {
						if aborted != "" {
							t.Errorf("deadlock: %s", aborted)
						}
					}
					if opponentErr != nil || deleteErr != nil {
						t.Fatalf("%s = %v, %s = %v", against, opponentErr, name, deleteErr)
					}
					// Whichever went second waited for the other alone,
					// before it reached a request.
					if opponentFirst && !slices.Equal(deleteWaitsOn, []int{opponentPID}) {
						t.Errorf("the %s waited on %v, want the %s (%d)", name, deleteWaitsOn, against, opponentPID)
					}
					if !opponentFirst && !slices.Equal(opponentWaitsOn, []int{deletePID}) {
						t.Errorf("the %s waited on %v, want the %s (%d)", against, opponentWaitsOn, name, deletePID)
					}
					left := 0
					for _, q := range []string{
						`SELECT count(*) FROM user_profiles WHERE user_id = $1 AND id = 'p' AND $2::text[] IS NOT NULL`,
						`SELECT count(*) FROM device_login_requests WHERE id = ANY($2) AND approved_profile_id IS NOT NULL AND $1::int > 0`,
					} {
						var n int
						if err := f.pool.QueryRow(context.WithoutCancel(ctx), q, f.userID, f.requests[:]).Scan(&n); err != nil {
							t.Fatal(err)
						}
						left += n
					}
					var opponentLeft int
					// A request the profile delete cleared first no longer
					// names the account, so the sign-out leaves it alone.
					q := `SELECT count(*) FROM device_login_requests WHERE id = ANY($1) AND status = 'approved'
						AND approved_by_user_id IS NOT NULL AND $2::bigint > 0`
					if against == "uninstall" {
						q = `SELECT (SELECT count(*) FROM plugin_auth_identities WHERE id = $2) +
							(SELECT count(*) FROM device_login_requests WHERE id = ANY($1) AND approved_identity_id IS NOT NULL)`
					}
					if err := f.pool.QueryRow(context.WithoutCancel(ctx), q, f.requests[:], f.identityID).Scan(&opponentLeft); err != nil {
						t.Fatal(err)
					}
					if left+opponentLeft != 0 {
						t.Fatalf("%d of the profile and its approvals, and %d of what the %s removes, remain", left, opponentLeft, against)
					}
				})
			}
		}
	}
}

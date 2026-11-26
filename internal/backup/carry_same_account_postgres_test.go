package backup

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/Silo-Server/silo-server/migrations"
)

const sameAccountDeployment = "same-account-restore-test"

// sameAccountServer is one deployment the test backs up and restores in
// full: its live database, where every backup lands, and the keys both use.
type sameAccountServer struct {
	url      string
	tools    Tools
	store    blobstore.Store
	root     string
	validate ValidateOptions
	// lastRestore is when the last restore ran. A swap names its databases
	// by the second, so each restore runs at least a second after the last.
	lastRestore *time.Time
}

// newSameAccountServer creates a deployment with a migrated database of its
// own and an empty backup destination.
func newSameAccountServer(t *testing.T, tools Tools) sameAccountServer {
	t.Helper()
	master := []byte("same-account-restore-test-key-0123456789abcdef")
	keyCheck, err := secret.KeyCheckValue(master)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := database.MigrationVersions(migrations.FS, "sql")
	if err != nil {
		t.Fatal(err)
	}
	live, liveURL := backuptest.Database(t)
	dbExec(t, live, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, serveridentity.Key, sameAccountDeployment)
	live.Close()
	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	return sameAccountServer{url: liveURL, tools: tools, store: store, root: root, lastRestore: new(time.Time),
		validate: ValidateOptions{KeyCheck: keyCheck, ManifestKey: manifestKeyOf(t, master), BinaryMigrations: versions}}
}

// backup takes a full backup of the live database and opens it.
func (s sameAccountServer) backup(t *testing.T) *Source {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), s.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := New(Options{
		Pool: pool, DatabaseURL: s.url, Destination: NewStoreDestination(s.store),
		Locker: PostgresLocker{Pool: pool}, KeyCheck: s.validate.KeyCheck, ManifestKey: s.validate.ManifestKey,
		ServerID:  func(context.Context) (string, error) { return sameAccountDeployment, nil },
		FindTools: func(context.Context) (Tools, error) { return s.tools, nil },
	})
	result, err := backupUnderLock(t, svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	return openTestSource(t, filepath.Join(s.root, "backups", sameAccountDeployment, result.ID))
}

// restore restores src in full over the live database, carrying the live
// server's credentials, and returns what it printed.
func (s sameAccountServer) restore(t *testing.T, src *Source) string {
	t.Helper()
	ctx := t.Context()
	ctl, err := ConnectControl(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctl.Close(ctx) }()
	var out bytes.Buffer
	var previous string
	err = RestoreSwap(ctx, src, PGLoader{Tools: s.tools}, ctl, SwapOptions{
		LiveURL: s.url, DatabaseOnly: true, Validate: s.validate, WorkDir: t.TempDir(), JournalKey: s.validate.ManifestKey, Out: &out,
		Journaled: func(stamp string) { previous = "silo_before_restore_" + stamp },
		Now: func() time.Time {
			next := s.lastRestore.Add(time.Second)
			if now := time.Now(); now.After(next) {
				next = now
			}
			*s.lastRestore = next
			return next
		},
	})
	if err != nil {
		t.Fatalf("full restore: %v\n%s", err, out.String())
	}
	// The swap keeps the replaced database under a name of the second it
	// ran; the test is done with it.
	if err := ctl.Drop(ctx, previous); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// with runs fn on a pool over the live database, closed again before the
// next backup or restore, which refuse other clients.
func (s sameAccountServer) with(t *testing.T, fn func(*pgxpool.Pool)) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), s.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fn(pool)
}

func createAccount(t *testing.T, pool *pgxpool.Pool, username string) int {
	t.Helper()
	user, err := auth.NewUserRepository(pool).Create(t.Context(), models.CreateUserInput{
		Username: username, Email: username + "@example.test", Password: "password-" + username, Role: models.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

// TestFullRestoreCarriesCredentialsOnlyOntoTheSameAccountDB: a full restore
// carries a live account's credentials (an API key, an external sign-in
// link) and its stricter switches only onto the same account in the
// restored data, never onto another account that holds the same id there.
// An account id can be held by two accounts once a restore has moved the id
// sequence back: one in a backup, another created on the live server since.
func TestFullRestoreCarriesCredentialsOnlyOntoTheSameAccountDB(t *testing.T) {
	tools := testTools(t)
	for _, tc := range []struct {
		name string
		// profile gives the backup's account a profile.
		profile bool
		// disabled disables the live account, which takes the backup
		// account's name.
		disabled bool
		// deleteNewest deletes the newest live account before the restore.
		deleteNewest bool
	}{
		{name: "account never used in the backup"},
		{name: "account with a profile in the backup", profile: true},
		{name: "live account disabled under the same name", disabled: true},
		{name: "newest live account deleted", deleteNewest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			server := newSameAccountServer(t, tools)
			var installation int64
			server.with(t, func(pool *pgxpool.Pool) {
				if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path)
					VALUES ('silo.auth.oidc', '1.0.0', '/plugins/oidc') RETURNING id`).Scan(&installation); err != nil {
					t.Fatal(err)
				}
			})

			before := server.backup(t)
			var first int
			var created time.Time
			server.with(t, func(pool *pgxpool.Pool) {
				first = createAccount(t, pool, "first")
				if err := pool.QueryRow(ctx, `SELECT created_at FROM users WHERE id = $1`, first).Scan(&created); err != nil {
					t.Fatal(err)
				}
				if tc.profile {
					dbExec(t, pool, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ('first-profile', $1, 'First', true)`, first)
				}
			})
			between := server.backup(t)
			server.restore(t, before)

			var second, third int
			server.with(t, func(pool *pgxpool.Pool) {
				// The restore moves the account id sequence past the ids the
				// live server used. A server restored before it did can
				// already hold such ids, so the sequence is put back as
				// such a restore left it.
				var next int64
				if err := pool.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('users', 'id'))`).Scan(&next); err != nil || next <= int64(first) {
					t.Fatalf("the next account id after the restore is %d (%v), want above %d, the live server's last", next, err, first)
				}
				dbExec(t, pool, `SELECT setval(pg_get_serial_sequence('users', 'id'), $1, false)`, first)
				name := "second"
				if tc.disabled {
					name = "first"
				}
				second = createAccount(t, pool, name)
				if second != first {
					t.Fatalf("the account created after restoring the older backup has id %d, want %d (the id the lost account had)", second, first)
				}
				third = createAccount(t, pool, "third")
				dbExec(t, pool, `INSERT INTO api_keys (user_id, label, key_hash) VALUES ($1, 'second key', encode(sha256('second-key'), 'hex'))`, second)
				dbExec(t, pool, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id, username)
					VALUES ($1, 'second-subject', $2, 'second')`, installation, second)
				if tc.disabled {
					dbExec(t, pool, `UPDATE users SET enabled = false WHERE id = $1`, second)
				}
				if tc.deleteNewest {
					dbExec(t, pool, `DELETE FROM users WHERE id = $1`, third)
				}
			})
			out := server.restore(t, between)

			server.with(t, func(pool *pgxpool.Pool) { checkSameAccountRestored(t, pool, first, third, created, out) })
		})
	}
}

// checkSameAccountRestored: the restored account first is the backup's, as
// it was created, without the live credentials or switches of the other
// account that held its id, and the next account takes an id above third,
// the live server's last.
func checkSameAccountRestored(t *testing.T, pool *pgxpool.Pool, first, third int, created time.Time, out string) {
	t.Helper()
	ctx := t.Context()
	var username string
	var enabled bool
	var keys, links int
	var restoredCreated time.Time
	if err := pool.QueryRow(ctx, `SELECT username, enabled, created_at,
		(SELECT count(*) FROM api_keys WHERE user_id = $1),
		(SELECT count(*) FROM plugin_auth_identities WHERE user_id = $1)
		FROM users WHERE id = $1`, first).Scan(&username, &enabled, &restoredCreated, &keys, &links); err != nil {
		t.Fatal(err)
	}
	if username != "first" || !restoredCreated.Equal(created) {
		t.Errorf("restored account %d is %q created %s, want the backup's account, created %s", first, username, restoredCreated, created)
	}
	if keys != 0 || links != 0 {
		t.Errorf("the backup's account %d holds %d API keys and %d sign-in links of another live account\n%s", first, keys, links, out)
	}
	if !enabled {
		t.Errorf("the backup's account %d was disabled by another live account's switch\n%s", first, out)
	}
	if dropped := fmt.Sprintf("'second key' (account %d)", first); !strings.Contains(out, dropped) {
		t.Errorf("the restore does not report the dropped key %s\n%s", dropped, out)
	}
	// The next account must not take an id the live server used.
	if next := createAccount(t, pool, "next"); next <= third {
		t.Errorf("the next account after the restore has id %d, want above %d, the live server's last", next, third)
	}
}

// TestFullRestoreKeepsTheLiveAccountIDSequenceDB: after a full restore the
// next account takes an id above every one the live server's sequence handed
// out, also when no account that held them is left there.
func TestFullRestoreKeepsTheLiveAccountIDSequenceDB(t *testing.T) {
	tools := testTools(t)
	for _, tc := range []struct {
		name string
		// again restores the same backup a second time before checking.
		again bool
	}{
		{name: "every live account deleted"},
		{name: "the same backup restored twice", again: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newSameAccountServer(t, tools)
			empty := server.backup(t)
			var used int
			server.with(t, func(pool *pgxpool.Pool) {
				used = createAccount(t, pool, "gone")
				if !tc.again {
					dbExec(t, pool, `DELETE FROM users WHERE id = $1`, used)
				}
			})
			server.restore(t, empty)
			if tc.again {
				server.restore(t, empty)
			}
			server.with(t, func(pool *pgxpool.Pool) {
				if next := createAccount(t, pool, "next"); next <= used {
					t.Errorf("the next account after the restore has id %d, want above %d, which the live server used", next, used)
				}
			})
		})
	}
}

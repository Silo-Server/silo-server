package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/appkeys/appkeystest"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/Silo-Server/silo-server/internal/serversettings"
)

// keyBackupServerID is the server identity of the server the key tests back
// up.
const keyBackupServerID = "5b0f3c4e-7d21-4a8e-9c3b-0e6f1a2d4c5b"

// keyHolder is an account with an app person key and an active device key
// that key vouched for.
type keyHolder struct {
	userID             int64
	person, device     appkeystest.Key
	personID, deviceID string
}

// seedKeyHolder adds an account with role and no password, and its keys.
func seedKeyHolder(t *testing.T, pool *pgxpool.Pool, name, role string) keyHolder {
	t.Helper()
	h := keyHolder{person: appkeystest.NewKey(t), device: appkeystest.NewKey(t)}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (role, username) VALUES ($1, $2) RETURNING id`, role, name).Scan(&h.userID); err != nil {
		t.Fatal(err)
	}
	h.personID = insertPersonKey(t, pool, h.userID, h.person)
	h.deviceID = insertDeviceKey(t, pool, h.userID, h.device, deviceKeyRow{personID: h.personID})
	return h
}

func insertPersonKey(t *testing.T, pool *pgxpool.Pool, userID int64, key appkeystest.Key) string {
	t.Helper()
	id := uuid.NewString()
	dbExec(t, pool, `INSERT INTO auth_person_keys (id, user_id, public_key, key_id, enrolled_via) VALUES ($1, $2, $3, $4, 'session')`,
		id, userID, key.Public().Bytes(), key.Public().KeyID())
	return id
}

// deviceKeyRow is how insertDeviceKey stores a device key: vouched for by
// personID (or none), with status (active when empty), and bound to the
// provider identity identityID when set.
type deviceKeyRow struct {
	personID, status string
	identityID       *int64
}

func insertDeviceKey(t *testing.T, pool *pgxpool.Pool, userID int64, key appkeystest.Key, row deviceKeyRow) string {
	t.Helper()
	id := uuid.NewString()
	if row.status == "" {
		row.status = "active"
	}
	dbExec(t, pool, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, status, enrolled_via, person_key_id, identity_id, provider_bound)
		VALUES ($1, $2, $3, $4, 'device', 'Phone', $5, 'introduce', NULLIF($6, '')::uuid, $7, $7::bigint IS NOT NULL)`,
		id, userID, key.Public().Bytes(), key.Public().KeyID(), row.status, row.personID, row.identityID)
	return id
}

// serverIDOf reads a database's server identity the way the running server
// does, minting one where it has none, as a server's first start does.
func serverIDOf(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id, err := serveridentity.New(serversettings.NewServerSettingsRepo(pool)).ServerID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// keySignIn signs in on pool with a device key whose message names named,
// as the sign-in route does: the server compares it with its own identity.
func keySignIn(t *testing.T, pool *pgxpool.Pool, key appkeystest.Key, named string) (*auth.KeySignInResult, error) {
	t.Helper()
	users, sessions := auth.NewUserRepository(pool), auth.NewSessionRepository(pool)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("app-key-restore-test-jwt", time.Hour, 24*time.Hour), sessions, users, nil, nil, nil)
	signIn := auth.NewAppKeySignIn(pool, svc)
	challenge, now, err := signIn.IssueChallenge(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return signIn.SignIn(t.Context(), auth.KeyRequest{
		Envelope: key.SignIn(t, named, challenge, time.Unix(now, 0)), ServerID: serverIDOf(t, pool),
	})
}

func wantSignedIn(t *testing.T, pool *pgxpool.Pool, key appkeystest.Key, named string) {
	t.Helper()
	res, err := keySignIn(t, pool, key, named)
	if err != nil || res.Status != appkeys.ReplySignedIn || res.Pair == nil {
		t.Fatalf("key sign-in = %+v, %v; want signed in", res, err)
	}
}

func wantRefused(t *testing.T, pool *pgxpool.Pool, key appkeystest.Key, named string) {
	t.Helper()
	if res, err := keySignIn(t, pool, key, named); !errors.Is(err, auth.ErrKeySignatureInvalid) {
		t.Fatalf("key sign-in = %+v, %v; want %v", res, err, auth.ErrKeySignatureInvalid)
	}
}

// keyRevoked reports whether the key row id of table exists, and whether it
// is revoked.
func keyRevoked(t *testing.T, pool *pgxpool.Pool, table, id string) (exists, revoked bool) {
	t.Helper()
	err := pool.QueryRow(t.Context(), `SELECT true, revoked_at IS NOT NULL FROM `+table+` WHERE id = $1`, id).Scan(&exists, &revoked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	return exists, revoked
}

// keyRestore is a backup of a server with app keys and the keys its
// restores use.
type keyRestore struct {
	loader      PGLoader
	folder      string
	validate    ValidateOptions
	manifestKey []byte
}

// backupKeys backs up source, whose identity is keyBackupServerID.
func backupKeys(t *testing.T, source *pgxpool.Pool, sourceURL string) keyRestore {
	t.Helper()
	tools := testTools(t)
	master := []byte("app-key-restore-test-key-0123456789abcdef")
	keyCheck, err := secret.KeyCheckValue(master)
	if err != nil {
		t.Fatal(err)
	}
	r := keyRestore{loader: PGLoader{Tools: tools}, manifestKey: manifestKeyOf(t, master)}
	r.validate = ValidateOptions{KeyCheck: keyCheck, ManifestKey: r.manifestKey}
	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Pool: source, DatabaseURL: sourceURL, Destination: NewStoreDestination(store),
		Locker: PostgresLocker{Pool: source}, KeyCheck: keyCheck, ManifestKey: r.manifestKey,
		ServerID:  func(context.Context) (string, error) { return keyBackupServerID, nil },
		FindTools: func(context.Context) (Tools, error) { return tools, nil },
	})
	result, err := backupUnderLock(t, svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	r.folder = filepath.Join(root, "backups", keyBackupServerID, result.ID)
	return r
}

// swap runs a full restore onto liveURL and returns its output.
func (r keyRestore) swap(t *testing.T, liveURL string, validate ValidateOptions) string {
	t.Helper()
	ctx := t.Context()
	ctl, err := ConnectControl(ctx, liveURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctl.Close(ctx) }()
	var out bytes.Buffer
	var previous string
	err = RestoreSwap(ctx, openTestSource(t, r.folder), r.loader, ctl, SwapOptions{
		LiveURL: liveURL, DatabaseOnly: true, Validate: validate, WorkDir: t.TempDir(), JournalKey: r.manifestKey, Out: &out,
		Journaled: func(stamp string) { previous = "silo_before_restore_" + stamp },
	})
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	// The swap keeps the replaced database under a name shared by swaps in
	// the same second.
	if err := ctl.Drop(ctx, previous); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func wantOutput(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("restore output lacks %q:\n%s", w, out)
		}
	}
}

func openPool(t *testing.T, dbURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestFullRestoreOntoAnotherServerDB: a full restore of another server's
// backup onto an install with no accounts takes the backup's server
// identity, so a passwordless account signs in by key there; onto a server
// with an account it keeps the live identity, and the backup's keys are kept
// revoked. A waiting key only the backup has is revoked either way. The
// restore names each account that held a key and is left with no way in:
// an administrator whose only key is a person key and an account whose keys
// are all bound to a sign-in link always (another server's links are not
// carried), and the passwordless account too when its keys are revoked or a
// sign-in switch is off on the live server, which the restore keeps off. A
// backup that predates app keys takes the backup's identity without saying
// that devices sign in by key, and lists its account with no password.
func TestFullRestoreOntoAnotherServerDB(t *testing.T) {
	for _, tc := range []struct {
		name     string
		account  bool
		off      string // a switch turned off on the live server
		stranded bool   // the passwordless account is listed
		noKeys   bool   // the backup predates the key tables
	}{
		{name: "empty install"},
		{name: "install with an account", account: true, stranded: true},
		{name: "key sign-in off", off: config.AuthAppKeySignInSettingKey, stranded: true},
		{name: "local passwords off", off: config.AuthLocalPasswordLoginSettingKey, stranded: true},
		{name: "backup without key tables", noKeys: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, sourceURL := backuptest.Database(t)
			dbExec(t, source, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, serveridentity.Key, keyBackupServerID)
			if tc.noKeys {
				dbExec(t, source, `ALTER TABLE auth_sessions DROP COLUMN device_key_id`)
				dbExec(t, source, `DROP TABLE auth_device_keys, auth_person_keys`)
				dbExec(t, source, `DELETE FROM goose_db_version WHERE version_id IN (20261125041456, 20261125074454)`)
				var passwordless int64
				if err := source.QueryRow(t.Context(), `INSERT INTO users (role, username) VALUES ('user', 'passwordless') RETURNING id`).Scan(&passwordless); err != nil {
					t.Fatal(err)
				}
				r := backupKeys(t, source, sourceURL)
				live, liveURL := backuptest.Database(t)
				serverIDOf(t, live)
				live.Close()
				validate := r.validate
				validate.AcceptLostCredentials = true
				out := r.swap(t, liveURL, validate)
				if got := serverIDOf(t, openPool(t, liveURL)); got != keyBackupServerID {
					t.Fatalf("server id = %q, want the backup's %q", got, keyBackupServerID)
				}
				// Its passwordless account has no key to sign in with.
				wantOutput(t, out, identityTakenNoKeys, keepOriginalOff,
					fmt.Sprintf("%s passwordless (account %d).", strandedSentence, passwordless))
				if strings.Contains(out, identityTaken) {
					t.Fatalf("a restore that kept no app keys says devices sign in by key:\n%s", out)
				}
				return
			}
			holder := seedKeyHolder(t, source, "passwordless", "user")
			waiting := insertDeviceKey(t, source, holder.userID, appkeystest.NewKey(t), deviceKeyRow{status: "pending"})
			// An administrator whose only key is a person key: introducing a
			// device with it waits for another administrator.
			var adminID int64
			if err := source.QueryRow(t.Context(), `INSERT INTO users (role, username) VALUES ('admin', 'person-key-admin') RETURNING id`).Scan(&adminID); err != nil {
				t.Fatal(err)
			}
			insertPersonKey(t, source, adminID, appkeystest.NewKey(t))
			// An account whose only key is bound to its sign-in link.
			var linkedID, identityID int64
			if err := source.QueryRow(t.Context(), `INSERT INTO users (role, username) VALUES ('user', 'linked') RETURNING id`).Scan(&linkedID); err != nil {
				t.Fatal(err)
			}
			dbExec(t, source, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES (9001, 'silo.auth.oidc', '1.0.0', '/plugins/oidc', true)`)
			if err := source.QueryRow(t.Context(), `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
				VALUES (9001, 'subject', $1) RETURNING id`, linkedID).Scan(&identityID); err != nil {
				t.Fatal(err)
			}
			insertDeviceKey(t, source, linkedID, appkeystest.NewKey(t), deviceKeyRow{identityID: &identityID})
			r := backupKeys(t, source, sourceURL)

			live, liveURL := backuptest.Database(t)
			liveID := serverIDOf(t, live) // started: it has an identity of its own
			if tc.account {
				dbExec(t, live, `INSERT INTO users (role, username) VALUES ('admin', 'first-admin')`)
			}
			if tc.off != "" {
				dbExec(t, live, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')`, tc.off)
			}
			live.Close()
			validate := r.validate
			validate.AcceptLostCredentials = true
			out := r.swap(t, liveURL, validate)

			restored := openPool(t, liveURL)
			got := serverIDOf(t, restored)
			if _, revoked := keyRevoked(t, restored, tableAuthDeviceKeys, waiting); !revoked {
				t.Fatal("the backup's waiting key is not revoked")
			}
			listed := fmt.Sprintf("person-key-admin (account %d), linked (account %d).", adminID, linkedID)
			if tc.stranded {
				listed = fmt.Sprintf("passwordless (account %d), ", holder.userID) + listed
			}
			wantOutput(t, out, strandedSentence+" "+listed)
			if !tc.account {
				if got != keyBackupServerID {
					t.Fatalf("server id = %q, want the backup's %q", got, keyBackupServerID)
				}
				if !tc.stranded {
					wantSignedIn(t, restored, holder.device, got)
				}
				wantOutput(t, out, shutDownOriginal, identityTaken, keepOriginalOff)
				return
			}
			if got != liveID {
				t.Fatalf("server id = %q, want the live server's %q", got, liveID)
			}
			wantRefused(t, restored, holder.device, keyBackupServerID)
			wantRefused(t, restored, holder.device, liveID)
			if exists, revoked := keyRevoked(t, restored, tableAuthPersonKeys, holder.personID); !exists || !revoked {
				t.Fatalf("the backup's person key: exists %v, revoked %v; want a revoked row", exists, revoked)
			}
			wantOutput(t, out, identityKept)
			if strings.Contains(out, identityTaken) || strings.Contains(out, shutDownOriginal) {
				t.Fatalf("a server keeping its identity printed the move notice:\n%s", out)
			}
		})
	}
}

// TestFullRestoreOntoTheSameServerDB is a full restore onto the server the
// backup came from, after changes to its keys:
//   - a device key revoked since stays revoked, one enrolled since signs in,
//     and a person key replaced since stays replaced (the live server wins);
//   - an account the backup gives more authority keeps the state of the key
//     the backup holds, and its key enrolled since is revoked;
//   - a provider-bound key whose sign-in link is gone is revoked, its public
//     key can't be enrolled again, and a link of the same account that
//     reuses the id doesn't revive it;
//   - a public key the backup has on an account deleted since and the live
//     server has on another account stays with the live row.
func TestFullRestoreOntoTheSameServerDB(t *testing.T) {
	ctx := t.Context()
	source, sourceURL := backuptest.Database(t)
	dbExec(t, source, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, serveridentity.Key, keyBackupServerID)
	changed := seedKeyHolder(t, source, "changed", "user")
	demoted := seedKeyHolder(t, source, "demoted", "admin")
	linked := seedKeyHolder(t, source, "linked", "user")
	dbExec(t, source, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES (9001, 'silo.auth.oidc', '1.0.0', '/plugins/oidc', true)`)
	var identityID int64
	if err := source.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES (9001, 'subject', $1) RETURNING id`, linked.userID).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	bound := appkeystest.NewKey(t)
	boundID := insertDeviceKey(t, source, linked.userID, bound, deviceKeyRow{personID: linked.personID, identityID: &identityID})
	deleted := seedKeyHolder(t, source, "deleted", "user")
	other := seedKeyHolder(t, source, "other", "user")
	r := backupKeys(t, source, sourceURL)

	// After the backup.
	dbExec(t, source, `UPDATE auth_device_keys SET revoked_at = now() WHERE id = $1`, changed.deviceID)
	dbExec(t, source, `UPDATE auth_person_keys SET revoked_at = now() WHERE id = $1`, changed.personID)
	newPerson := appkeystest.NewKey(t)
	newPersonID := insertPersonKey(t, source, changed.userID, newPerson)
	enrolled := appkeystest.NewKey(t)
	insertDeviceKey(t, source, changed.userID, enrolled, deviceKeyRow{personID: newPersonID})
	// Rewrite the replaced key after the new one exists, so the live rows
	// reach the restore new key first, the order that would break the
	// one-live-person-key index were the restored key not staged.
	dbExec(t, source, `UPDATE auth_person_keys SET revoked_at = revoked_at WHERE id = $1`, changed.personID)
	dbExec(t, source, `UPDATE users SET role = 'user' WHERE id = $1`, demoted.userID)
	sinceDemotion := insertDeviceKey(t, source, demoted.userID, appkeystest.NewKey(t), deviceKeyRow{personID: demoted.personID})
	dbExec(t, source, `DELETE FROM plugin_auth_identities WHERE id = $1`, identityID)
	dbExec(t, source, `DELETE FROM users WHERE id = $1`, deleted.userID)
	movedID := insertDeviceKey(t, source, other.userID, deleted.device, deviceKeyRow{})
	source.Close()

	out := r.swap(t, sourceURL, r.validate)
	restored := openPool(t, sourceURL)
	wantOutput(t, out, "App keys: kept this server's where it has them")
	if strings.Contains(out, identityTaken) || strings.Contains(out, identityKept) || strings.Contains(out, strandedSentence) {
		t.Fatalf("a same-server restore printed an identity notice or listed an account:\n%s", out)
	}

	t.Run("live wins", func(t *testing.T) {
		wantRefused(t, restored, changed.device, keyBackupServerID)
		wantSignedIn(t, restored, enrolled, keyBackupServerID)
		if _, revoked := keyRevoked(t, restored, tableAuthPersonKeys, changed.personID); !revoked {
			t.Fatal("the person key replaced since the backup is live again")
		}
		if _, revoked := keyRevoked(t, restored, tableAuthPersonKeys, newPersonID); revoked {
			t.Fatal("the person key enrolled since the backup is revoked")
		}
	})
	t.Run("more authority in the backup", func(t *testing.T) {
		wantSignedIn(t, restored, demoted.device, keyBackupServerID)
		if _, revoked := keyRevoked(t, restored, tableAuthDeviceKeys, sinceDemotion); !revoked {
			t.Fatal("a key enrolled under less authority than the backup gives is not revoked")
		}
	})
	t.Run("sign-in link gone", func(t *testing.T) {
		if exists, revoked := keyRevoked(t, restored, tableAuthDeviceKeys, boundID); !exists || !revoked {
			t.Fatalf("the provider-bound key: exists %v, revoked %v; want a revoked row", exists, revoked)
		}
		_, err := restored.Exec(ctx, `INSERT INTO auth_person_keys (id, user_id, public_key, key_id, enrolled_via) VALUES ($1, $2, $3, $4, 'session')`,
			uuid.NewString(), linked.userID, bound.Public().Bytes(), bound.Public().KeyID())
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
			t.Fatalf("enrolling the revoked public key again = %v, want a unique violation", err)
		}
		dbExec(t, restored, `INSERT INTO plugin_auth_identities (id, plugin_installation_id, external_subject, user_id)
			OVERRIDING SYSTEM VALUE VALUES ($1, 9001, 'another-subject', $2)`, identityID, linked.userID)
		wantRefused(t, restored, bound, keyBackupServerID)
	})
	t.Run("public key moved to another account", func(t *testing.T) {
		if exists, _ := keyRevoked(t, restored, tableAuthDeviceKeys, deleted.deviceID); exists {
			t.Fatal("the deleted account's restored row still holds the public key")
		}
		if exists, revoked := keyRevoked(t, restored, tableAuthDeviceKeys, movedID); !exists || revoked {
			t.Fatalf("the live row: exists %v, revoked %v; want it live", exists, revoked)
		}
		wantSignedIn(t, restored, deleted.device, keyBackupServerID)
	})
}

// TestCarryOntoABackupWithoutAppKeysDB: a backup taken before app keys
// existed restores with none and no error.
func TestCarryOntoABackupWithoutAppKeysDB(t *testing.T) {
	live, liveURL := backuptest.Database(t)
	seedKeyHolder(t, live, "holder", "user")
	restored, restoredURL := backuptest.Database(t)
	dbExec(t, restored, `ALTER TABLE auth_sessions DROP COLUMN device_key_id`)
	dbExec(t, restored, `DROP TABLE auth_device_keys, auth_person_keys`)
	carried, err := PGLoader{}.CarryCredentials(t.Context(), restoredURL, liveURL, AppKeysKept)
	if err != nil {
		t.Fatal(err)
	}
	if carried.AppKeys.Live+carried.AppKeys.Restored != 0 || !strings.Contains(strings.Join(carried.Absent, ","), tableAuthDeviceKeys) {
		t.Fatalf("carried %+v, want no app keys and the key tables absent", carried)
	}
}

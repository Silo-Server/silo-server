package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/s3client/s3test"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// testDB opens SILO_TEST_DATABASE_URL, a migrated database other tests share.
// A test that runs pg_dump or pg_restore uses backuptest.Database instead:
// other packages' tests create and drop schemas here while a dump reads them.
func testDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

func testTools(t *testing.T) Tools {
	t.Helper()
	tools, err := FindTools(t.Context())
	if err != nil {
		t.Skipf("PostgreSQL client tools: %v", err)
	}
	return tools
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// createDatabase makes an empty database on the test server and drops it at
// the end of the test.
func createDatabase(t *testing.T, pool *pgxpool.Pool, dsn string) string {
	t.Helper()
	name := "silo_backup_test_" + randomHex(t, 4)
	if _, err := pool.Exec(t.Context(), "CREATE DATABASE "+ident(name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)")
	})
	url, err := WithDatabase(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// setSetting writes a setting for the test and puts the previous state back.
// Other packages' tests share the database, so the keys a test sets are ones
// nothing else reads.
func setSetting(t *testing.T, pool *pgxpool.Pool, settings *catalog.EncryptedSettingsRepo, key, value string) {
	t.Helper()
	ctx := t.Context()
	var previous string
	err := pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&previous)
	existed := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_, _ = pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, key, previous)
			return
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
	})
}

// roundTripSeed is what the round trip writes before backing up.
var storageIdentities = []string{blobstore.IdentitySettingKey, blobstore.OperationalIdentitySettingKey}

type roundTripSeed struct {
	secretKey, secretValue string
	missingKey             string
	blobs                  map[string][]byte
	userID                 int64
	// leftOut is a stored blob the backup must not copy.
	leftOut string
}

// seedRoundTrip writes an encrypted setting, recorded storage identities, a
// branding asset, a library with a poster, a library whose poster is gone,
// and a profile with an uploaded avatar, all removed again when the test
// ends.
func seedRoundTrip(t *testing.T, pool *pgxpool.Pool, settings *catalog.EncryptedSettingsRepo, store blobstore.Store) roundTripSeed {
	t.Helper()
	ctx := t.Context()
	suffix := randomHex(t, 4)
	seed := roundTripSeed{secretKey: "s3.operational_token_secret", secretValue: "token-" + suffix, missingKey: "library-posters/" + suffix + "-gone.jpg"}
	setSetting(t, pool, settings, seed.secretKey, seed.secretValue)
	setSetting(t, pool, settings, "branding.mark_light_ref", suffix+".webp")
	// A signing key, which no restore brings back (rotatedSettings).
	setSetting(t, pool, settings, settingJWTSecret, "backup-jwt-"+suffix)
	// The source's recorded stores, which a separate restore must not keep.
	for _, key := range storageIdentities {
		setSetting(t, pool, settings, key, "local|/elsewhere")
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folderID, userID int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT COALESCE(MAX(id), 0) + 1000 FROM media_folders), (SELECT COALESCE(MAX(id), 0) + 1000 FROM users)`).Scan(&folderID, &userID); err != nil {
		t.Fatal(err)
	}
	posterKey := "library-posters/" + suffix + ".jpg"
	exec(`INSERT INTO media_folders (id, type, name, poster_path) VALUES ($1, 'movies', $2, $3), ($4, 'movies', $5, $6)`,
		folderID, "Backup test "+suffix, posterKey, folderID+1, "Backup test gone "+suffix, seed.missingKey)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id IN ($1, $2)`, folderID, folderID+1)
	})
	profileID := "backup-test-" + suffix
	avatarKey := "profile-avatars/" + profileID + "/original.webp"
	exec(`INSERT INTO users (id, role, username) VALUES ($1, 'user', $2)`, userID, "backup-test-"+suffix)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	exec(`INSERT INTO user_profiles (id, user_id, name, avatar) VALUES ($1, $2, 'Backup', $3)`, profileID, userID, "upload:"+avatarKey)
	// A sign-in the restore must not bring back (secretTables, secretLeftOut).
	exec(`INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ($1, $2, now() + interval '1 day')`, "backup-test-"+suffix, userID)
	// An API key, which a full restore replaces with the live ones and a
	// separate restore leaves out (secretTables, secretCarried).
	exec(`INSERT INTO api_keys (user_id, label, key_hash) VALUES ($1, 'in backup', encode(sha256(convert_to($2, 'UTF8')), 'hex'))`, userID, "in-backup-"+suffix)
	// A browser push subscription, which a full restore also takes from the
	// live server.
	exec(`INSERT INTO web_push_subscriptions (id, user_id, profile_id, endpoint, p256dh, auth, device_name, enabled, consecutive_failures, created_at, updated_at)
		VALUES ($1, $2, $3, 'https://push.example.test/'||$1, 'key', 'auth', 'Browser', true, 0, now(), now())`, "push-"+suffix, userID, profileID)
	seed.userID = userID
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_profiles WHERE id = $1`, profileID) })
	// An uploaded subtitle, which the backup keeps with
	// backup.include_subtitles off, and a provider's, which it leaves out.
	setSetting(t, pool, settings, config.BackupIncludeSubtitlesSettingKey, "false")
	uploadedSub, providerSub := "subtitles/"+suffix+"-upload.srt", "subtitles/"+suffix+"-provider.srt"
	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Backup subtitles')`, "backup-test-"+suffix)
	exec(`INSERT INTO media_files (id, media_folder_id, file_path, content_id) VALUES ($1, $2, $3, $4)`,
		folderID, folderID, "/backup-test/"+suffix+".mkv", "backup-test-"+suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, "backup-test-"+suffix)
	})
	exec(`INSERT INTO downloaded_subtitles (media_file_id, provider, language, format, s3_key) VALUES ($1, 'upload', 'en', 'srt', $2), ($1, 'opensubtitles', 'en', 'srt', $3)`,
		folderID, uploadedSub, providerSub)
	if err := store.Put(ctx, providerSub, []byte("provider "+suffix)); err != nil {
		t.Fatal(err)
	}
	seed.leftOut = providerSub
	seed.blobs = map[string][]byte{
		uploadedSub: []byte("uploaded subtitle " + suffix),
		posterKey:   []byte("poster " + suffix),
		"branding/mark-light/" + suffix + ".webp": []byte("mark " + suffix),
		avatarKey: []byte("avatar " + suffix),
		"profile-avatars/" + profileID + "/w256.webp": []byte("avatar display " + suffix),
	}
	for key, data := range seed.blobs {
		if err := store.Put(ctx, key, data); err != nil {
			t.Fatal(err)
		}
	}
	return seed
}

// TestBackupRestoreRoundTripDB is the full restore round trip: a seeded
// database with blobs and an encrypted setting is backed up, restored in
// separate mode into an empty database and a blob directory, and the restored
// inventory, blobs, and decrypted value are compared with the source. The
// drill refuses a database in use, and another SECRET_KEY refuses to restore.
func TestBackupRestoreRoundTripDB(t *testing.T) {
	pool, dsn := backuptest.Database(t)
	tools := testTools(t)
	ctx := t.Context()
	masterKey := []byte("round-trip-test-key-0123456789abcdef")
	cipher, err := secret.New(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	keyCheck, err := secret.KeyCheckValue(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	manifestKey, err := secret.BackupManifestKey(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	seed := seedRoundTrip(t, pool, settings, store)

	serverID := "round-trip-" + randomHex(t, 4)
	svc := New(Options{
		Pool: pool, DatabaseURL: dsn, Settings: settings,
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker:      PostgresLocker{Pool: pool},
		ServerID:    func(context.Context) (string, error) { return serverID, nil },
		KeyCheck:    keyCheck,
		ManifestKey: manifestKey,
		FindTools:   func(context.Context) (Tools, error) { return tools, nil },
	})
	held, err := svc.opts.Locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Backup(ctx, held, KindManual, "", nil)
	held.Release(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCompleteWithWarnings || len(result.MissingBlobs) != 1 || result.MissingBlobs[0].Key != seed.missingKey {
		t.Fatalf("backup result = %+v, want complete with warnings naming %s", result, seed.missingKey)
	}

	src := openTestSource(t, filepath.Join(root, "backups", serverID, result.ID))
	into := createDatabase(t, pool, dsn)
	blobsInto := t.TempDir()
	loader := PGLoader{Tools: tools}
	if _, err := RestoreSeparate(ctx, src, loader, SeparateOptions{
		Validate: ValidateOptions{KeyCheck: keyCheck, ManifestKey: manifestKey}, Into: into, BlobsInto: blobsInto,
	}); err != nil {
		t.Fatal(err)
	}
	checkManifestTampering(t, src, manifestKey, keyCheck)

	in, err := loader.Inspect(ctx, into)
	if err != nil {
		t.Fatal(err)
	}
	// Everything the manifest counts, less the storage identities, the
	// signing keys, and the API keys the restore cleared.
	var signingKeys int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM server_settings WHERE key = ANY($1)`, rotatedSettingKeys()).Scan(&signingKeys); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(src.Manifest.Tables)
	for i := range want {
		switch want[i].Name {
		case "server_settings":
			want[i].Rows -= int64(len(storageIdentities)) + signingKeys
		case "api_keys":
			want[i].Rows = 0
		}
	}
	if in.GooseVersion != src.Manifest.GooseVersion || !slices.Equal(in.Tables, want) {
		t.Fatalf("restored inventory %d %+v, want %d %+v", in.GooseVersion, in.Tables, src.Manifest.GooseVersion, want)
	}
	if len(src.Manifest.AppliedMigrations) == 0 || !slices.Equal(in.Applied, src.Manifest.AppliedMigrations) {
		t.Fatalf("restored applied migrations %v, manifest %v", in.Applied, src.Manifest.AppliedMigrations)
	}
	restoredPool, err := pgxpool.New(ctx, into)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredPool.Close()
	checkRestoredFiles(t, seed, blobsInto, restoredPool)
	checkCredentials(t, loader, pool, dsn, into, restoredPool, seed.userID)
	restoredSettings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(restoredPool), cipher)
	if got, err := restoredSettings.Get(ctx, seed.secretKey); err != nil || got != seed.secretValue {
		t.Fatalf("restored encrypted setting = %q, %v; want %q", got, err, seed.secretValue)
	}

	if err := VerifyBackup(ctx, src, loader, into, ValidateOptions{KeyCheck: keyCheck, ManifestKey: manifestKey}, io.Discard); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("verify into a used database = %v, want refusal", err)
	}
	checkAnotherKey(t, src, loader, pool, dsn, seed.secretKey)
	checkDrillAsOrdinaryRole(t, src, loader, pool, dsn, ValidateOptions{KeyCheck: keyCheck, ManifestKey: manifestKey})
}

// checkDrillAsOrdinaryRole: a restore drill runs as the role the docs
// recommend, one that is not a superuser and owns only its scratch
// database, where a superuser created the extensions.
func checkDrillAsOrdinaryRole(t *testing.T, src *Source, loader PGLoader, pool *pgxpool.Pool, dsn string, validate ValidateOptions) {
	t.Helper()
	ctx := t.Context()
	exec := func(db *pgxpool.Pool, sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	role := "silo_drill_" + randomHex(t, 4)
	password := randomHex(t, 16)
	exec(pool, "CREATE ROLE "+ident(role)+" LOGIN PASSWORD '"+password+"'")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+ident(role)) })
	scratch := createDatabase(t, pool, dsn)
	name, err := databaseName(scratch)
	if err != nil {
		t.Fatal(err)
	}
	exec(pool, "ALTER DATABASE "+ident(name)+" OWNER TO "+ident(role))
	super, err := pgxpool.New(ctx, scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"vector", "citext", "pg_trgm"} {
		exec(super, "CREATE EXTENSION IF NOT EXISTS "+ext)
	}
	super.Close()
	asRole, err := url.Parse(scratch)
	if err != nil {
		t.Fatal(err)
	}
	asRole.User = url.UserPassword(role, password)
	if err := VerifyBackup(ctx, src, loader, asRole.String(), validate, io.Discard); err != nil {
		t.Fatalf("verify as a role that owns only the scratch database: %v", err)
	}
	// A swap as that role would create the restored database itself, where
	// it cannot create vector.
	extensions, err := loader.Extensions(ctx, src.path(DumpName))
	if err != nil || !slices.Contains(extensions, "vector") {
		t.Fatalf("the dump's extensions = %v (%v), want vector among them", extensions, err)
	}
	ctl, err := ConnectControl(ctx, asRole.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctl.Close(context.Background()) }()
	if err := ctl.CheckExtensions(ctx, extensions); !errors.Is(err, ErrExtensionNeedsSuperuser) {
		t.Fatalf("extensions as an ordinary role = %v, want ErrExtensionNeedsSuperuser", err)
	}
	if err := ctl.CheckExtensions(ctx, []string{"citext", "pg_trgm"}); err != nil {
		t.Fatalf("trusted extensions as an ordinary role = %v", err)
	}
}

// checkManifestTampering: a backup whose manifest was changed after it was
// written, here an object's checksum rewritten to match other bytes, is
// refused before anything reads its objects.
func checkManifestTampering(t *testing.T, src *Source, manifestKey []byte, keyCheck string) {
	t.Helper()
	path := filepath.Join(src.Folder(), ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, data, 0o600) })
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	m.Objects[0].SHA256 = strings.Repeat("0", 64)
	changed, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	tampered, err := OpenSource(src.Folder())
	if err != nil {
		t.Fatal(err)
	}
	if err := tampered.Validate(t.Context(), ValidateOptions{KeyCheck: keyCheck, ManifestKey: manifestKey}); !errors.Is(err, ErrManifestTampered) {
		t.Fatalf("validating a changed manifest = %v, want ErrManifestTampered", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// checkRestoredFiles: every blob the backup holds is restored, and a
// provider subtitle the setting left out is neither restored nor listed.
func checkRestoredFiles(t *testing.T, seed roundTripSeed, blobsInto string, restored *pgxpool.Pool) {
	t.Helper()
	for key, want := range seed.blobs {
		got, err := os.ReadFile(filepath.Join(blobsInto, filepath.FromSlash(key)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored blob %s = %q, %v; want %q", key, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(blobsInto, filepath.FromSlash(seed.leftOut))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restored %s, a provider subtitle the setting leaves out (%v)", seed.leftOut, err)
	}
	var omitted int
	if err := restored.QueryRow(t.Context(), `SELECT count(*) FROM downloaded_subtitles WHERE s3_key = $1`, seed.leftOut).Scan(&omitted); err != nil || omitted != 0 {
		t.Fatalf("restored %d rows of the subtitle the backup left out (%v), want none", omitted, err)
	}
}

// checkCredentials: a restore brings back no sign-in session. A full
// restore keeps the live server's API keys and pending invitations as they
// are at the swap, not the backup's, and drops a live key whose account is
// not in the backup.
func checkCredentials(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool, userID int64) {
	t.Helper()
	ctx := t.Context()
	var sessions int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM auth_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("restored %d sign-in sessions (%v), want none", sessions, err)
	}
	suffix := randomHex(t, 4)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// After the backup: the backed-up key is revoked, one is created for
	// the backed-up account, and one for an account the backup lacks; the
	// browser that subscribed to push notifications is removed.
	exec(`DELETE FROM api_keys WHERE user_id = $1`, userID)
	exec(`DELETE FROM web_push_subscriptions WHERE user_id = $1`, userID)
	exec(`INSERT INTO api_keys (user_id, label, key_hash) VALUES ($1, 'after backup', encode(sha256(convert_to($2, 'UTF8')), 'hex'))`, userID, "live-"+suffix)
	var newUser, keptID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (role, username) VALUES ('user', $1) RETURNING id`, "after-backup-"+suffix).Scan(&newUser); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, newUser) })
	exec(`INSERT INTO api_keys (user_id, label, key_hash) VALUES ($1, 'new account', encode(sha256(convert_to($2, 'UTF8')), 'hex'))`, newUser, "orphan-"+suffix)
	// A pending invitation, carried too.
	exec(`INSERT INTO invitations (token_hash, invited_by, expires_at, delivery, library_ids)
		VALUES ($1, $2, now() + interval '1 day', 'link', '{}')`, "invite-"+suffix, userID)
	var liveKeys, liveInvitations int
	if err := pool.QueryRow(ctx, `SELECT count(*), max(id) FILTER (WHERE user_id = $1),
		(SELECT count(*) FROM invitations WHERE accepted_at IS NULL AND revoked_at IS NULL) FROM api_keys`, userID).Scan(&liveKeys, &keptID, &liveInvitations); err != nil {
		t.Fatal(err)
	}

	carried, err := loader.CarryCredentials(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var orphan int64
	if err := pool.QueryRow(ctx, `SELECT id FROM api_keys WHERE user_id = $1`, newUser).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	wantDropped := []string{fmt.Sprintf("API key %d 'new account' (account %d)", orphan, newUser)}
	if carried.Kept["api_keys"] != liveKeys-1 || carried.Kept["invitations"] != liveInvitations || !slices.Equal(carried.Dropped, wantDropped) {
		t.Fatalf("carried %+v, want %d keys and %d invitations kept and dropped %q", carried, liveKeys-1, liveInvitations, wantDropped)
	}
	var invited int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE token_hash = $1`, "invite-"+suffix).Scan(&invited); err != nil || invited != 1 {
		t.Fatalf("restored %d copies of the live invitation (%v), want 1", invited, err)
	}
	var subscriptions int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM web_push_subscriptions WHERE user_id = $1`, userID).Scan(&subscriptions); err != nil || subscriptions != 0 {
		t.Fatalf("restored %d push subscriptions removed since the backup (%v), want none", subscriptions, err)
	}
	rows, err := restored.Query(ctx, `SELECT label FROM api_keys WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	if labels, err := pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || !slices.Equal(labels, []string{"after backup"}) {
		t.Fatalf("restored keys of the backed-up account = %v (%v), want only the live one", labels, err)
	}
	// The next key the restored server creates must not reuse a live id.
	var next int64
	if err := restored.QueryRow(ctx, `INSERT INTO api_keys (user_id, label, key_hash) VALUES ($1, 'next', encode(sha256(convert_to($2, 'UTF8')), 'hex')) RETURNING id`, userID, "next-"+suffix).Scan(&next); err != nil || next <= keptID {
		t.Fatalf("next restored key id %d (%v), want above %d", next, err, keptID)
	}
	checkCarryAuthority(t, loader, pool, dsn, into, restored, userID, keptID, liveKeys)
	checkCarryInvitation(t, loader, pool, dsn, into, restored, "invite-"+suffix)
	checkCarryAutoProvision(t, loader, pool, dsn, into, restored)
	checkOutboundConnections(t, loader, pool, dsn, into, restored, userID)
	checkInterruptedWork(t, loader, pool, dsn, into, restored, userID)
	checkOutboundSettings(t, loader, pool, dsn, into, restored)
	checkLiveReleasesAndNodes(t, loader, pool, dsn, into, restored)
	checkAccountRollback(t, loader, pool, dsn, into, userID, newUser)
	checkStricterFlags(t, loader, pool, dsn, into, restored, userID)
	checkStricterSettings(t, loader, pool, dsn, into, restored)
	checkOtherDeployment(t, loader, pool, dsn, into, restored)
}

// checkAccountRollback: the restore names the accounts it rolls back, an
// admin role and a password changed since the backup and an account created
// since among them. Other tests share the database, so it checks only these.
func checkAccountRollback(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, userID, newUser int64) {
	t.Helper()
	ctx := t.Context()
	var name, role, hash, created string
	if err := pool.QueryRow(ctx, `SELECT username, role, COALESCE(password_hash, '') FROM users WHERE id = $1`, userID).Scan(&name, &role, &hash); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `UPDATE users SET role = $2, password_hash = NULLIF($3, '') WHERE id = $1`, userID, role, hash)
	}()
	if _, err := pool.Exec(ctx, `UPDATE users SET role = 'admin', password_hash = 'changed-since' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT username FROM users WHERE id = $1`, newUser).Scan(&created); err != nil {
		t.Fatal(err)
	}
	r, err := loader.AccountRollback(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Authority, name) || !slices.Contains(r.Password, name) || !slices.Contains(r.Lost, created) ||
		slices.Contains(r.Returned, name) || r.LiveChangedAt.IsZero() {
		t.Fatalf("rollback %+v, want %s's role and password and %s lost", r, name, created)
	}
}

// checkOtherDeployment: the restore reads this server's identity to tell its
// own backups from another deployment's, and a carry for another deployment
// (no live URL) leaves the restored database none of the carried rows, its
// own copies included.
func checkOtherDeployment(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	var want string
	if err := pool.QueryRow(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'live-deployment')
		ON CONFLICT (key) DO UPDATE SET value = server_settings.value RETURNING value`, serveridentity.Key).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if got, err := loader.ServerID(ctx, dsn); err != nil || got != want || got == "" {
		t.Fatalf("live server id = %q (%v), want %q", got, err, want)
	}
	if _, err := loader.CarryCredentials(ctx, into, ""); err != nil {
		t.Fatal(err)
	}
	for _, table := range secretTablesHandled(secretCarried) {
		var n int
		if err := restored.QueryRow(ctx, `SELECT count(*) FROM `+table.name).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a restore for another deployment kept %d %s rows (%v)", n, table.name, err)
		}
	}
}

// checkStricterSettings: a full restore keeps the live value of each access
// setting the backup would widen (password sign-in turned off since, a
// narrower proxy list), and the backup's where it is as strict or stricter.
func checkStricterSettings(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	set := func(db *pgxpool.Pool, key, value string) {
		if _, err := db.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	set(pool, settingLocalPasswordLogin, "false")
	set(restored, settingLocalPasswordLogin, "true")
	set(pool, settingTrustedProxies, "10.0.0.0/8")
	set(restored, settingTrustedProxies, "10.0.0.0/8, 0.0.0.0/0")
	set(pool, config.AuthRefreshTokenExpirySettingKey, "30d")
	set(restored, config.AuthRefreshTokenExpirySettingKey, "7d")
	set(pool, settingReleaseEvents, "false")
	set(restored, settingReleaseEvents, "true")
	kept, err := loader.KeepStricterSettings(ctx, into, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{settingLocalPasswordLogin: "false", settingTrustedProxies: "10.0.0.0/8",
		config.AuthRefreshTokenExpirySettingKey: "7d", settingReleaseEvents: "false"} {
		var got string
		if err := restored.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&got); err != nil || got != want {
			t.Fatalf("restored %s = %q (%v), want %q", key, got, err, want)
		}
	}
	if !slices.Contains(kept, settingLocalPasswordLogin) || slices.Contains(kept, config.AuthRefreshTokenExpirySettingKey) {
		t.Fatalf("kept %v", kept)
	}
}

// checkStricterFlags: an account an admin disabled since the backup stays
// disabled after a full restore, with its required password change; every
// plugin turned off since stays off, a sign-in provider included; a plugin
// removed since comes back off, and so does a library deleted since; each
// is reported. A plugin on here and in the backup stays on.
func checkStricterFlags(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool, userID int64) {
	t.Helper()
	ctx := t.Context()
	var username string
	if err := pool.QueryRow(ctx, `UPDATE users SET enabled = false, password_change_required = true WHERE id = $1 RETURNING username`, userID).Scan(&username); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `UPDATE users SET enabled = true, password_change_required = false WHERE id = $1`, userID)
	}()
	if _, err := restored.Exec(ctx, `UPDATE users SET enabled = true, password_change_required = false WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	// A sign-in provider and another plugin, off here and on in the backup;
	// a plugin removed here since; one on in both.
	install := func(db *pgxpool.Pool, id int64, pluginID string, enabled bool) int64 {
		if id == 0 {
			if err := db.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled) VALUES ($1, '1.0.0', '/plugins/x', $2) RETURNING id`, pluginID, enabled).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		if _, err := db.Exec(ctx, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES ($1, $2, '1.0.0', '/plugins/x', $3)`, id, pluginID, enabled); err != nil {
			t.Fatal(err)
		}
		return id
	}
	provider := install(pool, 0, "sso-provider", false)
	install(restored, provider, "sso-provider", true)
	other := install(pool, 0, "other-plugin", false)
	install(restored, other, "other-plugin", true)
	removed := install(restored, other+1000, "removed-plugin", true)
	running := install(pool, 0, "running-plugin", true)
	install(restored, running, "running-plugin", true)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id) VALUES ($1, 'sso')`, provider); err != nil {
		t.Fatal(err)
	}
	// Autoscan turned off here since the backup: its carried token stays
	// unusable.
	if _, err := pool.Exec(ctx, `INSERT INTO autoscan_settings (id, enabled) VALUES (true, false) ON CONFLICT (id) DO UPDATE SET enabled = false`); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Exec(ctx, `INSERT INTO autoscan_settings (id, enabled) VALUES (true, true) ON CONFLICT (id) DO UPDATE SET enabled = true`); err != nil {
		t.Fatal(err)
	}
	// A library, an autoscan source, and a history import source deleted
	// here since the backup: each comes back off.
	var library, source, history string
	if err := restored.QueryRow(ctx, `INSERT INTO media_folders (name, type, enabled) VALUES ('Removed since', 'movies', true) RETURNING id::text`).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow(ctx, `INSERT INTO autoscan_sources (plugin_id, capability_id, enabled) VALUES ('arr', 'sonarr', true) RETURNING id::text`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow(ctx, `INSERT INTO history_import_sources (id, name, source_type, base_url, enabled)
		VALUES (987654321, 'removed', 'plex', 'http://plex.example', true) RETURNING id::text`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	carried, err := loader.CarryCredentials(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{tableLibraries: library, "autoscan_sources": source, "history_import_sources": history} {
		var on bool
		if err := restored.QueryRow(ctx, `SELECT enabled FROM `+table+` WHERE id::text = $1`, id).Scan(&on); err != nil || on {
			t.Fatalf("restored %s row removed since on %v (%v), want it off", table, on, err)
		}
	}
	var autoscanOn bool
	if err := restored.QueryRow(ctx, `SELECT enabled FROM autoscan_settings`).Scan(&autoscanOn); err != nil || autoscanOn {
		t.Fatalf("restored autoscan on %v (%v), want it kept off", autoscanOn, err)
	}
	var enabled, mustChange, providerOn, otherOn, removedOn, runningOn bool
	if err := restored.QueryRow(ctx, `SELECT enabled, password_change_required, (SELECT enabled FROM plugin_installations WHERE id = $2),
		(SELECT enabled FROM plugin_installations WHERE id = $3), (SELECT enabled FROM plugin_installations WHERE id = $4),
		(SELECT enabled FROM plugin_installations WHERE id = $5) FROM users WHERE id = $1`, userID, provider, other, removed, running).Scan(
		&enabled, &mustChange, &providerOn, &otherOn, &removedOn, &runningOn); err != nil {
		t.Fatal(err)
	}
	if enabled || !mustChange || providerOn || otherOn || removedOn || !runningOn {
		t.Fatalf("restored account enabled %v, must change its password %v; plugins on: provider %v, other %v, removed %v, running %v",
			enabled, mustChange, providerOn, otherOn, removedOn, runningOn)
	}
	for _, want := range []string{"account " + username + " kept disabled", "account " + username + " still must change its password",
		"plugin sso-provider kept off", "plugin other-plugin kept off", "plugin removed-plugin restored off: removed after the backup",
		"library Removed since restored disabled: removed after the backup",
		"autoscan_sources " + source + " restored with enabled off: removed after the backup"} {
		if !slices.Contains(carried.Stricter, want) {
			t.Fatalf("reported %v, want %q", carried.Stricter, want)
		}
	}
}

// checkLiveReleasesAndNodes: what the server launches or routes clients
// through is the live server's. A plugin both have runs the live release,
// archive and capabilities included; one whose id is another plugin here
// comes back off and is reported; a stream node removed since the backup
// stays removed.
func checkLiveReleasesAndNodes(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	exec := func(db *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := strconv.ParseInt(randomHex(t, 3), 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	same, other := 700_000_000+base, 710_000_000+base
	install := func(db *pgxpool.Pool, id int64, plugin, version, checksum string) {
		t.Helper()
		exec(db, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES ($1, $2, $3, '/plugins/'||$3, true)`, id, plugin, version)
		exec(db, `INSERT INTO plugin_archives (plugin_installation_id, manifest_json, checksum, archive_bytes) VALUES ($1, '\x7b7d', $2, '\x00')`, id, checksum)
		exec(db, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata) VALUES ($1, 'metadata', $2, '{}')`, id, version)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = ANY($1)`, []int64{same, other})
		_, _ = pool.Exec(context.Background(), `DELETE FROM stream_nodes WHERE name = ANY($1)`, []string{"live-node", "removed-node"})
	})
	install(pool, same, "release-plugin", "2.0.0", "live")
	install(restored, same, "release-plugin", "1.0.0", "backup")
	install(pool, other, "live-plugin", "1.0.0", "live")
	install(restored, other, "backup-plugin", "1.0.0", "backup")
	off, err := loader.CarryPluginReleases(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var version, checksum, capability string
	var enabled bool
	if err := restored.QueryRow(ctx, `SELECT i.version, a.checksum, c.capability_id FROM plugin_installations i
		JOIN plugin_archives a ON a.plugin_installation_id = i.id JOIN plugin_capabilities c ON c.plugin_installation_id = i.id
		WHERE i.id = $1`, same).Scan(&version, &checksum, &capability); err != nil || version != "2.0.0" || checksum != "live" || capability != "2.0.0" {
		t.Fatalf("a plugin both have runs %s %s %s (%v), want the live release", version, checksum, capability, err)
	}
	if err := restored.QueryRow(ctx, `SELECT enabled FROM plugin_installations WHERE id = $1`, other).Scan(&enabled); err != nil || enabled ||
		!slices.ContainsFunc(off, func(s string) bool { return strings.Contains(s, "backup-plugin") }) {
		t.Fatalf("an installation of another plugin here: enabled %v (%v), reported %v; want it off and reported", enabled, err, off)
	}
	// Reinstalled since the backup from a catalog added since: the live
	// release names a catalog the backup does not have.
	var catalogID int64
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_repositories (url, enabled, display_name) VALUES ($1, true, 'New catalog') RETURNING id`,
		fmt.Sprintf("https://catalog-%d.invalid/index.json", base)).Scan(&catalogID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_repositories WHERE id = $1`, catalogID)
	})
	exec(pool, `UPDATE plugin_installations SET repository_id = $2, version = '3.0.0' WHERE id = $1`, same, catalogID)
	if _, err := loader.CarryCredentials(ctx, into, dsn); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.CarryPluginReleases(ctx, into, dsn); err != nil {
		t.Fatalf("a release from a catalog added since: %v", err)
	}
	var repository *int64
	if err := restored.QueryRow(ctx, `SELECT version, repository_id FROM plugin_installations WHERE id = $1`, same).Scan(&version, &repository); err != nil ||
		version != "3.0.0" || repository == nil || *repository != catalogID {
		t.Fatalf("reinstalled plugin = %s from %v (%v), want the live release and catalog", version, repository, err)
	}
	exec(pool, `INSERT INTO stream_nodes (name, type, url, enabled) VALUES ('live-node', 'transcode', 'http://live.invalid', true)`)
	exec(restored, `INSERT INTO stream_nodes (name, type, url, enabled) VALUES ('removed-node', 'transcode', 'http://removed.invalid', true)`)
	if _, err := loader.CarryCredentials(ctx, into, dsn); err != nil {
		t.Fatal(err)
	}
	var removed, liveNodes int
	if err := restored.QueryRow(ctx, `SELECT count(*) FILTER (WHERE name = 'removed-node'), count(*) FILTER (WHERE name = 'live-node') FROM stream_nodes`).Scan(&removed, &liveNodes); err != nil ||
		removed != 0 || liveNodes != 1 {
		t.Fatalf("stream nodes: %d removed since, %d live (%v); want the live set", removed, liveNodes, err)
	}
}

// checkOutboundSettings: a full restore keeps the live mail route as a
// unit, an SMTP host and password changed since the backup included; a
// route the live server does not have comes back with its secret cleared
// and turned off, and so does every route of a restore that carries none.
func checkOutboundSettings(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	set := func(db *pgxpool.Pool, values map[string]string) {
		t.Helper()
		for key, value := range values {
			if _, err := db.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(key string) string {
		t.Helper()
		var v string
		if err := restored.QueryRow(ctx, `SELECT coalesce((SELECT value FROM server_settings WHERE key = $1), '')`, key).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	keys := []string{settingSMTPHost, settingSMTPPassword, mail.SettingSMTPUsername, settingEmailEnabled, settingAIBaseURL, settingAIKey}
	var saved []string
	for _, key := range keys {
		var v *string
		_ = pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&v)
		if v != nil {
			saved = append(saved, key, *v)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = ANY($1)`, keys)
		for i := 0; i+1 < len(saved); i += 2 {
			_, _ = pool.Exec(context.Background(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, saved[i], saved[i+1])
		}
	})
	set(restored, map[string]string{settingSMTPHost: "smtp.then.invalid", settingSMTPPassword: "then", mail.SettingSMTPUsername: "then-user", settingEmailEnabled: "true",
		settingAIBaseURL: "https://ai.then.invalid", settingAIKey: "then-key"})
	set(pool, map[string]string{settingSMTPHost: "smtp.now.invalid", settingSMTPPassword: "now", mail.SettingSMTPUsername: "now-user", settingEmailEnabled: "true"})
	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, []string{settingAIBaseURL, settingAIKey}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loader.CarryOutboundSettings(ctx, into, dsn); err != nil {
		t.Fatal(err)
	}
	if read(settingSMTPHost) != "smtp.now.invalid" || read(settingSMTPPassword) != "now" || read(mail.SettingSMTPUsername) != "now-user" {
		t.Fatalf("mail route %s / %s, want the live one", read(settingSMTPHost), read(settingSMTPPassword))
	}
	if read(settingAIBaseURL) != "https://ai.then.invalid" || read(settingAIKey) != "" {
		t.Fatalf("a route the live server lacks: %s / %q, want the address kept and the key cleared", read(settingAIBaseURL), read(settingAIKey))
	}
	if _, cleared, err := loader.CarryOutboundSettings(ctx, into, ""); err != nil || read(settingSMTPPassword) != "" || read(settingEmailEnabled) != settingOff ||
		!slices.Contains(cleared, "mail (SMTP)") {
		t.Fatalf("carrying none (%v): password %q, enabled %q, cleared %v; want it cleared and off", err, read(settingSMTPPassword), read(settingEmailEnabled), cleared)
	}
}

// checkInterruptedWork: after a full restore nothing the backup had queued
// or running runs by itself. A storage transition queued when the backup
// was taken, and canceled on the live server since, ends canceled, its
// staged target is not revived, and a finished job stays as it was.
func checkInterruptedWork(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool, userID int64) {
	t.Helper()
	ctx := t.Context()
	var queued, done string
	for status, id := range map[string]*string{"queued": &queued, "completed": &done} {
		if err := restored.QueryRow(ctx, `INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, requested_at, updated_at)
			VALUES (gen_random_uuid()::text, 'storage_transition', $1, $2, now(), now()) RETURNING id`, status, userID).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := restored.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'staged-elsewhere')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.StorageTransitionTargetKey); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, config.StorageTransitionTargetKey); err != nil {
		t.Fatal(err)
	}
	// The search engine's index as the backup knew it.
	if _, err := restored.Exec(ctx, `INSERT INTO catalog_search_index_state (provider, active_index_uid, schema_version, updated_at)
		VALUES ('meilisearch', 'silo_catalog_then', 1, now()) ON CONFLICT (provider) DO UPDATE SET active_index_uid = EXCLUDED.active_index_uid`); err != nil {
		t.Fatal(err)
	}
	changed, err := loader.InterruptWork(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	status := func(id string) (s string) {
		t.Helper()
		if err := restored.QueryRow(ctx, `SELECT status FROM admin_jobs WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	var staged, indexes int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM server_settings WHERE key = $1`, config.StorageTransitionTargetKey).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM catalog_search_index_state`).Scan(&indexes); err != nil || indexes != 0 {
		t.Fatalf("search index state %d (%v), want none so the index is rebuilt", indexes, err)
	}
	if status(queued) != adminjob.StatusCancelled || status(done) != adminjob.StatusCompleted || staged != 0 || len(changed) == 0 {
		t.Fatalf("queued job %s, finished job %s, staged targets %d, changed %v; want it ended, history kept, no target",
			status(queued), status(done), staged, changed)
	}
}

// checkOutboundConnections: what this server presents to outside services
// never comes back from the backup. A watch-provider connection removed
// since the backup stays removed; a history import source keeps the
// backup's row but takes the live address and token, and one the live
// server no longer has comes back with its token cleared and turned off.
func checkOutboundConnections(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool, userID int64) {
	t.Helper()
	ctx := t.Context()
	var profile string
	if err := restored.QueryRow(ctx, `SELECT id FROM user_profiles WHERE user_id = $1 LIMIT 1`, userID).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	var connection string
	if err := restored.QueryRow(ctx, `INSERT INTO watch_provider_connections (provider, user_id, profile_id, access_token, scrobble_enabled)
		VALUES ('trakt', $1, $2, 'removed-since', true) RETURNING id::text`, userID, profile).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	base, err := strconv.ParseUint(randomHex(t, 2), 16, 32)
	if err != nil {
		t.Fatal(err)
	}
	kept, gone := 900_000_000+int(base), 900_100_000+int(base)
	source := func(db *pgxpool.Pool, id int, url, token string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO history_import_sources (id, name, source_type, base_url, admin_token, enabled)
			VALUES ($1, 'Source', 'jellyfin', $2, $3, true)`, id, url, token); err != nil {
			t.Fatal(err)
		}
	}
	source(pool, kept, "https://now.invalid", "live-token")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM history_import_sources WHERE id = $1`, kept)
	})
	source(restored, kept, "https://then.invalid", "backup-token")
	source(restored, gone, "https://gone.invalid", "gone-token")
	carried, err := loader.CarryCredentials(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var left int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM watch_provider_connections WHERE id::text = $1`, connection).Scan(&left); err != nil || left != 0 {
		t.Fatalf("a watch-provider connection removed since the backup: %d rows (%v), want none", left, err)
	}
	read := func(id int) (url, token string, enabled bool) {
		t.Helper()
		if err := restored.QueryRow(ctx, `SELECT base_url, coalesce(admin_token, ''), enabled FROM history_import_sources WHERE id = $1`, id).Scan(&url, &token, &enabled); err != nil {
			t.Fatal(err)
		}
		return url, token, enabled
	}
	if url, token, enabled := read(kept); url != "https://now.invalid" || token != "live-token" || !enabled {
		t.Fatalf("a source both have = %s %s %v, want the live address and token", url, token, enabled)
	}
	if _, token, enabled := read(gone); token != "" || enabled || !slices.ContainsFunc(carried.Cleared, func(s string) bool { return strings.HasPrefix(s, fmt.Sprintf("history import source %d", gone)) }) {
		t.Fatalf("a source only the backup has: token %q, enabled %v, cleared %v; want it cleared, off, and reported", token, enabled, carried.Cleared)
	}
}

// checkCarryAutoProvision: a sign-in provider that creates accounts is
// carried with account creation off, and reported, when an account it
// creates would get more under the backup's default group than here; it
// keeps creating them under the same policy.
func checkCarryAutoProvision(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	var installation, binding int64
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled) VALUES ('provisioning-sso', '1.0.0', '/plugins/x', true) RETURNING id`).Scan(&installation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installation)
	})
	if _, err := restored.Exec(ctx, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES ($1, 'provisioning-sso', '1.0.0', '/plugins/x', true)`, installation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, auto_provision) VALUES ($1, 'oidc', true) RETURNING id`, installation).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	// The default group's stream cap: unlimited (0) in the backup, one
	// here. A cap is no on/off switch, so the restore does not keep the
	// live one, and the restored group is wider.
	var streams int
	if err := pool.QueryRow(ctx, `SELECT max_streams FROM access_groups WHERE is_default`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE access_groups SET max_streams = $1 WHERE is_default`, streams)
	})
	for db, limit := range map[*pgxpool.Pool]int{pool: 1, restored: 0} {
		if _, err := db.Exec(ctx, `UPDATE access_groups SET max_streams = $1 WHERE is_default`, limit); err != nil {
			t.Fatal(err)
		}
	}
	provisions := func() bool {
		t.Helper()
		var on bool
		if err := restored.QueryRow(ctx, `SELECT auto_provision FROM plugin_auth_bindings WHERE id = $1`, binding).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}
	limited := func(c CarriedCredentials) bool {
		return slices.ContainsFunc(c.Limited, func(s string) bool { return strings.HasPrefix(s, fmt.Sprintf("sign-in provider %d (oidc)", binding)) })
	}
	carried, err := loader.CarryCredentials(ctx, into, dsn)
	if err != nil || !limited(carried) || provisions() {
		t.Fatalf("carried %+v (%v), provisions %v; want the provider kept with account creation off", carried, err, provisions())
	}
	if _, err := restored.Exec(ctx, `UPDATE access_groups SET max_streams = 1 WHERE is_default`); err != nil {
		t.Fatal(err)
	}
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil || limited(carried) || !provisions() {
		t.Fatalf("carried %+v (%v), provisions %v; want account creation kept under the same policy", carried, err, provisions())
	}
}

// checkCarryInvitation: a pending invitation is dropped when the account it
// would create gets more under the restored data than live (its default
// group had no stream cap in the backup and has one now), and kept when the
// policies match.
func checkCarryInvitation(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool, tokenHash string) {
	t.Helper()
	ctx := t.Context()
	var id int64
	var streams int
	if err := pool.QueryRow(ctx, `SELECT i.id, g.max_streams FROM invitations i, access_groups g WHERE i.token_hash = $1 AND g.is_default`, tokenHash).Scan(&id, &streams); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE access_groups SET max_streams = $1 WHERE is_default`, streams)
	})
	setStreams := func(db *pgxpool.Pool, limit int) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE access_groups SET max_streams = $1 WHERE is_default`, limit); err != nil {
			t.Fatal(err)
		}
	}
	invited := func() int {
		t.Helper()
		var n int
		if err := restored.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE token_hash = $1`, tokenHash).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	setStreams(pool, 1)
	setStreams(restored, 0)
	carried, err := loader.CarryCredentials(ctx, into, dsn)
	want := fmt.Sprintf("invitation %d", id)
	if err != nil || !slices.Contains(carried.Dropped, want) || invited() != 0 {
		t.Fatalf("carried %+v (%v), want %q dropped: its group had no stream cap in the backup", carried, err, want)
	}
	setStreams(restored, 1)
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil || slices.Contains(carried.Dropped, want) || invited() != 1 {
		t.Fatalf("carried %+v (%v), want the invitation kept under the same policy", carried, err)
	}
	// The group stopped allowing the permissions a new account gets by
	// default (auth.NewUserPermissions) after the backup.
	var permissions []string
	if err := pool.QueryRow(ctx, `SELECT allowed_permissions FROM access_groups WHERE is_default`).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE access_groups SET allowed_permissions = $1 WHERE is_default`, permissions)
	})
	setPermissions := func(db *pgxpool.Pool, allowed []string) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE access_groups SET allowed_permissions = $1 WHERE is_default`, allowed); err != nil {
			t.Fatal(err)
		}
	}
	setPermissions(pool, []string{})
	setPermissions(restored, auth.DefaultUserPermissions())
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil || !slices.Contains(carried.Dropped, want) || invited() != 0 {
		t.Fatalf("carried %+v (%v), want %q dropped: its group allowed more permissions in the backup", carried, err, want)
	}
	setPermissions(restored, []string{})
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil || slices.Contains(carried.Dropped, want) || invited() != 1 {
		t.Fatalf("carried %+v (%v), want the invitation kept once the permissions match", carried, err)
	}
}

// checkCarryAuthority: a live key is dropped when its account has more
// authority in the restored data than live (an admin there, or every
// library there and one here), and kept when the policies match.
func checkCarryAuthority(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool, userID, keptID int64, liveKeys int) {
	t.Helper()
	ctx := t.Context()
	// An account that was an admin in the backup and is a user here: its
	// live key would carry admin authority, so it is dropped.
	if _, err := restored.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	carried, err := loader.CarryCredentials(ctx, into, dsn)
	if err != nil {
		t.Fatal(err)
	}
	demoted := fmt.Sprintf("API key %d 'after backup' (account %d)", keptID, userID)
	var left int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE user_id = $1`, userID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if carried.Kept["api_keys"] != liveKeys-2 || !slices.Contains(carried.Dropped, demoted) || left != 0 {
		t.Fatalf("carried %+v with %d keys left for the demoted account, want its key %q dropped", carried, left, demoted)
	}
	// The same account with the same role, narrowed here to one library
	// since the backup: the restored account sees every library, so its
	// live key is dropped too.
	var library int64
	if err := restored.QueryRow(ctx, `UPDATE users SET role = 'user' WHERE id = $1 RETURNING (SELECT min(id) FROM media_folders)`, userID).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET library_ids = ARRAY[$2::int] WHERE id = $1`, userID, library); err != nil {
		t.Fatal(err)
	}
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(carried.Dropped, demoted) {
		t.Fatalf("carried %+v, want the narrowed account's key %q dropped", carried, demoted)
	}
	// Narrowed the same way in the restored data, it keeps its key.
	if _, err := restored.Exec(ctx, `UPDATE users SET library_ids = ARRAY[$2::int] WHERE id = $1`, userID, library); err != nil {
		t.Fatal(err)
	}
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(carried.Dropped, demoted) || carried.Kept["api_keys"] != liveKeys-1 {
		t.Fatalf("carried %+v, want the key of an account with the same policy kept", carried)
	}
	// Blocked from requesting here, and unlimited with automatic approval
	// in the backup: its key could request titles that approve
	// themselves, so it is dropped.
	exec := func(db *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(pool, `INSERT INTO request_user_limits (user_id, limit_mode, approval_mode) VALUES ($1, 'blocked', 'inherit')
		ON CONFLICT (user_id) DO UPDATE SET limit_mode = 'blocked', approval_mode = 'inherit'`, userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM request_user_limits WHERE user_id = $1`, userID)
	})
	// Requests on here too: the restore keeps the stricter of the two
	// servers' switches before it compares authority.
	var requestsOn bool
	if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT requests_enabled FROM request_settings WHERE id), false)`).Scan(&requestsOn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE request_settings SET requests_enabled = $1 WHERE id`, requestsOn)
	})
	for _, db := range []*pgxpool.Pool{pool, restored} {
		exec(db, `INSERT INTO request_settings (id, requests_enabled) VALUES (true, true) ON CONFLICT (id) DO UPDATE SET requests_enabled = true`)
	}
	exec(restored, `INSERT INTO request_user_limits (user_id, limit_mode, approval_mode) VALUES ($1, 'unlimited', 'auto')
		ON CONFLICT (user_id) DO UPDATE SET limit_mode = 'unlimited', approval_mode = 'auto'`, userID)
	if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(carried.Dropped, demoted) {
		t.Fatalf("carried %+v, want the key of an account the backup lets request more dropped", carried)
	}
	// The same request limits on both sides again keep the key; then its
	// primary profile has a PIN here and none in the backup, so whoever
	// holds the key could manage the household without the PIN: dropped,
	// until the backup's profile has a PIN too.
	exec(restored, `UPDATE request_user_limits SET limit_mode = 'blocked', approval_mode = 'inherit' WHERE user_id = $1`, userID)
	keptAs := func(want bool, why string) {
		t.Helper()
		if carried, err = loader.CarryCredentials(ctx, into, dsn); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(carried.Dropped, demoted) == want {
			t.Fatalf("carried %+v: %s", carried, why)
		}
	}
	keptAs(true, "want the key kept under the same request limits")
	var profile string
	var pin string
	var primary bool
	if err := pool.QueryRow(ctx, `SELECT id, pin_hash, is_primary FROM user_profiles WHERE user_id = $1 ORDER BY id LIMIT 1`, userID).Scan(&profile, &pin, &primary); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE user_profiles SET pin_hash = $2, is_primary = $3 WHERE id = $1`, profile, pin, primary)
	})
	exec(pool, `UPDATE user_profiles SET pin_hash = 'live-pin', is_primary = true WHERE id = $1`, profile)
	exec(restored, `UPDATE user_profiles SET pin_hash = '', is_primary = true WHERE id = $1`, profile)
	keptAs(false, "want the key dropped: the backup's primary profile has no PIN")
	exec(restored, `UPDATE user_profiles SET pin_hash = 'backup-pin' WHERE id = $1`, profile)
	keptAs(true, "want the key kept: both primary profiles have a PIN")
	// A profile deleted here since the backup comes back with it: the key's
	// holder could select it, so the key is dropped.
	exec(restored, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'Deleted since')`, profile+"-gone", userID)
	keptAs(false, "want the key dropped: the backup has a profile this account no longer has")
	exec(restored, `DELETE FROM user_profiles WHERE id = $1`, profile+"-gone")
}

// checkAnotherKey: a backup taken with another SECRET_KEY restores only
// with the operator's explicit acceptance, and then without the encrypted
// settings this key cannot read, which Silo could not start on.
func checkAnotherKey(t *testing.T, src *Source, loader PGLoader, pool *pgxpool.Pool, dsn, secretKey string) {
	t.Helper()
	ctx := t.Context()
	other, err := secret.New([]byte("another-test-key-0123456789abcdefgh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSeparate(ctx, src, loader, SeparateOptions{
		Validate: ValidateOptions{KeyCheck: "0000-0000-0000-0000"}, Into: createDatabase(t, pool, dsn), DatabaseOnly: true,
	}); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("restore with another key = %v, want ErrKeyMismatch", err)
	}
	into := createDatabase(t, pool, dsn)
	var out strings.Builder
	if _, err := RestoreSeparate(ctx, src, loader, SeparateOptions{
		Validate: ValidateOptions{KeyCheck: "0000-0000-0000-0000", AcceptLostCredentials: true, AcceptSuperuser: true}, Cipher: other,
		Into: into, DatabaseOnly: true, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := pgxpool.New(ctx, into)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var left int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM server_settings WHERE value LIKE 'enc:%'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != len(rotatedSettings) || !strings.Contains(out.String(), secretKey) || strings.Contains(out.String(), "enter them again after Silo starts: auth.jwt_secret") {
		t.Fatalf("%d encrypted settings left, want only the new signing keys; output %q must name %s and not the signing key", left, out.String(), secretKey)
	}
	// The signing key is a new one, encrypted with this server's key.
	jwt, err := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(restored), other).Get(ctx, settingJWTSecret)
	if err != nil || jwt == "" || strings.HasPrefix(jwt, "backup-jwt-") {
		t.Fatalf("restored signing key = %q, %v; want a new one", jwt, err)
	}
}

// TestEveryTableIsClassifiedDB: every table of the schema, after the
// migrations, is in exactly one list (secretTables, loggedTables,
// restoredDataTables, serverStateTables), and every listed name is a table,
// so a new table cannot reach a backup or a restore without someone
// deciding whether it is data, server state, a log, or a credential the
// restore leaves out or takes from the live server.
func TestEveryTableIsClassifiedDB(t *testing.T) {
	pool, _ := backuptest.Database(t)
	ctx := t.Context()
	tables, err := collectStrings(ctx, pool, `
		SELECT c.relname::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	add := func(list, name string) {
		if other, ok := listed[name]; ok {
			t.Errorf("%s is in %s and %s", name, other, list)
		}
		listed[name] = list
	}
	for _, table := range secretTables {
		add("secretTables", table.name)
		if (table.handling == secretCarried) != (table.carry != nil) || table.why == "" {
			t.Errorf("secretTables entry %s: a carried table needs a carry spec, every entry a reason", table.name)
		}
	}
	for _, name := range loggedTables {
		add("loggedTables", name)
	}
	for _, name := range liveTables {
		add("liveTables", name)
	}
	for _, name := range restoredDataTables {
		add("restoredDataTables", name)
	}
	for _, name := range serverStateTables {
		add("serverStateTables", name)
	}
	present := map[string]bool{}
	for _, table := range tables {
		present[table] = true
		if _, ok := listed[table]; !ok {
			t.Errorf("table %s is not classified: add it to internal/backup (tables.go or secretTables in credentials.go) as data, "+
				"server state, a log, or a credential a restore leaves out or carries from the live server", table)
		}
	}
	for name, list := range listed {
		if !present[name] {
			t.Errorf("%s names %s, which is not a table", list, name)
		}
	}
	// A security switch kept at its stricter value is a column of a table
	// the restore otherwise restores as the backup has it.
	restoredAsBackedUp := map[string]bool{}
	for _, name := range restoredDataTables {
		restoredAsBackedUp[name] = true
	}
	for _, h := range []secretHandling{secretRestored, secretScrubbed} {
		for _, table := range secretTablesHandled(h) {
			restoredAsBackedUp[table.name] = true
		}
	}
	for _, f := range stricterFlags {
		var boolean bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2 AND data_type = 'boolean')`, f.table, f.column).Scan(&boolean); err != nil {
			t.Fatal(err)
		}
		if !restoredAsBackedUp[f.table] || !boolean {
			t.Errorf("stricterFlags %s.%s: want a boolean column of a table restored as the backup has it, it is in %q", f.table, f.column, listed[f.table])
		}
		// A row removed since the backup comes back with an off-wins switch
		// off, unless the flag says why it rolls back as it was.
		if !f.stricter && !f.absentOff && f.rollsBack == "" {
			t.Errorf("stricterFlags %s.%s: a row removed since the backup would come back on; set absentOff or say why it rolls back", f.table, f.column)
		}
	}
	checkFlagColumnsDecided(t, pool, restoredAsBackedUp)
}

// checkFlagColumnsDecided: every boolean column of a table the restore
// brings back as the backup has it is either a stricterFlags switch or in
// restoredFlagColumns with its reason, never both, so a new switch cannot
// be restored without a decision; and every restoredFlagColumns entry is
// such a column.
func checkFlagColumnsDecided(t *testing.T, pool *pgxpool.Pool, restored map[string]bool) {
	t.Helper()
	columns, err := collectStrings(t.Context(), pool, `SELECT c.table_name || '.' || c.column_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE' AND c.data_type = 'boolean' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	stricter := map[string]bool{}
	for _, f := range stricterFlags {
		stricter[f.table+"."+f.column] = true
	}
	present := map[string]bool{}
	for _, column := range columns {
		present[column] = true
		table, _, _ := strings.Cut(column, ".")
		_, decided := restoredFlagColumns[column]
		switch {
		case !restored[table]:
		case stricter[column] && decided:
			t.Errorf("%s is a stricterFlags switch and in restoredFlagColumns", column)
		case !stricter[column] && !decided:
			t.Errorf("%s is a switch with no decision: add it to stricterFlags, or to restoredFlagColumns with why the backup's value is safe", column)
		}
	}
	for column := range restoredFlagColumns {
		table, _, _ := strings.Cut(column, ".")
		if !present[column] || !restored[table] {
			t.Errorf("restoredFlagColumns names %s, which is not a boolean column of a restored table", column)
		}
	}
}

// failingDestination fails writes of one object name.
type failingDestination struct {
	Destination
	failName string
}

func (d failingDestination) Put(ctx context.Context, key string, r io.Reader) error {
	if strings.HasSuffix(key, "/"+d.failName) {
		_, _ = io.Copy(io.Discard, r)
		return errors.New("injected write failure")
	}
	return d.Destination.Put(ctx, key, r)
}

// TestBackupWritesManifestLastDB: a backup that fails after the dump leaves a
// folder with no manifest, which listing reports as incomplete and the sweep
// deletes once it is a day old.
func TestBackupWritesManifestLastDB(t *testing.T) {
	pool, dsn := backuptest.Database(t)
	tools := testTools(t)
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	serverID := "manifest-last-" + randomHex(t, 4)
	opts := Options{
		Pool: pool, DatabaseURL: dsn,
		Stores:      blobstore.Stores{Assets: store, Operational: store},
		Destination: failingDestination{Destination: NewStoreDestination(store), failName: BlobsName},
		Locker:      PostgresLocker{Pool: pool},
		Status:      PostgresStatus{Pool: pool},
		ServerID:    func(context.Context) (string, error) { return serverID, nil },
		FindTools:   func(context.Context) (Tools, error) { return tools, nil },
		Now:         func() time.Time { return now },
	}
	resetStatus := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM backup_status`) }
	resetStatus()
	t.Cleanup(resetStatus)
	failing := New(opts)
	if _, err := backupUnderLock(t, failing, KindNightly); err == nil {
		t.Fatal("backup with a failing blobs.tar write succeeded")
	}
	failedID := FolderName(now, KindNightly)
	if _, err := store.Stat(ctx, objectKey(serverID, failedID, ManifestName)); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("failed backup wrote a manifest: %v", err)
	}
	if _, err := store.Stat(ctx, objectKey(serverID, failedID, DumpName)); err != nil {
		t.Fatalf("the dump before the failure is gone: %v", err)
	}
	listing, err := failing.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Backups) != 1 || listing.Backups[0].Status != StatusIncomplete {
		t.Fatalf("listing = %+v, want one incomplete folder", listing.Backups)
	}

	// The folder's own objects carry real modification times; the day has
	// to pass for those too, so move the clock well past both.
	opts.Destination = NewStoreDestination(store)
	opts.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	result, err := backupUnderLock(t, New(opts), KindNightly)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Swept, []string{failedID}) {
		t.Fatalf("swept %v, want [%s]", result.Swept, failedID)
	}
	if objects, _, _ := store.List(ctx, folderPrefix(serverID, failedID), "", 0); len(objects) != 0 {
		t.Fatalf("swept folder still holds %d objects", len(objects))
	}
}

// backupUnderLock takes the subsystem lock and backs up under it.
func backupUnderLock(t *testing.T, svc *Service, kind Kind) (RunResult, error) {
	t.Helper()
	held, err := svc.opts.Locker.TryLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release(t.Context())
	return svc.Backup(t.Context(), held, kind, "", nil)
}

// TestBackupAndDeleteWithOnePoolConnectionDB: Silo runs with a database pool
// of one connection. The lock's session is not a pool connection, and status
// writes run on it, so a backup (snapshot transaction, settings reads,
// rotation) and a delete finish instead of waiting for themselves.
func TestBackupAndDeleteWithOnePoolConnectionDB(t *testing.T) {
	_, dsn := backuptest.Database(t)
	tools := testTools(t)
	// A deadlock on the one connection shows as this deadline, not a hang.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resetStatus := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM backup_status`) }
	resetStatus()
	t.Cleanup(resetStatus)
	cipher, err := secret.New([]byte("one-connection-test-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	// The stores as the server opens them: the first write records the
	// storage identity through the settings, and so through the pool.
	stores, _, err := blobstore.Open(ctx, blobstore.Options{Backend: blobstore.BackendLocal, LocalPath: t.TempDir(), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	store := stores.Operational
	serverID := "one-conn-" + randomHex(t, 4)
	svc := New(Options{
		Pool: pool, DatabaseURL: dsn, Settings: settings,
		Stores: stores, Destination: NewStoreDestination(store),
		Locker: PostgresLocker{Pool: pool}, Status: PostgresStatus{Pool: pool},
		ServerID:  func(context.Context) (string, error) { return serverID, nil },
		FindTools: func(context.Context) (Tools, error) { return tools, nil },
	})
	result, err := svc.RunTask(ctx, nil)
	if err != nil || result.ID == "" {
		t.Fatalf("backup with one pool connection = %+v, %v", result, err)
	}
	listing, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if listing.LastRun == nil || listing.LastRun.ID != result.ID || listing.LastRun.Status != string(result.Status) || listing.Active != nil {
		t.Fatalf("published status = last %+v, active %+v", listing.LastRun, listing.Active)
	}
	if err := svc.Delete(ctx, result.ID); err != nil {
		t.Fatalf("delete with one pool connection: %v", err)
	}
	if objects, _, _ := store.List(ctx, folderPrefix(serverID, result.ID), "", 0); len(objects) != 0 {
		t.Fatalf("the deleted backup still holds %d objects", len(objects))
	}
}

// setBackupBucket gives the test server a backup bucket of its own, which a
// copy restored from its backup must not use.
func setBackupBucket(t *testing.T, pool *pgxpool.Pool, settings *catalog.EncryptedSettingsRepo) {
	t.Helper()
	setSetting(t, pool, settings, config.BackupDestinationSettingKey, DestinationS3)
	setSetting(t, pool, settings, config.BackupS3SecretKeySettingKey, "bucket-secret")
}

// checkNoBackupBucket fails unless a restored copy has no backup bucket
// settings, so it never writes to the original's bucket.
func checkNoBackupBucket(t *testing.T, settings *catalog.EncryptedSettingsRepo) {
	t.Helper()
	bucket, err := settings.GetMany(t.Context(), config.BackupDestinationSettingKey, config.BackupS3SecretKeySettingKey)
	if err != nil || len(bucket) != 0 {
		t.Fatalf("the copy's backup bucket settings = %v, %v; want none", bucket, err)
	}
}

func manifestKeyOf(t *testing.T, masterKey []byte) []byte {
	t.Helper()
	key, err := secret.BackupManifestKey(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestEncryptedS3RoundTripDB is the round trip through an S3 destination:
// every object but the manifest is stored encrypted, the backup previews
// on this server, and the full restore fetches it from the bucket,
// decrypting as it streams, into an empty database and a bucket prefix.
// Another SECRET_KEY cannot fetch it, and a damaged object fails the fetch.
func TestEncryptedS3RoundTripDB(t *testing.T) {
	pool, dsn := backuptest.Database(t)
	tools := testTools(t)
	ctx := t.Context()
	masterKey := []byte("s3-round-trip-test-key-0123456789abcdef")
	cipher, err := secret.New(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewArchiveKey(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed := seedRoundTrip(t, pool, settings, store)
	setBackupBucket(t, pool, settings)
	manifestKey := manifestKeyOf(t, masterKey)
	srv := s3test.New(t, testBucket)
	serverID := "s3-round-trip-" + randomHex(t, 4)
	opts := Options{
		KeyCheck: key.Check, ManifestKey: manifestKey,
		Pool: pool, DatabaseURL: dsn, Settings: s3Settings(srv, key.Check),
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker:     PostgresLocker{Pool: pool},
		ServerID:   func(context.Context) (string, error) { return serverID, nil },
		ArchiveKey: key,
		FindTools:  func(context.Context) (Tools, error) { return tools, nil },
	}
	svc := New(opts)
	result, err := backupUnderLock(t, svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	folder := "silo/" + folderPrefix(serverID, result.ID)
	keys := srv.Keys(testBucket, folder)
	if want := []string{"blobs.tar.age", "db.dump.age", ManifestName, "restore-set.tar.age"}; len(keys) != len(want) {
		t.Fatalf("stored %v, want %v", keys, want)
	}
	for _, k := range keys {
		data, _ := srv.Object(testBucket, k)
		if strings.HasSuffix(k, EncryptedSuffix) != isAgeHeader(data) {
			t.Fatalf("%s: encrypted %v", k, isAgeHeader(data))
		}
	}
	manifestData, _ := srv.Object(testBucket, folder+ManifestName)
	m, err := ParseManifest(manifestData)
	if dump, _ := srv.Object(testBucket, folder+"db.dump.age"); err != nil || !m.Encrypted || m.KeyCheck != key.Check || m.TOCCheck == "" || bytes.Contains(dump, []byte("PGDMP")) {
		t.Fatalf("manifest = %+v, %v; or the stored dump holds plaintext", m, err)
	}
	// A preview reads the restore set decrypted and checks it against the
	// stored digest. (The seeded account has no email, which the users
	// section's account reader does not take, so the read stops short of
	// planning.)
	if rs, authenticated, err := svc.previewSet(ctx, restoreSource{backupID: result.ID}, false); err != nil || !authenticated || rs.Index.ServerID != serverID {
		t.Fatalf("reading the encrypted restore set: %v", err)
	}
	otherKey, err := NewArchiveKey([]byte("another-servers-key-0123456789abcdefgh"))
	if err != nil {
		t.Fatal(err)
	}

	// The command names the folder by its whole key, as Location shows it.
	whole := s3TestConfig(srv)
	whole.KeyPrefix = ""
	objects := BucketObjects{Client: whole.Client(), Prefix: strings.TrimSuffix(folder, "/")}
	if _, err := FetchSource(ctx, objects, m, FetchOptions{Key: otherKey, WorkDir: t.TempDir()}); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("fetch with another key = %v, want ErrKeyMismatch", err)
	}
	src, err := FetchSource(ctx, objects, m, FetchOptions{Key: key, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	into := createDatabase(t, pool, dsn)
	restored := S3Config{Endpoint: srv.URL, Bucket: testBucket, KeyPrefix: "restored", AccessKey: "a", SecretKey: "s", PathStyle: true}
	if _, err := RestoreSeparate(ctx, src, PGLoader{Tools: tools}, SeparateOptions{
		Validate: ValidateOptions{KeyCheck: key.Check, ManifestKey: manifestKey}, Into: into, BlobsInto: "s3://backups/restored",
		BlobStore: blobstore.NewS3(restored.Client()),
	}); err != nil {
		t.Fatal(err)
	}
	for k, want := range seed.blobs {
		if got, ok := srv.Object(testBucket, "restored/"+k); !ok || !bytes.Equal(got, want) {
			t.Fatalf("restored blob %s = %q, want %q", k, got, want)
		}
	}
	restoredPool, err := pgxpool.New(ctx, into)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredPool.Close()
	restoredSettings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(restoredPool), cipher)
	if got, err := restoredSettings.Get(ctx, seed.secretKey); err != nil || got != seed.secretValue {
		t.Fatalf("restored encrypted setting = %q, %v; want %q", got, err, seed.secretValue)
	}
	checkNoBackupBucket(t, restoredSettings)

	// A damaged object fails the fetch before anything is restored.
	damaged, _ := srv.Object(testBucket, folder+"blobs.tar.age")
	srv.Put(testBucket, folder+"blobs.tar.age", append(damaged[:len(damaged)-1:len(damaged)-1], damaged[len(damaged)-1]^1))
	if _, err := FetchSource(ctx, objects, m, FetchOptions{Key: key, WorkDir: t.TempDir()}); err == nil {
		t.Fatal("a damaged blobs.tar.age fetched")
	}
}

// TestFullRestoresAreSerializedDB: two full restores of one database cannot
// run at once. Each control connection is on the maintenance database, so
// neither preflight sees the other; the restore lock refuses the second
// before it reads the backup or changes anything, names the holder, and
// frees when its holder returns or its connection dies.
func TestFullRestoresAreSerializedDB(t *testing.T) {
	_, dsn := backuptest.Database(t)
	ctx := t.Context()
	live, err := databaseName(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connect := func() *PGControl {
		ctl, err := ConnectControl(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ctl.Close(context.Background()) })
		return ctl
	}
	first, second := connect(), connect()
	unlock, err := first.LockRestore(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	err = RestoreSwap(ctx, &Source{Manifest: &Manifest{}}, nil, second, SwapOptions{LiveURL: dsn})
	if !errors.Is(err, ErrRestoreRunning) || !strings.Contains(err.Error(), "process ") {
		t.Fatalf("a second restore = %v, want ErrRestoreRunning naming the holder", err)
	}
	other, err := second.LockRestore(ctx, live+"_other")
	if err != nil {
		t.Fatalf("another database's restore: %v", err)
	}
	other(ctx)
	unlock(ctx)
	if _, err := second.LockRestore(ctx, live); err != nil {
		t.Fatalf("after the first returned: %v", err)
	}
	// A restore that dies, still holding it, frees the lock with its
	// connection.
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	third := connect()
	if unlock, err := third.LockRestore(ctx, live); err != nil {
		t.Fatalf("after the holder's connection closed: %v", err)
	} else {
		unlock(ctx)
	}
}

// openTestSource opens a backup folder and removes its private copies when
// the test ends.
func openTestSource(t *testing.T, dir string) *Source {
	t.Helper()
	src, err := OpenSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// TestServerSpecificSettingsComeFromTheLiveServerDB: after a full restore
// every server-specific setting is the live server's, whatever the backup
// held, and one the live server does not have is gone so its default
// applies; signing keys keep the values the restore rotated in. Each
// setting of the classification is checked, on databases of their own.
func TestServerSpecificSettingsComeFromTheLiveServerDB(t *testing.T) {
	pool, dsn := backuptest.Database(t)
	ctx := t.Context()
	open := func() (*pgxpool.Pool, string) {
		url := createDatabase(t, pool, dsn)
		db, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		if _, err := db.Exec(ctx, `CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		return db, url
	}
	live, liveURL := open()
	restored, restoredURL := open()
	var keys []string
	for _, key := range settingRegistry() {
		if kind, _ := SettingKindOf(key); kind == SettingKindServerSpecific && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	if len(keys) < 10 {
		t.Fatalf("only %d server-specific settings registered", len(keys))
	}
	for i, key := range keys {
		if _, err := restored.Exec(ctx, `INSERT INTO server_settings VALUES ($1, 'backup')`, key); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if _, err := live.Exec(ctx, `INSERT INTO server_settings VALUES ($1, 'live')`, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := (PGLoader{}).CarryServerSpecificSettings(ctx, restoredURL, liveURL); err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		var got *string
		if err := restored.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&got); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		_, rotated := rotatedSettings[key]
		switch {
		case rotated:
			if got == nil || *got != "backup" {
				t.Errorf("%s = %v, want the restore's own value kept", key, got)
			}
		case i%2 == 0 && (got == nil || *got != "live"):
			t.Errorf("%s = %v, want the live value", key, got)
		case i%2 == 1 && got != nil:
			t.Errorf("%s = %q, want it gone: the live server has none", key, *got)
		}
	}
}

// TestControlConnectionIsNeverOnTheLiveDatabaseDB: a deployment whose live
// database is named postgres gets its control connection on template1,
// since a swap renames the live database.
func TestControlConnectionIsNeverOnTheLiveDatabaseDB(t *testing.T) {
	_, dsn := backuptest.Database(t)
	ctx := t.Context()
	for live, want := range map[string]string{maintenancePostgres: "template1", "silo": maintenancePostgres} {
		url, err := WithDatabase(dsn, live)
		if err != nil {
			t.Fatal(err)
		}
		ctl, err := ConnectControl(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		var on string
		err = ctl.Conn.QueryRow(ctx, `SELECT current_database()`).Scan(&on)
		_ = ctl.Close(context.Background())
		if err != nil || on != want || ctl.Like != live {
			t.Fatalf("live %s: control on %q (%v), like %q; want %s", live, on, err, ctl.Like, want)
		}
	}
}

// TestCustomPoliciesComeFromTheLiveServerDB: a full restore runs the live
// server's custom access policies, as one unit: a newer policy and its
// running version replace the backup's. With no live policy, the backup's
// come back turned off and are reported. Each side is a database of its
// own.
func TestCustomPoliciesComeFromTheLiveServerDB(t *testing.T) {
	ctx := t.Context()
	// Two migrated databases: the policy tables' ids are GENERATED ALWAYS
	// identities there, which an insert of explicit ids must override.
	open := func() (*pgxpool.Pool, string) { return backuptest.Database(t) }
	policy := func(db *pgxpool.Pool, version int, author *int) {
		t.Helper()
		var doc, ver int64
		if err := db.QueryRow(ctx, `INSERT INTO policy_documents (domain, name, enabled) VALUES ('scope', 'house rules', true) RETURNING id`).Scan(&doc); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `INSERT INTO policy_document_versions (document_id, version_number, rego_source, source_sha256, compiled_ok, created_by_user_id)
			VALUES ($1, $2, 'package x', 'sha', true, $3) RETURNING id`, doc, version, author).Scan(&ver); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE policy_documents SET active_version_id = $2 WHERE id = $1`, doc, ver); err != nil {
			t.Fatal(err)
		}
	}
	live, liveURL := open()
	restored, restoredURL := open()
	var author int
	if err := live.QueryRow(ctx, `INSERT INTO users (username, password_hash, role, permissions) VALUES ('policy-author', 'h', 'admin', '{}') RETURNING id`).Scan(&author); err != nil {
		t.Fatal(err)
	}
	policy(restored, 1, nil)
	policy(live, 2, &author) // the live author is not in the restored data
	if _, err := (PGLoader{}).CarryPolicies(ctx, restoredURL, liveURL); err != nil {
		t.Fatal(err)
	}
	var running int
	var by *int
	if err := restored.QueryRow(ctx, `SELECT v.version_number, v.created_by_user_id FROM policy_documents d JOIN policy_document_versions v ON v.id = d.active_version_id
		WHERE d.enabled`).Scan(&running, &by); err != nil || running != 2 || by != nil {
		t.Fatalf("the restored database runs version %d by %v (%v), want the live version 2 without its absent author", running, by, err)
	}
	if _, err := live.Exec(ctx, `DELETE FROM policy_documents`); err != nil {
		t.Fatal(err)
	}
	off, err := (PGLoader{}).CarryPolicies(ctx, restoredURL, liveURL)
	if err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM policy_documents WHERE enabled`).Scan(&enabled); err != nil || enabled != 0 || len(off) != 1 {
		t.Fatalf("with no live policy: %d enabled (%v), reported %v; want the backup's off and reported", enabled, err, off)
	}
}

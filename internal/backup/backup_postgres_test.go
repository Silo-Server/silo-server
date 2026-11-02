package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/s3client/s3test"
	"github.com/Silo-Server/silo-server/internal/secret"
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
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_profiles WHERE id = $1`, profileID) })
	seed.blobs = map[string][]byte{
		posterKey: []byte("poster " + suffix),
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
		Locker:    PostgresLocker{Pool: pool},
		ServerID:  func(context.Context) (string, error) { return serverID, nil },
		KeyCheck:  keyCheck,
		FindTools: func(context.Context) (Tools, error) { return tools, nil },
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

	src, err := OpenSource(filepath.Join(root, "backups", serverID, result.ID))
	if err != nil {
		t.Fatal(err)
	}
	into := createDatabase(t, pool, dsn)
	blobsInto := t.TempDir()
	loader := PGLoader{Tools: tools}
	if _, err := RestoreSeparate(ctx, src, loader, SeparateOptions{
		Validate: ValidateOptions{KeyCheck: keyCheck}, Into: into, BlobsInto: blobsInto,
	}); err != nil {
		t.Fatal(err)
	}

	in, err := loader.Inspect(ctx, into)
	if err != nil {
		t.Fatal(err)
	}
	// Everything the manifest counts, less the storage identities the
	// restore cleared.
	want := slices.Clone(src.Manifest.Tables)
	for i := range want {
		if want[i].Name == "server_settings" {
			want[i].Rows -= int64(len(storageIdentities))
		}
	}
	if in.GooseVersion != src.Manifest.GooseVersion || !slices.Equal(in.Tables, want) {
		t.Fatalf("restored inventory %d %+v, want %d %+v", in.GooseVersion, in.Tables, src.Manifest.GooseVersion, want)
	}
	if len(src.Manifest.AppliedMigrations) == 0 || !slices.Equal(in.Applied, src.Manifest.AppliedMigrations) {
		t.Fatalf("restored applied migrations %v, manifest %v", in.Applied, src.Manifest.AppliedMigrations)
	}
	for key, want := range seed.blobs {
		got, err := os.ReadFile(filepath.Join(blobsInto, filepath.FromSlash(key)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored blob %s = %q, %v; want %q", key, got, err, want)
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

	if err := VerifyBackup(ctx, src, loader, into, ValidateOptions{KeyCheck: keyCheck}, io.Discard); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("verify into a used database = %v, want refusal", err)
	}
	// Another SECRET_KEY needs the operator's explicit acceptance.
	if _, err := RestoreSeparate(ctx, src, loader, SeparateOptions{
		Validate: ValidateOptions{KeyCheck: "0000-0000-0000-0000"}, Into: createDatabase(t, pool, dsn), DatabaseOnly: true,
	}); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("restore with another key = %v, want ErrKeyMismatch", err)
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
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverID := "one-conn-" + randomHex(t, 4)
	svc := New(Options{
		Pool: pool, DatabaseURL: dsn, Settings: catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher),
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
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
	srv := s3test.New(t, testBucket)
	serverID := "s3-round-trip-" + randomHex(t, 4)
	opts := Options{
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
	if rs, err := svc.previewSet(ctx, restoreSource{backupID: result.ID}); err != nil || rs.Index.ServerID != serverID {
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
		Validate: ValidateOptions{KeyCheck: key.Check}, Into: into, BlobsInto: "s3://backups/restored",
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

	// A damaged object fails the fetch before anything is restored.
	damaged, _ := srv.Object(testBucket, folder+"blobs.tar.age")
	srv.Put(testBucket, folder+"blobs.tar.age", append(damaged[:len(damaged)-1:len(damaged)-1], damaged[len(damaged)-1]^1))
	if _, err := FetchSource(ctx, objects, m, FetchOptions{Key: key, WorkDir: t.TempDir()}); err == nil {
		t.Fatal("a damaged blobs.tar.age fetched")
	}
}

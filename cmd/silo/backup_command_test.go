package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/envutil"
	"github.com/Silo-Server/silo-server/internal/s3client/s3test"
	"github.com/Silo-Server/silo-server/internal/secret"
)

type mapSettingsReader map[string]string

func (m mapSettingsReader) Get(_ context.Context, key string) (string, error) { return m[key], nil }

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// bucketBackup is an encrypted backup a server wrote to a fake bucket.
type bucketBackup struct {
	srv                *s3test.Server
	masterKey          string
	serverID, id, from string
	mark, markData     string
	newDatabase        func() string
}

// backupToBucket takes an encrypted backup of the test database to a fake
// bucket, with one referenced file (a branding mark, as the backup
// package's round trip seeds) so blobs.tar has something to restore.
func backupToBucket(t *testing.T) *bucketBackup {
	t.Helper()
	ctx := t.Context()
	tools, err := backup.FindTools(ctx)
	if err != nil {
		t.Skipf("PostgreSQL client tools: %v", err)
	}
	pool, dsn := backuptest.Database(t)
	b := &bucketBackup{srv: s3test.New(t, "backups"), masterKey: "cli-s3-test-secret-key-0123456789abcdef"}
	b.newDatabase = func() string {
		name := "silo_backup_cli_" + randomSuffix(t)
		if _, err := pool.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`) })
		raw, err := backup.WithDatabase(dsn, name)
		if err != nil {
			t.Fatal(err)
		}
		// The commands refuse a password on their command line.
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if password, ok := u.User.Password(); ok {
			t.Setenv("PGPASSWORD", password)
			u.User = url.User(u.User.Username())
		}
		return u.String()
	}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	suffix := randomSuffix(t)
	b.mark, b.markData = "branding/mark-light/"+suffix+".webp", "mark "+suffix
	if err := store.Put(ctx, b.mark, []byte(b.markData)); err != nil {
		t.Fatal(err)
	}
	var previous *string
	_ = pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = 'branding.mark_light_ref'`).Scan(&previous)
	if _, err := pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ('branding.mark_light_ref', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, suffix+".webp"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if previous != nil {
			_, _ = pool.Exec(context.Background(), `UPDATE server_settings SET value = $1 WHERE key = 'branding.mark_light_ref'`, *previous)
			return
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = 'branding.mark_light_ref'`)
	})

	key, err := backup.NewArchiveKey([]byte(b.masterKey))
	if err != nil {
		t.Fatal(err)
	}
	manifestKey, err := secret.BackupManifestKey([]byte(b.masterKey))
	if err != nil {
		t.Fatal(err)
	}
	b.serverID = "cli-s3-" + suffix
	svc := backup.New(backup.Options{
		Pool: pool, DatabaseURL: dsn,
		Settings: mapSettingsReader{
			config.BackupDestinationSettingKey: backup.DestinationS3,
			config.BackupS3EndpointSettingKey:  b.srv.URL, config.BackupS3BucketSettingKey: "backups",
			config.BackupS3KeyPrefixSettingKey: "silo", config.BackupS3AccessKeySettingKey: "a",
			config.BackupS3SecretKeySettingKey: "s", config.BackupS3PathStyleSettingKey: "true",
			config.BackupS3KeyAcknowledgedSettingKey: key.Check,
		},
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: backup.NewStoreDestination(store),
		Locker:     backup.PostgresLocker{Pool: pool},
		ServerID:   func(context.Context) (string, error) { return b.serverID, nil },
		ArchiveKey: key, KeyCheck: key.Check, ManifestKey: manifestKey,
		FindTools: func(context.Context) (backup.Tools, error) { return tools, nil },
	})
	// Wait for the lock: the backup package's database tests take it too,
	// and the contract runner runs both packages at once.
	held, err := backup.PostgresLocker{Pool: pool}.Lock(ctx, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Backup(ctx, held, backup.KindManual, "", nil)
	held.Release(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.id = result.ID
	b.from = "s3://backups/silo/backups/" + b.serverID + "/" + b.id
	return b
}

// dirNames lists a directory's entries.
func dirNames(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestBackupCommandsReadS3DB: `silo backup verify`, `restore`, and
// `decrypt` read an encrypted backup straight from its bucket with the
// credentials in the environment, decrypting with SECRET_KEY, and restore
// files into a bucket; with another SECRET_KEY the source and decrypt paths
// refuse and write nothing.
func TestBackupCommandsReadS3DB(t *testing.T) {
	b := backupToBucket(t)
	ctx := t.Context()
	// Each command runs from an empty directory: the fetched copy is made
	// there, and nothing may be left behind.
	work := t.TempDir()
	t.Chdir(work)
	for k, v := range map[string]string{
		"SECRET_KEY": b.masterKey, envBackupS3Endpoint: b.srv.URL, envBackupS3Region: "us-east-1",
		envBackupS3AccessKey: "a", envBackupS3SecretKey: "s", envBackupS3PathStyle: "true",
		// A shell that sets the file variants would make each read ambiguous.
		"SECRET_KEY_FILE": "", "DATABASE_URL_FILE": "", envBackupS3AccessKey + "_FILE": "", envBackupS3SecretKey + "_FILE": "",
	} {
		t.Setenv(k, v)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runBackupCommand(ctx, append(args, "-env", filepath.Join(work, "no.env")), &out)
		return out.String(), err
	}

	if out, err := run("verify", "--from", b.from, "--scratch", b.newDatabase()); err != nil || !strings.Contains(out, "Verified") {
		t.Fatalf("verify from the bucket = %v\n%s", err, out)
	}
	if out, err := run("restore", "--from", b.from, "--into", b.newDatabase(), "--blobs-into", "s3://backups/restored"); err != nil {
		t.Fatalf("restore from the bucket = %v\n%s", err, out)
	}
	if got, ok := b.srv.Object("backups", "restored/"+b.mark); !ok || string(got) != b.markData {
		t.Fatalf("restored mark = %q, %v; bucket holds %v", got, ok, b.srv.Keys("backups", "restored/"))
	}
	if left := dirNames(work); len(left) != 0 {
		t.Fatalf("the commands left %v in the working directory", left)
	}
	if _, err := run("decrypt", b.from+"/db.dump.age", "-o", "db.dump"); err != nil {
		t.Fatal(err)
	}
	if dump, err := os.ReadFile("db.dump"); err != nil || !bytes.HasPrefix(dump, []byte("PGDMP")) {
		t.Fatalf("decrypted dump = %d bytes, %v", len(dump), err)
	}

	t.Setenv("SECRET_KEY", "another-servers-secret-key-0123456789abc")
	for name, args := range map[string][]string{
		"verify":  {"verify", "--from", b.from, "--scratch", b.newDatabase()},
		"decrypt": {"decrypt", b.from + "/restore-set.tar.age", "-o", "restore-set.tar"},
	} {
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), "different SECRET_KEY") {
			t.Errorf("%s with another key = %v, want a key refusal", name, err)
		}
	}
	if left := dirNames(work); len(left) != 1 || left[0] != "db.dump" {
		t.Fatalf("refused commands left %v", left)
	}
}

// `silo secret check` answers once a key typed or pasted on stdin ends its
// line, with no end of input needed, and `--file` reads a key file the way
// SECRET_KEY_FILE does, including an extra trailing newline or a wrapped
// multi-line key.
func TestSecretCheck(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	checkOf := func(t *testing.T, key string) string {
		t.Helper()
		check, err := secret.KeyCheckValue([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		return check
	}

	t.Run("stdin", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			in, typed := io.Pipe()
			defer func() { _ = typed.Close() }()
			var out bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- runSecretCommand([]string{"check"}, in, &out) }()
			if _, err := io.WriteString(typed, key+"\n"); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			select {
			case err := <-done:
				if got := strings.TrimSpace(out.String()); err != nil || got != checkOf(t, key) {
					t.Fatalf("secret check = %q, %v; want %q", got, err, checkOf(t, key))
				}
			default:
				t.Fatal("secret check is still waiting for the end of input after the key's line")
			}
		})
	})

	dir := t.TempDir()
	for name, raw := range map[string]string{
		"file double newline": key + "\n\n",
		"file multiline":      key + "\nsecond-line-of-key\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := envutil.ReadSecretFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runSecretCommand([]string{"check", "--file", path}, strings.NewReader(""), &out); err != nil {
				t.Fatal(err)
			}
			if got, want := strings.TrimSpace(out.String()), checkOf(t, loaded); got != want {
				t.Fatalf("secret check --file = %q, want %q (from ReadSecretFile)", got, want)
			}
		})
	}
}

// Missing or conflicting _FILE variables fail before restore reads --from.
func TestBackupRestoreChecksSecretFilesBeforeOpeningSource(t *testing.T) {
	dir := t.TempDir()
	for k, v := range map[string]string{
		"SECRET_KEY":        "0123456789abcdef0123456789abcdef-at-rest",
		"SECRET_KEY_FILE":   "",
		"DATABASE_URL":      "postgres://example.invalid/silo",
		"DATABASE_URL_FILE": "",
		"REDIS_URL":         "",
		"REDIS_URL_FILE":    filepath.Join(dir, "missing-redis-url"),
	} {
		t.Setenv(k, v)
	}
	var out bytes.Buffer
	err := runBackupCommand(t.Context(), []string{
		"restore", "--from", filepath.Join(dir, "missing-backup"), "-env", filepath.Join(dir, "no.env"),
	}, &out)
	if err == nil || !strings.Contains(err.Error(), "REDIS_URL_FILE") {
		t.Fatalf("error = %v, want REDIS_URL_FILE error before reading --from", err)
	}
}

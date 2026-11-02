package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/s3client/s3test"
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
		url, err := backup.WithDatabase(dsn, name)
		if err != nil {
			t.Fatal(err)
		}
		return url
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
		ArchiveKey: key,
		FindTools:  func(context.Context) (backup.Tools, error) { return tools, nil },
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

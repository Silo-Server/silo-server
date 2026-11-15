package backup

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/s3client/s3test"
)

// The before-upgrade sequence on an older release's schema, through the
// server's own wiring, is cmd/silo's TestBeforeMigrateBackupOnAnOldSchemaDB.

// unreadableSettings fails reads of one key, as a database error after the
// startup settings load would.
type unreadableSettings struct {
	*mapSettings
	key string
}

func (u unreadableSettings) Get(ctx context.Context, key string) (string, error) {
	if key == u.key {
		return "", errors.New("connection reset")
	}
	return u.mapSettings.Get(ctx, key)
}

// upgradeOptions is a service over a test database of its own writing to a
// directory of its own, under a server id of its own.
func upgradeOptions(t *testing.T) (Options, blobstore.Store) {
	t.Helper()
	pool, dsn := backuptest.Database(t)
	tools := testTools(t)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverID := "upgrade-" + randomHex(t, 4)
	return Options{
		Pool: pool, DatabaseURL: dsn, Settings: &mapSettings{values: map[string]string{}},
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker:    PostgresLocker{Pool: pool},
		ServerID:  func(context.Context) (string, error) { return serverID, nil },
		FindTools: func(context.Context) (Tools, error) { return tools, nil },
	}, store
}

func storedObjects(t *testing.T, store blobstore.Store) []blobstore.ObjectInfo {
	t.Helper()
	objects, _, err := store.List(t.Context(), RootPrefix, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

// TestBeforeUpgradeFailsClosedOrSkipsDB: a backup that fails, or a setting
// that cannot be read, returns the reason with the override and gives the
// lock back; only settings that were read can skip it, and each skip writes
// nothing and keeps the lock for the migrations. A nightly run that cannot
// read its destination fails rather than writing to the default one.
func TestBeforeUpgradeFailsClosedOrSkipsDB(t *testing.T) {
	ctx := t.Context()
	archiveKey, err := NewArchiveKey([]byte("before-upgrade-skip-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	unconfirmedS3 := func(o *Options) *mapSettings {
		o.ArchiveKey = archiveKey
		return &mapSettings{values: map[string]string{
			config.BackupDestinationSettingKey: DestinationS3,
			config.BackupS3EndpointSettingKey:  "http://127.0.0.1:1", config.BackupS3BucketSettingKey: testBucket,
		}}
	}

	failures := map[string]func(*Options){
		"failing destination": func(o *Options) {
			o.Destination = failingDestination{Destination: o.Destination, failName: DumpName}
		},
		"unreadable backup.enabled": func(o *Options) {
			o.Settings = unreadableSettings{&mapSettings{values: map[string]string{}}, config.BackupEnabledSettingKey}
		},
		"unreadable key acknowledgement": func(o *Options) {
			o.Settings = unreadableSettings{unconfirmedS3(o), config.BackupS3KeyAcknowledgedSettingKey}
		},
	}
	for name, configure := range failures {
		t.Run(name, func(t *testing.T) {
			opts, store := upgradeOptions(t)
			configure(&opts)
			held, err := New(opts).BeforeUpgrade(ctx, UpgradeOptions{})
			if err == nil {
				held.Release(ctx)
				t.Fatal("BeforeUpgrade succeeded")
			}
			if !strings.Contains(err.Error(), SkipBeforeUpgradeEnv) {
				t.Errorf("error %q does not name %s", err, SkipBeforeUpgradeEnv)
			}
			other, err := opts.Locker.TryLock(ctx)
			if err != nil {
				t.Fatalf("the lock was kept after the failure: %v", err)
			}
			other.Release(ctx)
			if name != "failing destination" {
				if objects := storedObjects(t, store); len(objects) != 0 {
					t.Fatalf("destination holds %+v, want nothing", objects)
				}
			}
		})
	}

	skips := map[string]func(*Options, *UpgradeOptions){
		SkipBeforeUpgradeEnv: func(_ *Options, u *UpgradeOptions) { u.Skip = true },
		"backups off": func(o *Options, _ *UpgradeOptions) {
			o.Settings = &mapSettings{values: map[string]string{config.BackupEnabledSettingKey: "false"}}
		},
		"SQLite user store":    func(o *Options, _ *UpgradeOptions) { o.UserStoreBackend = func() string { return "sqlite" } },
		"no destination":       func(o *Options, _ *UpgradeOptions) { o.Destination = nil },
		"S3 key not confirmed": func(o *Options, _ *UpgradeOptions) { o.Settings = unconfirmedS3(o) },
	}
	for name, configure := range skips {
		t.Run(name, func(t *testing.T) {
			opts, store := upgradeOptions(t)
			var upgrade UpgradeOptions
			configure(&opts, &upgrade)
			held, err := New(opts).BeforeUpgrade(ctx, upgrade)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := opts.Locker.TryLock(ctx); !errors.Is(err, ErrBusy) {
				t.Errorf("TryLock after a skip = %v, want ErrBusy: the migrations run under the lock", err)
			}
			held.Release(ctx)
			if objects := storedObjects(t, store); len(objects) != 0 {
				t.Fatalf("destination holds %+v, want nothing", objects)
			}
		})
	}

	t.Run("nightly with an unreadable destination", func(t *testing.T) {
		opts, store := upgradeOptions(t)
		opts.Settings = unreadableSettings{unconfirmedS3(&opts), config.BackupDestinationSettingKey}
		held, err := opts.Locker.TryLock(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release(ctx)
		if _, err := New(opts).Backup(ctx, held, KindNightly, "", nil); err == nil {
			t.Fatal("the nightly backup ran without reading its destination")
		}
		if objects := storedObjects(t, store); len(objects) != 0 {
			t.Fatalf("the default destination holds %+v, want nothing", objects)
		}
	})
}

// blockingDestination holds the dump's upload until release is closed, as a
// long nightly backup on another server would hold the lock.
type blockingDestination struct {
	Destination
	entered, release chan struct{}
	once             sync.Once
}

func (d *blockingDestination) Put(ctx context.Context, key string, r io.Reader) error {
	if strings.HasSuffix(key, "/"+DumpName) {
		d.once.Do(func() { close(d.entered) })
		select {
		case <-d.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d.Destination.Put(ctx, key, r)
}

// TestBeforeUpgradeWaitsForANightlyBackupDB: behind a running nightly backup
// the before-upgrade backup waits for the lock; a wait that runs out fails
// naming SILO_MIGRATION_WAIT, and a longer one backs up once the nightly run
// finishes. The nightly slot that run recorded neither skips the
// before-upgrade backup nor changes with it.
func TestBeforeUpgradeWaitsForANightlyBackupDB(t *testing.T) {
	ctx := t.Context()
	opts, store := upgradeOptions(t)
	// Both publish status, so the nightly run records its slot.
	opts.Status = PostgresStatus{Pool: opts.Pool}
	nightlyOpts := opts
	blocking := &blockingDestination{Destination: opts.Destination, entered: make(chan struct{}), release: make(chan struct{})}
	nightlyOpts.Destination = blocking
	nightly := make(chan error, 1)
	go func() {
		result, err := New(nightlyOpts).RunTask(ctx, nil)
		if err == nil && result.Skipped != "" {
			err = errors.New(result.Skipped)
		}
		nightly <- err
	}()
	awaitBefore(t, blocking.entered, nightly, "the nightly backup to start writing its dump")

	svc := New(opts)
	if held, err := svc.BeforeUpgrade(ctx, UpgradeOptions{Wait: 300 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "SILO_MIGRATION_WAIT") {
		if held != nil {
			held.Release(ctx)
		}
		t.Fatalf("BeforeUpgrade behind a nightly backup = %v, want a timeout naming SILO_MIGRATION_WAIT", err)
	}
	upgraded := make(chan error, 1)
	go func() {
		held, err := svc.BeforeUpgrade(ctx, UpgradeOptions{Wait: time.Minute})
		if err == nil {
			held.Release(ctx)
		}
		upgraded <- err
	}()
	close(blocking.release)
	if err := await(t, nightly, "the nightly backup to finish"); err != nil {
		t.Fatalf("nightly backup: %v", err)
	}
	slot, err := opts.Status.Read(ctx)
	if err != nil || slot.LastNightlySlot == nil {
		t.Fatalf("the nightly run recorded no slot: %+v, %v", slot, err)
	}
	// The slot is done, and the before-upgrade backup runs anyway and
	// leaves it as the nightly run recorded it.
	if err := await(t, upgraded, "the before-upgrade backup to finish"); err != nil {
		t.Fatalf("before-upgrade backup after the nightly one: %v", err)
	}
	if after, err := opts.Status.Read(ctx); err != nil || after.LastNightlySlot == nil || !after.LastNightlySlot.Equal(*slot.LastNightlySlot) {
		t.Fatalf("after the before-upgrade backup the slot is %v (%v), want %v", after.LastNightlySlot, err, *slot.LastNightlySlot)
	}
	serverID, _ := opts.ServerID(ctx)
	folders, err := svc.folders(ctx, target{Destination: NewStoreDestination(store)}, serverID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[Kind]bool{}
	for _, f := range folders {
		if f.manifest != nil {
			kinds[f.kind] = true
		}
	}
	if len(folders) != 2 || !kinds[KindNightly] || !kinds[KindBeforeUpgrade] {
		t.Fatalf("backups = %+v, want a complete nightly and before-upgrade backup", folders)
	}
}

// TestBeforeUpgradeDestinationsDB: to an S3 bucket with the key confirmed,
// the before-upgrade backup is the encrypted dump and the manifest.
func TestBeforeUpgradeDestinationsDB(t *testing.T) {
	ctx := t.Context()
	opts, _ := upgradeOptions(t)
	key, err := NewArchiveKey([]byte("before-upgrade-s3-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	srv := s3test.New(t, testBucket)
	opts.Settings, opts.ArchiveKey = s3Settings(srv, key.Check), key
	held, err := New(opts).BeforeUpgrade(ctx, UpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	held.Release(ctx)
	serverID, _ := opts.ServerID(ctx)
	keys := srv.Keys(testBucket, "silo/"+serverPrefix(serverID))
	if len(keys) != 2 {
		t.Fatalf("stored %v, want db.dump.age and the manifest", keys)
	}
	folder := keys[0][:strings.LastIndex(keys[0], "/")+1]
	data, _ := srv.Object(testBucket, folder+ManifestName)
	m, err := ParseManifest(data)
	if err != nil || m.Kind != KindBeforeUpgrade || !m.Encrypted || len(m.Objects) != 1 || m.Objects[0].Name != DumpName+EncryptedSuffix {
		t.Fatalf("manifest = %+v, %v; want an encrypted before-upgrade dump alone", m, err)
	}
	if dump, _ := srv.Object(testBucket, folder+DumpName+EncryptedSuffix); !isAgeHeader(dump) {
		t.Fatal("the stored before-upgrade dump is not encrypted")
	}
}

// TestBeforeUpgradeRetriesDB: a retry of an upgrade whose migrations failed
// keeps the backup the first attempt took while it is the newest, within
// upgradeReuseWindow, over the same applied migrations; otherwise it takes
// another. Rotation keeps every copy of an upgrade that has not completed,
// and lets them go once a later upgrade finds all their migrations applied.
func TestBeforeUpgradeRetriesDB(t *testing.T) {
	const p1, p2, p3, next = 99990001, 99990002, 99990003, 99990004
	type attempt struct {
		// apply is what the failed attempt before this one left applied.
		apply []int64
		// after is how long after the first attempt this one runs.
		after time.Duration
	}
	tests := []struct {
		name     string
		attempts []attempt
		want     int
	}{
		{name: "a failed migration retried twice", attempts: []attempt{{}, {after: time.Minute}, {after: 2 * time.Minute}}, want: 1},
		{name: "migrations applied between attempts", attempts: []attempt{{}, {apply: []int64{p1}, after: time.Minute}, {apply: []int64{p2}, after: 2 * time.Minute}}, want: 3},
		{name: "a retry after the window", attempts: []attempt{{}, {after: upgradeReuseWindow + time.Minute}, {after: 2 * (upgradeReuseWindow + time.Minute)}}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			opts, store := upgradeOptions(t)
			start := time.Now().UTC().Truncate(time.Second)
			now := start
			opts.Now = func() time.Time { return now }
			svc := New(opts)
			_, applied, err := appliedMigrations(ctx, opts.Pool)
			if err != nil {
				t.Fatal(err)
			}
			binary := append(slices.Clone(applied), p1, p2, p3)
			apply := func(versions ...int64) {
				t.Helper()
				for _, v := range versions {
					if _, err := opts.Pool.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, v); err != nil {
						t.Fatal(err)
					}
				}
			}
			upgrade := func() {
				t.Helper()
				held, err := svc.BeforeUpgrade(ctx, UpgradeOptions{Migrations: binary})
				if err != nil {
					t.Fatal(err)
				}
				held.Release(ctx)
			}
			serverID, _ := opts.ServerID(ctx)
			upgradeCopies := func() int {
				t.Helper()
				folders, err := svc.folders(ctx, target{Destination: NewStoreDestination(store)}, serverID)
				if err != nil {
					t.Fatal(err)
				}
				n := 0
				for _, f := range folders {
					if f.kind == KindBeforeUpgrade && f.manifest != nil {
						n++
					}
				}
				return n
			}
			for _, a := range tt.attempts {
				apply(a.apply...)
				now = start.Add(a.after)
				upgrade()
			}
			if got := upgradeCopies(); got != tt.want {
				t.Fatalf("before-upgrade backups after the attempts = %d, want %d", got, tt.want)
			}
			// The upgrade completes, and the next one rotates as usual.
			_, applied, err = appliedMigrations(ctx, opts.Pool)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range []int64{p1, p2, p3} {
				if _, found := slices.BinarySearch(applied, v); !found {
					apply(v)
				}
			}
			binary = append(binary, next)
			now = now.Add(time.Minute)
			upgrade()
			if got, want := upgradeCopies(), min(tt.want+1, beforeUpgradeKeep); got != want {
				t.Fatalf("before-upgrade backups after the next upgrade = %d, want %d", got, want)
			}
		})
	}
}

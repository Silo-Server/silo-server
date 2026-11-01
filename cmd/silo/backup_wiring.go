package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// newBackupService builds the backup subsystem over the operational store,
// which is the default destination, with the archive key an S3 destination
// encrypts with. A deployment without an operational store (an S3 artwork
// backend with no private bucket) still gets a service, so the Backups page
// can say why there is nowhere to write, or keep backups in an S3 bucket.
func newBackupService(pool *pgxpool.Pool, bc *config.BootstrapConfig, settings *catalog.EncryptedSettingsRepo, deps *api.Dependencies) *backup.Service {
	archiveKey, err := backup.NewArchiveKey(bc.SecretKey)
	if err != nil {
		log.Fatalf("backup archive key: %v", err)
	}
	return backup.New(backup.Options{
		Pool:                pool,
		DatabaseURL:         bc.DatabaseURL,
		Settings:            settings,
		SettingsWriter:      settings,
		Stores:              deps.Blobs,
		Destination:         backup.NewStoreDestination(deps.Blobs.Operational),
		Locker:              backup.PostgresLocker{Pool: pool},
		Status:              backup.PostgresStatus{Pool: pool},
		ServerID:            serveridentity.New(settings).ServerID,
		KeyCheck:            archiveKey.Check,
		ArchiveKey:          archiveKey,
		Cipher:              deps.SecretCipher,
		UserStoreBackend:    func() string { return deps.CurrentConfig().UserDB.Backend },
		AllowUnratedContent: config.NewUnratedContentPolicy(settings).AllowUnratedContent,
	})
}

// beforeMigrateBackup is the hook database.MigrateAtStartup runs when
// migrations are pending: the before-upgrade backup, holding the backup lock
// through the migrations. It runs before this release's schema exists, so
// it reads only the server_settings rows the previous release left, with
// defaults for the rest, and publishes no status.
func beforeMigrateBackup(bc *config.BootstrapConfig, pool *pgxpool.Pool, cipher *secret.Cipher, wait time.Duration) func(context.Context) (database.Guard, error) {
	return func(ctx context.Context) (database.Guard, error) {
		skip := backup.SkipBeforeUpgradeFromEnv()
		opts, err := upgradeBackupOptions(ctx, pool, bc, cipher)
		if err != nil {
			if !skip {
				return nil, fmt.Errorf("preparing the backup before database migrations, so the migrations did not run: %w. "+
					"Fix the cause and start again, or set %s=true to start once without it", err, backup.SkipBeforeUpgradeEnv)
			}
			// Skipping needs only the lock the migrations run under.
			opts = backup.Options{Pool: pool, Locker: backup.PostgresLocker{Pool: pool}}
		}
		svc := backup.New(opts)
		// Rotation after the backup keeps a queued restore's source. On a
		// schema the job repository cannot read, rotation is skipped with a
		// warning; the backup itself stands.
		svc.SetRestoreTargets(backup.RestoreTargets{ActiveRestore: api.ActiveBackupRestore(adminjob.NewRepository(pool))})
		held, err := svc.BeforeUpgrade(ctx, backup.UpgradeOptions{Wait: wait, Skip: skip})
		if err != nil {
			return nil, err
		}
		return held, nil
	}
}

// upgradeBackupOptions builds the backup service's options from the
// configuration the database holds now, as the previous release left it.
func upgradeBackupOptions(ctx context.Context, pool *pgxpool.Pool, bc *config.BootstrapConfig, cipher *secret.Cipher) (backup.Options, error) {
	archiveKey, err := backup.NewArchiveKey(bc.SecretKey)
	if err != nil {
		return backup.Options{}, fmt.Errorf("backup archive key: %w", err)
	}
	settings := catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)
	all, err := settings.GetAll(ctx)
	if err != nil {
		return backup.Options{}, fmt.Errorf("reading settings: %w", err)
	}
	cfg, err := config.LoadFromDB(all)
	if err != nil {
		return backup.Options{}, fmt.Errorf("reading settings: %w", err)
	}
	stores, err := storesFromConfig(ctx, backupStoreConfig(cfg))
	if err != nil {
		return backup.Options{}, fmt.Errorf("opening blob storage: %w", err)
	}
	return backup.Options{
		Pool:             pool,
		DatabaseURL:      bc.DatabaseURL,
		Settings:         settings,
		Stores:           stores,
		Destination:      backup.NewStoreDestination(stores.Operational),
		Locker:           backup.PostgresLocker{Pool: pool},
		ServerID:         serveridentity.New(settings).ServerID,
		ArchiveKey:       archiveKey,
		UserStoreBackend: func() string { return cfg.UserDB.Backend },
	}, nil
}

// newProfileTransfer serves profile export and import, which read and write
// the PostgreSQL user store; nil on the SQLite bridge backend, so the
// capability reports not_configured there.
func newProfileTransfer(pool *pgxpool.Pool, settings *catalog.EncryptedSettingsRepo, cfg *config.Config) *backup.ProfileTransfer {
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.UserDB.Backend), "sqlite") {
		return nil
	}
	return backup.NewProfileTransfer(pool, serveridentity.New(settings).ServerID)
}

// backupRestoreExecutor runs backup_restore admin jobs on the backup
// service, translating the runner's fenced transaction into the service's.
type backupRestoreExecutor struct{ svc *backup.Service }

func (e backupRestoreExecutor) ExecuteBackupRestore(ctx context.Context, run adminjob.BackupRestoreRun) (any, error) {
	return e.svc.ExecuteRestore(ctx, backup.RestoreRun{
		Payload: run.Payload,
		Fenced: func(ctx context.Context, fn func(pgx.Tx) (backup.FencedUpdate, error)) error {
			return run.Fenced(ctx, func(tx pgx.Tx) (adminjob.FencedUpdate, error) {
				update, err := fn(tx)
				return adminjob.FencedUpdate(update), err
			})
		},
		Published: run.Published,
	})
}

// syncBackupSchedule keeps the backup task's daily trigger on backup.time:
// now, on another server's settings write (the admin event), and on this
// server's config reloads.
func syncBackupSchedule(ctx context.Context, svc *backup.Service, mgr backup.ScheduleManager, bus cache.EventBus, watcher *nodeconfig.Watcher) {
	sync := func() {
		if err := svc.SyncSchedule(context.WithoutCancel(ctx), mgr); err != nil {
			slog.WarnContext(ctx, "updating the backup schedule failed", "component", "backup", "error", err)
		}
	}
	sync()
	if bus != nil {
		_ = bus.Subscribe(ctx, cache.ChannelAdmin, func(event cache.Event) {
			if event.Type == cache.EventSettingsChanged {
				sync()
			}
		})
	}
	if watcher != nil {
		watcher.OnChange(func(_, _ *config.Config) { sync() })
	}
}

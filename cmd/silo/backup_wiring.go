package main

import (
	"context"
	"log"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// newBackupService builds the backup subsystem over the operational store,
// which is the default destination when it is on local storage. A
// deployment without one (an S3 artwork backend) still gets a service, so
// the Backups page can say why there is nowhere to write.
func newBackupService(pool *pgxpool.Pool, bc *config.BootstrapConfig, settings *catalog.EncryptedSettingsRepo, deps *api.Dependencies) *backup.Service {
	keyCheck, err := secret.KeyCheckValue(bc.SecretKey)
	if err != nil {
		log.Fatalf("backup key check value: %v", err)
	}
	return backup.New(backup.Options{
		Pool:             pool,
		DatabaseURL:      bc.DatabaseURL,
		Settings:         settings,
		Stores:           deps.Blobs,
		Destination:      backup.NewStoreDestination(deps.Blobs.Operational),
		Locker:           backup.PostgresLocker{Pool: pool},
		Status:           backup.PostgresStatus{Pool: pool},
		ServerID:         serveridentity.New(settings).ServerID,
		KeyCheck:         keyCheck,
		Cipher:           deps.SecretCipher,
		UserStoreBackend: func() string { return deps.CurrentConfig().UserDB.Backend },
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

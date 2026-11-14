package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// SkipBeforeUpgradeEnv skips the backup before startup migrations for the
// start it is set on.
const SkipBeforeUpgradeEnv = "SILO_SKIP_BACKUP_BEFORE_MIGRATE"

// SkipBeforeUpgradeFromEnv reads SkipBeforeUpgradeEnv. A value that is not a
// boolean does not skip: the backup is the safe default.
func SkipBeforeUpgradeFromEnv() bool {
	raw := strings.TrimSpace(os.Getenv(SkipBeforeUpgradeEnv))
	if raw == "" {
		return false
	}
	skip, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("ignoring an unreadable value; the backup before migrations still runs",
			"component", "backup", "env", SkipBeforeUpgradeEnv, "value", raw)
		return false
	}
	return skip
}

// UpgradeOptions configure BeforeUpgrade.
type UpgradeOptions struct {
	// Wait bounds the wait for the subsystem lock while another backup or
	// restore holds it; zero or less waits indefinitely.
	Wait time.Duration
	// Skip is SkipBeforeUpgradeEnv: take no backup this start.
	Skip bool
	// Migrations is every migration this binary carries, ascending
	// (database.MigrationVersions). Those the database has not applied are
	// the upgrade the backup's manifest records; nil records none.
	Migrations []int64
}

// upgradeReuseWindow bounds how old a before-upgrade backup a retry of the
// same upgrade keeps instead of taking another. The database matches it
// unless a server ran on it since, which only the previous release could
// have done, so the window keeps that case short; a backup taken anyway
// costs time and space, not the earlier copy (rotation keeps it).
const upgradeReuseWindow = 6 * time.Hour

// upgradeHeartbeat is how often a server waiting for the backup lock, or
// running the before-upgrade backup, logs that it is still at it.
const upgradeHeartbeat = 30 * time.Second

// BeforeUpgrade runs under the migration wrapper when migrations are
// pending. It takes the subsystem lock, waiting for a running backup or
// restore, and takes a before-upgrade backup: the database dump and the
// manifest, read only from what the old schema has. It returns the lock,
// which the caller holds through goose Up so no backup or restore on another
// server reads a schema being migrated.
//
// It fails closed: a backup that fails, or a setting it cannot read, returns
// the error and releases the lock, and startup stops. It skips the backup,
// holding the lock all the same, when opts.Skip is set, when backups are
// off, on the SQLite user store (backups refuse there, and the upgrade that
// imports it must not be blocked), when there is no destination, and when
// the destination is a bucket (encrypted) and the SECRET_KEY is not
// confirmed. Each skip is logged with its reason.
func (s *Service) BeforeUpgrade(ctx context.Context, opts UpgradeOptions) (Held, error) {
	held, err := s.lockForUpgrade(ctx, opts.Wait)
	if err != nil {
		return nil, err
	}
	failed := func(err error) (Held, error) {
		held.Release(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("the backup before database migrations failed, so the migrations did not run: %w. "+
			"Fix the cause and start again, or set %s=true to start once without it", err, SkipBeforeUpgradeEnv)
	}
	reason, warn, err := s.skipBeforeUpgrade(ctx, opts.Skip)
	if err != nil {
		return failed(err)
	}
	if reason != "" {
		level := slog.LevelInfo
		if warn {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "NOT taking a backup before database migrations", "component", "backup", "reason", reason)
		return held, nil
	}
	upgrade, earlier, err := s.upgradeAttempt(ctx, opts.Migrations)
	if err != nil {
		return failed(err)
	}
	if earlier != "" {
		slog.InfoContext(ctx, "keeping the backup taken before an earlier attempt at the same migrations; not taking another",
			"component", "backup", "id", earlier)
		return held, nil
	}
	slog.InfoContext(ctx, "taking a backup before database migrations", "component", "backup")
	work, cancel := withLost(ctx, held)
	defer cancel()
	var step atomic.Value
	step.Store("Starting")
	stop := heartbeat(upgradeHeartbeat, func(elapsed time.Duration) {
		slog.InfoContext(ctx, "the backup before database migrations is still running", "component", "backup",
			"step", step.Load(), "elapsed", elapsed.Round(time.Second).String())
	})
	result, err := s.backupWith(work, held, KindBeforeUpgrade, "", func(_ float64, message string) {
		step.Store(message)
		slog.InfoContext(ctx, "backup before database migrations", "component", "backup", "step", message)
	}, nil, upgrade)
	stop()
	if err != nil {
		if lockLost(work) {
			err = fmt.Errorf("%w: %w", errLockLost, err)
		}
		return failed(err)
	}
	slog.InfoContext(ctx, "backed up the database before migrations", "component", "backup",
		"id", result.ID, "size_bytes", result.SizeBytes, "duration_ms", result.DurationMs)
	return held, nil
}

// upgradeAttempt returns the upgrade a before-upgrade backup now records,
// with the migrations of binary (ascending) the database has not applied,
// and the id of the backup an earlier attempt at the same upgrade took, or
// "" to take one. An earlier attempt's backup is kept when it is this
// server's newest backup, verified and complete, taken within
// upgradeReuseWindow, and records the same pending migrations over the same
// applied ones: the attempt failed, and whatever it changed is what the
// backup is the way back from. A destination that cannot be listed takes a
// backup, which then fails with the reason.
func (s *Service) upgradeAttempt(ctx context.Context, binary []int64) (*UpgradeRecord, string, error) {
	if binary == nil {
		return nil, "", nil
	}
	_, applied, err := appliedMigrations(ctx, s.opts.Pool)
	if err != nil {
		return nil, "", err
	}
	upgrade := &UpgradeRecord{Pending: []int64{}}
	for _, v := range binary {
		if _, found := slices.BinarySearch(applied, v); !found {
			upgrade.Pending = append(upgrade.Pending, v)
		}
	}
	prep, err := s.prepareBackup(ctx, KindBeforeUpgrade, "", s.opts.Now())
	if err != nil {
		// The backup prepares again, and fails with the reason.
		slog.WarnContext(ctx, "could not list earlier backups before database migrations", "component", "backup", "error", err)
		return upgrade, "", nil
	}
	return upgrade, earlierUpgradeBackup(prep.folders, upgrade, applied, s.opts.Now()), nil
}

// earlierUpgradeBackup is upgradeAttempt's choice among folders.
func earlierUpgradeBackup(folders []folder, upgrade *UpgradeRecord, applied []int64, now time.Time) string {
	var newest *folder
	for i := range folders {
		if newest == nil || folders[i].at.After(newest.at) {
			newest = &folders[i]
		}
	}
	if newest == nil || newest.kind != KindBeforeUpgrade || !newest.cleanCopy() || now.Sub(newest.at) > upgradeReuseWindow {
		return ""
	}
	m := newest.manifest
	if m.Upgrade == nil || !slices.Equal(m.Upgrade.Pending, upgrade.Pending) || !slices.Equal(m.AppliedMigrations, applied) {
		return ""
	}
	return newest.id
}

// lockForUpgrade takes the subsystem lock, waiting up to wait behind a
// running backup or restore (another server's nightly run, for one) and
// logging the wait, since the server is not ready meanwhile.
func (s *Service) lockForUpgrade(ctx context.Context, wait time.Duration) (Held, error) {
	held, err := s.opts.Locker.TryLock(ctx)
	if !errors.Is(err, ErrBusy) {
		return held, err
	}
	waitFor := "indefinitely"
	if wait > 0 {
		waitFor = wait.String()
	}
	slog.InfoContext(ctx, "another backup or restore is running; waiting for it before the database migrations (not ready)",
		"component", "backup", "wait", waitFor)
	stop := heartbeat(upgradeHeartbeat, func(elapsed time.Duration) {
		slog.InfoContext(ctx, "still waiting for another backup or restore before the database migrations (not ready)",
			"component", "backup", "elapsed", elapsed.Round(time.Second).String())
	})
	held, err = s.opts.Locker.Lock(ctx, wait)
	stop()
	if errors.Is(err, ErrBusy) {
		return nil, fmt.Errorf("another backup or restore held the backup lock for longer than %s (SILO_MIGRATION_WAIT); let it finish, then start again", wait)
	}
	return held, err
}

// heartbeat calls beat with the elapsed time every interval until the
// returned stop is called; stop waits for a beat in progress.
func heartbeat(interval time.Duration, beat func(elapsed time.Duration)) (stop func()) {
	started := time.Now()
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				beat(time.Since(started))
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

// skipBeforeUpgrade returns why no backup is taken before migrations, or ""
// to take one; warn marks the reasons an admin should act on. Only settings
// that were read can skip it: a failed read is an error, which fails startup.
func (s *Service) skipBeforeUpgrade(ctx context.Context, override bool) (reason string, warn bool, err error) {
	if override {
		return SkipBeforeUpgradeEnv + " is set. Remove it after this start", true, nil
	}
	enabled, err := s.readSetting(ctx, config.BackupEnabledSettingKey)
	if err != nil {
		return "", false, err
	}
	if !config.AdminSettingEnabled(config.BackupEnabledSettingKey, enabled) {
		return "backups are turned off", false, nil
	}
	_, err = s.preflight(ctx)
	switch {
	case err == nil:
		return "", false, nil
	case errors.Is(err, ErrSQLiteUserStore):
		return "the user store is SQLite, which backups cannot include. Back up the database and the SQLite file yourself before upgrading", true, nil
	case errors.Is(err, ErrNoDestination), errors.Is(err, ErrS3NotConfigured), errors.Is(err, ErrNoArchiveKey):
		return "there is nowhere to write it: " + err.Error(), true, nil
	case errors.Is(err, ErrKeyNotConfirmed):
		return "the destination is an S3 bucket, where backups are encrypted, and this server's SECRET_KEY is not confirmed on the Backups page", true, nil
	default:
		return "", false, err
	}
}

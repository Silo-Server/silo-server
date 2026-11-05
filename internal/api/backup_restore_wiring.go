package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// wireBackupRestore gives selective restore the write paths it applies
// through: the admin settings write path, the plugin installer, the library
// editor's follow-ups, artwork announcements, the library editor's folder
// check, the admin job queue, the template catalog and bundle apply defaults
// for template collections, and the section editor's checks. A missing
// handler leaves its section unavailable rather than half wired.
func wireBackupRestore(deps Dependencies, admin *handlers.AdminHandler, libraries *handlers.LibraryHandler, images *handlers.AdminImageHandler, pluginHandler *handlers.PluginHandler,
	collections *handlers.LibraryCollectionHandler, pageSections *handlers.SectionHandler, rateLimits *handlers.RateLimitHandler) {
	jobs := adminjob.NewRepository(deps.DB)
	targets := backup.RestoreTargets{
		CheckFolder: func(_ context.Context, path string) error { return checkLibraryFolder(path) },
		CreateJob: func(ctx context.Context, userID int, req backup.RestoreRequest) (*models.AdminJob, error) {
			return jobs.Create(ctx, adminjob.CreateJobInput{JobType: adminjob.JobTypeBackupRestore, CreatedByUserID: userID, RequestPayload: req, Message: "Queued restore"})
		},
		CancelJob: func(ctx context.Context, id string) error {
			_, err := jobs.Cancel(ctx, id, "Canceled: the restore was queued after the backup lock was lost", time.Now().UTC())
			return err
		},
	}
	if admin != nil {
		targets.ApplySettings = func(ctx context.Context, tx pgx.Tx, values map[string]string, check func(map[string]string) error) ([]string, func(context.Context), error) {
			return applyRestoredSettings(ctx, tx, admin, rateLimits, values, check)
		}
	}
	if pluginHandler != nil {
		targets.Plugins = pluginHandler
		targets.PluginInstalled = pluginHandler.PluginRestored
	}
	if libraries != nil {
		targets.LibraryCreated = libraries.LibraryRestored
		targets.LibrarySectionsCreated = libraries.LibrarySectionsRestored
		targets.CheckLibrary = libraries.CheckRestoredLibrary
	}
	if images != nil {
		targets.ArtworkChanged = images.ArtworkRestored
	}
	if tracker := catalog.NewArtworkRevisionTracker(deps.DB); tracker != nil {
		targets.TrackArtwork = tracker.TrackArtworkRevision
	}
	if collections != nil {
		targets.CheckCollection = collections.CheckRestoredCollection
		targets.SmartCollectionCreated = collections.SmartCollectionRestored
		targets.TemplateTitle = collections.TemplateTitle
		targets.TemplateCollectionCreated = collections.TemplateCollectionRestored
		targets.TemplateCollection = func(bundleID, templateID string, libraryID int, libraryType string) (backup.TemplateDefaults, error) {
			title, input, needsSetup, ok, err := collections.TemplateCollectionDefaults(bundleID, templateID, libraryID, libraryType)
			if err != nil {
				return backup.TemplateDefaults{}, err
			}
			if !ok {
				return backup.TemplateDefaults{}, backup.ErrTemplateNotAvailable
			}
			return backup.TemplateDefaults{Title: title, Input: input, NeedsSetup: needsSetup}, nil
		}
	}
	if pageSections != nil {
		targets.CheckSection = pageSections.CheckRestoredSection
	}
	deps.Backups.SetRestoreTargets(targets)
}

// applyRestoredSettings writes restored settings in tx, each family through
// its editor's write path: rate limits through the rate-limit
// configuration's (/admin/rate-limits/config), the rest through the admin
// settings batch. Both check that the settings are as planned.
func applyRestoredSettings(ctx context.Context, tx pgx.Tx, admin *handlers.AdminHandler, rateLimits *handlers.RateLimitHandler,
	values map[string]string, check func(map[string]string) error) ([]string, func(context.Context), error) {
	limits, rest := map[string]string{}, map[string]string{}
	for key, value := range values {
		if strings.HasPrefix(key, "ratelimit.") {
			limits[key] = value
		} else {
			rest[key] = value
		}
	}
	var restart []string
	var after []func(context.Context)
	if len(rest) > 0 {
		var guard func(handlers.AdminSettingsSnapshot) error
		if check != nil {
			guard = func(s handlers.AdminSettingsSnapshot) error { return check(s.Stored) }
		}
		result, publish, err := admin.ApplyAdminSettingsInTx(ctx, tx, rest, guard)
		if err != nil {
			return nil, nil, err
		}
		restart, after = result.RestartRequiredKeys, append(after, publish)
	}
	if len(limits) > 0 {
		if rateLimits == nil {
			return nil, nil, errors.New("this server cannot restore rate limits")
		}
		publish, err := rateLimits.ApplyRateLimitSettingsInTx(ctx, tx, limits, check)
		if err != nil {
			return nil, nil, err
		}
		after = append(after, publish)
	}
	return restart, func(ctx context.Context) {
		for _, f := range after {
			if f != nil {
				f(ctx)
			}
		}
	}, nil
}

// checkLibraryFolder reports whether this server can open path as a library
// folder: an absolute directory it can read. It reads at most one entry,
// never the whole directory, so a preview's checks cost a few system calls
// a folder.
func checkLibraryFolder(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", path)
	}
	if _, err := dir.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

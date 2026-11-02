package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	collections *handlers.LibraryCollectionHandler, pageSections *handlers.SectionHandler) {
	jobs := adminjob.NewRepository(deps.DB)
	filesystem := handlers.NewFilesystemHandler()
	targets := backup.RestoreTargets{
		CheckFolder: func(ctx context.Context, path string) error {
			_, err := filesystem.BrowseDirectoryPage(ctx, path, "", "", 1)
			return err
		},
		CreateJob: func(ctx context.Context, userID int, req backup.RestoreRequest) (*models.AdminJob, error) {
			return jobs.Create(ctx, adminjob.CreateJobInput{JobType: adminjob.JobTypeBackupRestore, CreatedByUserID: userID, RequestPayload: req, Message: "Queued restore"})
		},
		ActiveRestore: ActiveBackupRestore(jobs),
	}
	if admin != nil {
		targets.ApplySettings = func(ctx context.Context, tx pgx.Tx, values map[string]string) ([]string, func(context.Context), error) {
			result, publish, err := admin.ApplyAdminSettingsInTx(ctx, tx, values)
			if err != nil {
				return nil, nil, err
			}
			return result.RestartRequiredKeys, publish, nil
		}
	}
	if pluginHandler != nil {
		targets.Plugins = pluginHandler
		targets.PluginInstalled = pluginHandler.PluginRestored
	}
	if libraries != nil {
		targets.LibraryCreated = libraries.LibraryRestored
	}
	if images != nil {
		targets.ArtworkChanged = images.ArtworkRestored
	}
	if tracker := catalog.NewArtworkRevisionTracker(deps.DB); tracker != nil {
		targets.TrackArtwork = tracker.TrackArtworkRevision
	}
	if collections != nil {
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

// ActiveBackupRestore reads the request of the backup_restore job queued or
// running now, or nil when there is none. Backup deletion and rotation keep
// its source.
func ActiveBackupRestore(jobs *adminjob.Repository) func(context.Context) (*backup.RestoreRequest, error) {
	return func(ctx context.Context) (*backup.RestoreRequest, error) {
		job, err := jobs.GetActiveByType(ctx, adminjob.JobTypeBackupRestore)
		if errors.Is(err, adminjob.ErrJobNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var req backup.RestoreRequest
		if err := json.Unmarshal(job.RequestPayload, &req); err != nil {
			return nil, fmt.Errorf("reading the active restore job: %w", err)
		}
		return &req, nil
	}
}

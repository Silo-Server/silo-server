package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// LibraryRefreshRunner runs one library metadata refresh in the calling
// goroutine. *adminjob.LibraryRefreshExecutor implements it.
type LibraryRefreshRunner interface {
	Execute(ctx context.Context, req adminjob.LibraryRefreshRequest, progress func(current, total int, message string)) (*adminjob.LibraryRefreshResult, error)
}

// ActiveLibraryRefreshFinder reports a library refresh job that is already
// queued or running. *adminjob.Repository implements it.
type ActiveLibraryRefreshFinder interface {
	GetActiveLibraryRefreshByLibraryID(ctx context.Context, libraryID int) (*models.AdminJob, error)
}

// clusterLock takes a lock shared by every server without waiting. acquired is
// false when another server holds it.
type clusterLock interface {
	TryAcquire(ctx context.Context) (release func(), acquired bool, err error)
}

// fullMetadataRefreshAdvisoryLock spells "SILOFULR".
const fullMetadataRefreshAdvisoryLock int64 = 0x53494C4F46554C52

type advisoryClusterLock struct {
	pool *pgxpool.Pool
	key  int64
}

func (l advisoryClusterLock) TryAcquire(ctx context.Context) (func(), bool, error) {
	lock, acquired, err := pglock.TryAcquire(ctx, l.pool, l.key)
	if err != nil || !acquired {
		return nil, false, err
	}
	return func() {
		if err := lock.Release(ctx); err != nil {
			slog.WarnContext(ctx, "full metadata refresh: releasing advisory lock failed", "component", "taskmanager", "error", err)
		}
	}, true, nil
}

// RefreshAllLibraryMetadataTask re-fetches provider metadata for every item in
// every enabled library. It runs the library refresh executor directly rather
// than queueing admin jobs, because admin jobs belong to a requesting account
// and a task run has none. Every API process runs the task manager, so an
// advisory lock keeps one full refresh running across the cluster.
type RefreshAllLibraryMetadataTask struct {
	folderRepo ScanFolderRepository
	activeJobs ActiveLibraryRefreshFinder
	runner     LibraryRefreshRunner
	lock       clusterLock
}

// NewRefreshAllLibraryMetadataTask creates a new RefreshAllLibraryMetadataTask.
func NewRefreshAllLibraryMetadataTask(pool *pgxpool.Pool, folderRepo ScanFolderRepository, activeJobs ActiveLibraryRefreshFinder, runner LibraryRefreshRunner) *RefreshAllLibraryMetadataTask {
	return &RefreshAllLibraryMetadataTask{
		folderRepo: folderRepo,
		activeJobs: activeJobs,
		runner:     runner,
		lock:       advisoryClusterLock{pool: pool, key: fullMetadataRefreshAdvisoryLock},
	}
}

func (t *RefreshAllLibraryMetadataTask) Key() string  { return "refresh_all_library_metadata" }
func (t *RefreshAllLibraryMetadataTask) Name() string { return "Refresh All Library Metadata" }
func (t *RefreshAllLibraryMetadataTask) Description() string {
	return "Re-fetches provider metadata for every item in all enabled libraries. Runs only when started manually; large libraries can take hours and make many provider requests"
}
func (t *RefreshAllLibraryMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *RefreshAllLibraryMetadataTask) IsHidden() bool { return false }

func (t *RefreshAllLibraryMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return nil
}

// ManualOnly keeps the task off schedules. A full refresh is expensive, and
// interval triggers are timed per process: on a multi-server cluster a server
// that lost the lock would schedule its next run from its own short attempt
// and could start a second full refresh right after the first one finished.
func (t *RefreshAllLibraryMetadataTask) ManualOnly() bool { return true }

type refreshAllLibraryMetadataSummary struct {
	Libraries      int `json:"libraries"`
	Refreshed      int `json:"refreshed"`
	Skipped        int `json:"skipped"`
	Failed         int `json:"failed"`
	ItemsRefreshed int `json:"items_refreshed"`
	ItemsFailed    int `json:"items_failed"`
}

// errFullMetadataRefreshRunning fails a run that another server already
// covers, so the run history does not show a refresh that never happened.
var errFullMetadataRefreshRunning = errors.New("another server is already running a full metadata refresh")

func (t *RefreshAllLibraryMetadataTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	release, acquired, err := t.lock.TryAcquire(ctx)
	if err != nil {
		return fmt.Errorf("claiming full metadata refresh: %w", err)
	}
	if !acquired {
		return errFullMetadataRefreshRunning
	}
	defer release()

	folders, err := t.folderRepo.GetEnabled(ctx)
	if err != nil {
		return fmt.Errorf("listing enabled libraries: %w", err)
	}

	var libraries []*models.MediaFolder
	for _, folder := range folders {
		if folder != nil {
			libraries = append(libraries, folder)
		}
	}
	summary := refreshAllLibraryMetadataSummary{Libraries: len(libraries)}
	defer func() {
		if data, err := json.Marshal(summary); err == nil {
			progress.SetResultData(data)
		}
	}()
	if len(libraries) == 0 {
		progress.Report(100, "No libraries to refresh")
		return nil
	}

	total := float64(len(libraries))
	for i, library := range libraries {
		if err := ctx.Err(); err != nil {
			return err
		}
		base := float64(i) / total * 100
		reportItems := func(current, itemTotal int, message string) {
			if itemTotal <= 0 {
				return
			}
			progress.Report(base+float64(current)/float64(itemTotal)/total*100,
				fmt.Sprintf("%s: %s (%d/%d)", library.Name, message, current, itemTotal))
		}

		progress.Report(base, fmt.Sprintf("Refreshing %s (%d/%d libraries)", library.Name, i+1, len(libraries)))
		result, err := t.refreshLibrary(ctx, library, reportItems)
		switch {
		case err == nil:
			summary.Refreshed++
			if result != nil {
				summary.ItemsRefreshed += result.RefreshedOK + result.PipelineOK
				summary.ItemsFailed += result.RefreshedFailed + result.PipelineFailed
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, adminjob.ErrLibraryRefreshInProgress):
			summary.Skipped++
			progress.Report(base, fmt.Sprintf("Skipped %s: a metadata refresh is already queued or running", library.Name))
		default:
			slog.ErrorContext(ctx, "full metadata refresh: library refresh failed", "component", "taskmanager",
				"library_id", library.ID, "name", library.Name, "error", err)
			summary.Failed++
		}
	}

	progress.Report(100, fmt.Sprintf("Refreshed %d of %d libraries (%d skipped, %d failed)",
		summary.Refreshed, summary.Libraries, summary.Skipped, summary.Failed))
	return nil
}

// refreshLibrary runs one library's full refresh under the same time limit as
// a library refresh job. A queued or running job for the library reports
// adminjob.ErrLibraryRefreshInProgress.
func (t *RefreshAllLibraryMetadataTask) refreshLibrary(
	ctx context.Context,
	library *models.MediaFolder,
	progress func(current, total int, message string),
) (*adminjob.LibraryRefreshResult, error) {
	active, err := t.activeJobs.GetActiveLibraryRefreshByLibraryID(ctx, library.ID)
	switch {
	case err == nil && active != nil:
		return nil, adminjob.ErrLibraryRefreshInProgress
	case err != nil && !errors.Is(err, adminjob.ErrJobNotFound):
		return nil, fmt.Errorf("checking active library refresh: %w", err)
	}

	libraryCtx, cancel := context.WithTimeout(ctx, adminjob.LibraryRefreshTimeout)
	defer cancel()
	result, err := t.runner.Execute(libraryCtx, adminjob.LibraryRefreshRequest{
		LibraryID:   library.ID,
		LibraryName: library.Name,
		Mode:        adminjob.LibraryRefreshModeFull,
	}, progress)
	if err != nil && ctx.Err() == nil && libraryCtx.Err() != nil {
		return nil, fmt.Errorf("timed out after %s: %w", adminjob.LibraryRefreshTimeout, err)
	}
	return result, err
}

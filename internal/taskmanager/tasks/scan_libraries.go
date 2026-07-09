package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ScanFolderRepository provides folder operations for scanning.
type ScanFolderRepository interface {
	GetEnabled(ctx context.Context) ([]*models.MediaFolder, error)
}

// LibraryScanQueuer enqueues a full-library scan for durable execution.
type LibraryScanQueuer interface {
	EnqueueLibraryScan(ctx context.Context, folderID int, trigger string) (bool, error)
}

// ScanLibrariesTask scans all enabled library folders for media changes.
type ScanLibrariesTask struct {
	folderRepo ScanFolderRepository
	queuer     LibraryScanQueuer
	eventBus   cache.EventBus
}

// NewScanLibrariesTask creates a new ScanLibrariesTask.
func NewScanLibrariesTask(folderRepo ScanFolderRepository, queuer LibraryScanQueuer, eventBus cache.EventBus) *ScanLibrariesTask {
	return &ScanLibrariesTask{
		folderRepo: folderRepo,
		queuer:     queuer,
		eventBus:   eventBus,
	}
}

func (t *ScanLibrariesTask) Key() string  { return "scan_libraries" }
func (t *ScanLibrariesTask) Name() string { return "Queue Media Library Scans" }
func (t *ScanLibrariesTask) Description() string {
	return "Queues durable full-library scans for all enabled libraries; actual scan progress continues in Admin Libraries and Server Activity"
}
func (t *ScanLibrariesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *ScanLibrariesTask) IsHidden() bool { return false }

func (t *ScanLibrariesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"},
	}
}

func (t *ScanLibrariesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	folders, err := t.folderRepo.GetEnabled(ctx)
	if err != nil {
		return fmt.Errorf("listing enabled folders: %w", err)
	}
	if len(folders) == 0 {
		progress.Report(100, "No folders to scan")
		return nil
	}

	type scanSummary struct {
		Queued  int `json:"queued"`
		Skipped int `json:"skipped"`
		Errors  int `json:"errors"`
	}

	var (
		mu      sync.Mutex
		summary scanSummary
		done    atomic.Int64
		total   = int64(len(folders))
	)

	for _, folder := range folders {
		if folder == nil {
			done.Add(1)
			continue
		}

		created, enqueueErr := t.queuer.EnqueueLibraryScan(ctx, folder.ID, "task:scan_libraries")
		if enqueueErr != nil {
			slog.ErrorContext(ctx, "scan task: failed to enqueue library scan", "component", "taskmanager", "folder_id", folder.ID, "name", folder.Name, "error", enqueueErr)
			mu.Lock()
			summary.Errors++
			mu.Unlock()
		} else {
			mu.Lock()
			if created {
				summary.Queued++
			} else {
				summary.Skipped++
			}
			mu.Unlock()
		}

		completed := done.Add(1)
		progress.Report(float64(completed)/float64(total)*100,
			fmt.Sprintf("Queued %s (%d/%d folders)", folder.Name, completed, total))
	}

	progress.Report(100, "Library scans queued")

	if data, err := json.Marshal(summary); err == nil {
		progress.SetResultData(data)
	}

	return nil
}

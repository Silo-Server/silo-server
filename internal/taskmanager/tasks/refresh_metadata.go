package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/worker"
)

const (
	refreshMetadataTaskInterval = 6 * time.Hour
	refreshMetadataBatchSize    = 200
	refreshMetadataWorkerCount  = 12
)

// MetadataRefresher can refresh metadata for a queued target.
type MetadataRefresher interface {
	RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error
}

// RefreshCandidateFinder finds items needing metadata refresh.
type RefreshCandidateFinder interface {
	FindCandidates(ctx context.Context, limit int) ([]worker.RefreshCandidate, error)
}

type RefreshDebtPruner interface {
	PruneDisabledLibraryDebt(ctx context.Context) error
}

// RefreshMetadataTask refreshes stale metadata for media items.
type RefreshMetadataTask struct {
	finder    RefreshCandidateFinder
	refresher MetadataRefresher
}

// NewRefreshMetadataTask creates a new RefreshMetadataTask.
func NewRefreshMetadataTask(finder RefreshCandidateFinder, refresher MetadataRefresher) *RefreshMetadataTask {
	return &RefreshMetadataTask{
		finder:    finder,
		refresher: refresher,
	}
}

func (t *RefreshMetadataTask) Key() string  { return "refresh_metadata" }
func (t *RefreshMetadataTask) Name() string { return "Refresh Metadata" }
func (t *RefreshMetadataTask) Description() string {
	return "Refreshes stale metadata from providers for existing media items"
}
func (t *RefreshMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *RefreshMetadataTask) IsHidden() bool { return false }

func (t *RefreshMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(refreshMetadataTaskInterval / time.Millisecond)},
	}
}

func (t *RefreshMetadataTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Finding refresh candidates")
	if pruner, ok := t.finder.(RefreshDebtPruner); ok {
		if err := pruner.PruneDisabledLibraryDebt(ctx); err != nil {
			return fmt.Errorf("pruning disabled-library refresh debt: %w", err)
		}
	}

	var refreshed, errored, claimed, batches int
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		candidates, err := t.finder.FindCandidates(ctx, refreshMetadataBatchSize)
		if err != nil {
			return fmt.Errorf("finding refresh candidates: %w", err)
		}

		if len(candidates) == 0 {
			if claimed == 0 {
				progress.Report(100, "No items need refreshing")
			} else {
				progress.Report(100, fmt.Sprintf(
					"Refreshed %d, errored %d",
					refreshed,
					errored,
				))
			}
			return nil
		}

		batches++
		claimed += len(candidates)
		batchRefreshed, batchErrored, err := t.refreshBatch(ctx, progress, batches, candidates, refreshed, errored)
		refreshed += batchRefreshed
		errored += batchErrored
		if err != nil {
			return err
		}

		if len(candidates) < refreshMetadataBatchSize {
			progress.Report(100, fmt.Sprintf(
				"Refreshed %d, errored %d",
				refreshed,
				errored,
			))
			return nil
		}
	}
}

func (t *RefreshMetadataTask) refreshBatch(
	ctx context.Context,
	progress taskmanager.ProgressReporter,
	batchNumber int,
	candidates []worker.RefreshCandidate,
	baseRefreshed int,
	baseErrored int,
) (int, int, error) {
	if len(candidates) == 0 {
		return 0, 0, nil
	}

	workerCount := refreshMetadataWorkerCount
	if len(candidates) < workerCount {
		workerCount = len(candidates)
	}

	type refreshJob struct {
		candidate worker.RefreshCandidate
	}

	jobs := make(chan refreshJob)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var started, processed, refreshed, errored int

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}

				mu.Lock()
				started++
				current := started
				startRefreshed := baseRefreshed + refreshed
				startErrored := baseErrored + errored
				mu.Unlock()
				progress.Report(0, fmt.Sprintf(
					"Refreshing batch %d item %d/%d (refreshed %d, errored %d)",
					batchNumber,
					current,
					len(candidates),
					startRefreshed,
					startErrored,
				))

				itemCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				err := t.refresher.RefreshScheduledTarget(itemCtx, job.candidate.TargetType, job.candidate.ContentID)
				cancel()

				mu.Lock()
				processed++
				if err != nil {
					errored++
				} else {
					refreshed++
				}
				done := processed
				doneRefreshed := baseRefreshed + refreshed
				doneErrored := baseErrored + errored
				mu.Unlock()

				if err != nil {
					slog.WarnContext(ctx, "refresh task: failed", "component", "taskmanager",
						"target_type", job.candidate.TargetType,
						"content_id", job.candidate.ContentID,
						"error", err)
				}

				progress.Report(0, fmt.Sprintf(
					"Refreshing batch %d item %d/%d (refreshed %d, errored %d)",
					batchNumber,
					done,
					len(candidates),
					doneRefreshed,
					doneErrored,
				))
			}
		}()
	}

	for _, candidate := range candidates {
		select {
		case jobs <- refreshJob{candidate: candidate}:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return refreshed, errored, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()

	if ctx.Err() != nil {
		return refreshed, errored, ctx.Err()
	}
	return refreshed, errored, nil
}

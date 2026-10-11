package tasks

import (
	"context"
	"errors"
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
	// refreshClaimMissesBeforeStop is how many claim renewals in a row may fail
	// before a batch stops. Renewals run every third of the lease, so after two
	// misses the next one would come after the claims lapse. By then another
	// node may hold the rows, and this batch's flush would settle them under
	// that node's claim.
	refreshClaimMissesBeforeStop = 2
)

// errRefreshClaimsLost stops a batch whose claims could not be renewed. Its
// rows are left claimed and are retried once their lease expires.
var errRefreshClaimsLost = errors.New("refresh claims could not be renewed")

// MetadataRefresher can refresh metadata for a queued target.
type MetadataRefresher interface {
	RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error
}

// MetadataRefreshBatcher is implemented by refreshers that can defer
// series-wide follow-up work across a claimed batch. Targets refreshed with the
// returned context share the batch, and the task calls flush once after every
// target in the batch has returned.
type MetadataRefreshBatcher interface {
	BeginScheduledRefreshBatch(ctx context.Context) (context.Context, func(context.Context))
}

// RefreshCandidateFinder finds items needing metadata refresh.
type RefreshCandidateFinder interface {
	FindCandidates(ctx context.Context, limit int) ([]worker.RefreshCandidate, error)
}

// RefreshClaimRenewer is implemented by finders whose claims expire. The task
// renews a batch's claims until the batch, including its flush, has finished,
// so a long batch keeps the rows it has not settled yet.
type RefreshClaimRenewer interface {
	RenewClaims(ctx context.Context, candidates []worker.RefreshCandidate) error
	ClaimRenewInterval() time.Duration
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

	// The batch stops early when its claims can no longer be renewed. The
	// flush then runs on the stopped context, so it settles nothing.
	batchCtx, stopBatch := context.WithCancelCause(ctx)
	defer stopBatch(nil)

	// Deferred calls run in reverse, so the claims stay renewed through the flush.
	if renewer, ok := t.finder.(RefreshClaimRenewer); ok {
		defer renewRefreshClaims(batchCtx, renewer, candidates, stopBatch)()
	}

	// Every return below happens after the workers have stopped, so the
	// deferred flush sees the whole batch's refreshes.
	itemParent := batchCtx
	if batcher, ok := t.refresher.(MetadataRefreshBatcher); ok {
		var flush func(context.Context)
		itemParent, flush = batcher.BeginScheduledRefreshBatch(batchCtx)
		defer flush(batchCtx)
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
				if batchCtx.Err() != nil {
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

				itemCtx, cancel := context.WithTimeout(itemParent, 2*time.Minute)
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
		case <-batchCtx.Done():
			close(jobs)
			wg.Wait()
			return refreshed, errored, context.Cause(batchCtx)
		}
	}
	close(jobs)
	wg.Wait()

	if batchCtx.Err() != nil {
		return refreshed, errored, context.Cause(batchCtx)
	}
	return refreshed, errored, nil
}

// renewRefreshClaims renews the batch's claims on the renewer's interval until
// the returned stop function is called or ctx is done. After
// refreshClaimMissesBeforeStop failed renewals in a row it stops the batch
// with errRefreshClaimsLost.
func renewRefreshClaims(ctx context.Context, renewer RefreshClaimRenewer, candidates []worker.RefreshCandidate, stopBatch context.CancelCauseFunc) func() {
	interval := renewer.ClaimRenewInterval()
	if interval <= 0 || len(candidates) == 0 {
		return func() {}
	}
	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		misses := 0
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				err := renewer.RenewClaims(renewCtx, candidates)
				if renewCtx.Err() != nil {
					return
				}
				if err == nil {
					misses = 0
					continue
				}
				misses++
				slog.WarnContext(renewCtx, "refresh task: failed to renew claims", "component", "taskmanager",
					"claims", len(candidates), "misses", misses, "error", err)
				if misses >= refreshClaimMissesBeforeStop {
					stopBatch(fmt.Errorf("%w: %w", errRefreshClaimsLost, err))
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

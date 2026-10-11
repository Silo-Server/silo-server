package metadata

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// Local sidecar artwork is copied into the artwork store by the process that
// discovered it. Only api and integrated processes run metadata refreshes and
// the image cache, and a refresh can only discover a sidecar it can read, so
// the discovering process is one that sees the library mount. The queue keeps
// each job for retries, which any server may claim.

const (
	// localArtworkCopyWorkers bounds the copies one process runs at once.
	// Each decodes an image of up to MaxArtworkSourceBytes and encodes its
	// variant ladder, alongside the image-cache drain's own workers, so the
	// pool stays small; refreshes never wait for it.
	localArtworkCopyWorkers = 2

	// localArtworkCopyBacklog bounds the discovered slots waiting for a copy
	// worker. A slot that does not fit is left to the queue: once its
	// discovery hold ends, any drain copies it.
	localArtworkCopyBacklog = 4096
)

// imageCacheOutcomeDeferred is the outcome of a local job left for a server
// that can see its library root.
const imageCacheOutcomeDeferred = "deferred"

// LocalArtworkCopier copies discovered local artwork into the artwork store
// from the calling process. *ImageCacheProcessor implements it.
type LocalArtworkCopier interface {
	CopyLocalArtwork(ctx context.Context, inputs []EnqueueImageCacheJobInput) error
}

// localArtworkCopies is the processor's pool for discovered local artwork.
type localArtworkCopies struct {
	start    sync.Once
	workerID string
	pending  chan EnqueueImageCacheJobInput
	// inflight counts queued and running copies, so tests can wait for them.
	inflight sync.WaitGroup
}

// CopyLocalArtwork queues the local sidecar inputs and copies them on this
// process in the background, through the same claim, confinement, read and
// write path as a queued job. It returns once the jobs are queued; it never
// waits for a copy. Inputs that are not local are ignored.
//
// Inputs may target any slot the processor updates (items, seasons, episodes,
// people). The processor confines the read to the library roots of SeriesID,
// falling back to TargetContentID, so a person photo found in an item's
// folder must carry that item's content ID as SeriesID.
func (p *ImageCacheProcessor) CopyLocalArtwork(ctx context.Context, inputs []EnqueueImageCacheJobInput) error {
	if p == nil || p.cacher == nil {
		return errNoLocalArtworkQueue
	}
	store, ok := p.jobs.(localArtworkJobStore)
	if !ok {
		return errNoLocalArtworkQueue
	}
	local, _ := splitLocalImageCacheInputs(inputs)
	if len(local) == 0 {
		return nil
	}
	queued, err := store.enqueueHeldLocalJobs(ctx, local)
	if err != nil {
		return err
	}
	if len(queued) == 0 {
		return nil
	}
	p.localCopies.start.Do(func() {
		p.localCopies.workerID = "local-artwork:" + uuid.NewString()
		p.localCopies.pending = make(chan EnqueueImageCacheJobInput, localArtworkCopyBacklog)
		for range localArtworkCopyWorkers {
			go p.runLocalArtworkCopies(store)
		}
	})
	dropped := 0
	for _, in := range queued {
		p.localCopies.inflight.Add(1)
		select {
		case p.localCopies.pending <- in:
		default:
			p.localCopies.inflight.Done()
			dropped++
		}
	}
	if dropped > 0 {
		slog.InfoContext(ctx, "metadata image cache: local artwork backlog full, leaving copies to the queue",
			"component", "metadata", "count", dropped, "retry_after", localArtworkDiscoveryHold)
	}
	return nil
}

func (p *ImageCacheProcessor) runLocalArtworkCopies(store localArtworkJobStore) {
	for in := range p.localCopies.pending {
		p.copyDiscoveredLocalArtwork(store, in)
		p.localCopies.inflight.Done()
	}
}

// copyDiscoveredLocalArtwork claims one discovered slot's job and processes
// it. Nothing is left to do when the job is gone, running elsewhere, or now
// carries another source.
func (p *ImageCacheProcessor) copyDiscoveredLocalArtwork(store localArtworkJobStore, in EnqueueImageCacheJobInput) {
	ctx, cancel := context.WithTimeout(context.Background(), ImageCacheJobTimeout)
	defer cancel()
	job, err := store.claimQueuedLocalJob(ctx, in, p.localCopies.workerID)
	if err != nil {
		p.logger.WarnContext(ctx, "metadata image cache: failed to claim discovered local artwork",
			"target_type", in.TargetType, "target_id", in.TargetContentID, "error", err)
		return
	}
	if job == nil {
		return
	}
	result := p.processOne(ctx, job)
	p.logger.DebugContext(ctx, "metadata image cache: copied discovered local artwork",
		"job_id", job.ID, "target_type", job.TargetType, "target_id", job.TargetContentID,
		"image_type", job.ImageType, "outcome", result.outcome)
}

// waitLocalArtworkCopies blocks until every discovered copy handed to the pool
// has finished. Tests use it.
func (p *ImageCacheProcessor) waitLocalArtworkCopies() {
	p.localCopies.inflight.Wait()
}

// deferLocalJobWithoutRoot releases a local job this server cannot serve
// because none of the library roots holding its file is present here: each is
// missing, unreadable, unresponsive, or empty (an unmounted share often leaves
// an empty mount point). The job goes back to the queue without counting an
// attempt, for a server that has the mount. When a root is present, the
// failure is about the file and the caller records it as usual.
func (p *ImageCacheProcessor) deferLocalJobWithoutRoot(ctx context.Context, job *models.MetadataImageCacheJob, path string, roots []string) bool {
	deferrer, ok := p.jobs.(imageCacheJobDeferrer)
	if !ok {
		return false
	}
	var missing []string
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if !localImagePathWithinRoots(path, []string{root}) {
			continue
		}
		probe := rootcheck.ProbeWithTimeout(ctx, root, rootcheck.DefaultProbeTimeout)
		if probe.Reachable && !probe.Empty {
			return false
		}
		reason := probe.ErrorCode
		if reason == "" {
			reason = "empty mount point"
		}
		missing = append(missing, root+" ("+reason+")")
	}
	if len(missing) == 0 {
		return false
	}
	errText := "library root not available on this server: " + strings.Join(missing, ", ")
	writeCtx, cancel := terminalJobContext(ctx)
	defer cancel()
	if err := deferrer.DeferClaimed(writeCtx, job.ID, job.LockedBy, errText, imageCacheRootUnavailableRetry); err != nil {
		p.logger.WarnContext(ctx, "metadata image cache: failed to defer local artwork job", "job_id", job.ID, "error", err)
		return false
	}
	p.logger.DebugContext(ctx, "metadata image cache: left local artwork for a server with the library mounted",
		"job_id", job.ID, "target_id", job.TargetContentID, "reason", errText, "retry_after", imageCacheRootUnavailableRetry)
	return true
}

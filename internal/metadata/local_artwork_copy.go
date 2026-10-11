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
// waits for a copy. Inputs that are not local sidecar files are ignored.
//
// Inputs may target any slot the processor updates (items, seasons, episodes,
// people). The processor confines the read to the library roots of SeriesID,
// falling back to TargetContentID, so a person photo found in an item's
// folder carries that item's content ID as SeriesID (and the person's ID as
// ProviderContentID, which keys its copy).
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
	// After a partial failure, the slots already queued behind the hold are
	// still this process's to copy.
	queued, err := store.enqueueHeldLocalJobs(ctx, local)
	if len(queued) > 0 {
		p.startLocalArtworkCopies(ctx, store, queued)
	}
	return err
}

// startLocalArtworkCopies hands queued slots to the copy workers, starting
// them on first use. A slot that does not fit the backlog waits out its hold.
func (p *ImageCacheProcessor) startLocalArtworkCopies(ctx context.Context, store localArtworkJobStore, queued []EnqueueImageCacheJobInput) {
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

// deferLocalJobWithoutStorage releases a local job this server cannot serve
// because the storage holding its file is not mounted here. It applies the
// rule a refresh uses before dropping cached local art: the nearest existing
// folder at or above the file's folder, searching no higher than the library
// root, must list entries. An unmounted share leaves its mount point empty or
// missing, also when it is nested inside a root that is present, and a folder
// that cannot be read or does not answer in time counts as absent too. The
// job goes back to the queue without counting an attempt, for a server that
// has the mount. When the folder is there, the failure is about the file and
// the caller records it as usual.
func (p *ImageCacheProcessor) deferLocalJobWithoutStorage(ctx context.Context, job *models.MetadataImageCacheJob, path string, roots []string) bool {
	deferrer, ok := p.jobs.(imageCacheJobDeferrer)
	if !ok {
		return false
	}
	var containing []string
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if localImagePathWithinRoots(path, []string{root}) {
			containing = append(containing, root)
		}
	}
	// A canceled job must not read as an unresponsive mount; each probe
	// keeps its own timeout.
	folders := &localArtworkRetention{ctx: context.WithoutCancel(ctx), probes: make(map[string]rootcheck.Result)}
	if len(containing) == 0 || folders.nearestFolderListsEntries(filepath.Dir(path), containing) {
		return false
	}
	errText := localImageStorageUnavailable + ": " + filepath.Dir(path)
	writeCtx, cancel := terminalJobContext(ctx)
	defer cancel()
	if err := deferrer.DeferClaimed(writeCtx, job.ID, job.LockedBy, errText, imageCacheRootUnavailableRetry, imageCacheRootUnavailableRepeatRetry); err != nil {
		p.logger.WarnContext(ctx, "metadata image cache: failed to defer local artwork job", "job_id", job.ID, "error", err)
		return false
	}
	p.logger.DebugContext(ctx, "metadata image cache: left local artwork for a server with the library mounted",
		"job_id", job.ID, "target_id", job.TargetContentID, "reason", errText)
	return true
}

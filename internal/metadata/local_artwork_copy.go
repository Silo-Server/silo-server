package metadata

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// Local sidecar artwork is copied into the artwork store by the process that
// discovered it, while metadata.cache_images is on; with it off no local art is
// selected or copied. Only api and integrated processes run metadata refreshes and
// the image cache, and a refresh can only discover a sidecar it can read, so
// the discovering process is one that sees the library mount. The queue keeps
// each job for retries, which any server may claim.

const (
	// localArtworkCopyWorkers bounds the copies one process runs at once.
	// Each decodes an image of up to MaxArtworkSourceBytes and encodes its
	// variant ladder, and takes a slot of the same budget as the image-cache
	// drain (SetWorkerBudget); refreshes never wait for it.
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
	start   sync.Once
	pending chan EnqueueImageCacheJobInput
	// lastFullLog rate-limits the full-backlog message (Unix nanoseconds).
	lastFullLog atomic.Int64
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
	if !p.enabled.Load() {
		// The caller queues the artwork instead; a drain copies it once
		// caching is on.
		return errLocalArtworkCachingOff
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
		p.localCopies.pending = make(chan EnqueueImageCacheJobInput, localArtworkCopyBacklog)
		for range localArtworkCopyWorkers {
			go p.runLocalArtworkCopies(store)
		}
	})
	dropped := 0
	for _, in := range queued {
		select {
		case p.localCopies.pending <- in:
		default:
			dropped++
		}
	}
	if dropped > 0 {
		// A large first scan can overflow the backlog on every refresh; one
		// line a minute is enough to say so.
		now := time.Now().UnixNano()
		last := p.localCopies.lastFullLog.Load()
		if now-last >= int64(time.Minute) && p.localCopies.lastFullLog.CompareAndSwap(last, now) {
			slog.InfoContext(ctx, "metadata image cache: local artwork backlog full, leaving copies to the queue",
				"component", "metadata", "count", dropped, "retry_after", localArtworkDiscoveryHold)
		}
	}
}

func (p *ImageCacheProcessor) runLocalArtworkCopies(store localArtworkJobStore) {
	for in := range p.localCopies.pending {
		p.copyDiscoveredLocalArtwork(store, in)
	}
}

// copyDiscoveredLocalArtwork claims one discovered slot's job and processes
// it. Nothing is left to do when the job is gone, running elsewhere, or now
// carries another source.
func (p *ImageCacheProcessor) copyDiscoveredLocalArtwork(store localArtworkJobStore, in EnqueueImageCacheJobInput) {
	// Take a slot of the process's artwork budget before claiming, so a
	// claimed job never waits on it.
	release, err := p.workBudget.acquire(context.Background())
	if err != nil {
		return
	}
	defer release()
	if !p.enabled.Load() {
		// Caching went off while the slot waited; the job stays queued.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ImageCacheJobTimeout)
	defer cancel()
	// A token per claim: when a refresh replaces the slot's source while an
	// older copy runs, the older copy's terminal write must not match the
	// newer claim of the same job row.
	job, err := store.claimQueuedLocalJob(ctx, in, "local-artwork:"+uuid.NewString())
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

// deferLocalJobWithoutStorage releases a local job this server cannot serve
// because the storage holding its file is not mounted here. It looks for the
// nearest folder that exists at or above the file's folder, searching no
// higher than the library roots holding the file: unless that folder answers
// and lists entries, the storage is not mounted on this server. An unmounted
// share leaves its mount point empty or missing, also when it sits inside a
// library root that is present, and a folder that does not answer in time or
// fails to read proves nothing. A folder that exists but denies listing shows
// the storage is there. The job goes back to the queue without
// counting an attempt, for a server that has the mount. When the folder lists
// entries, the storage is there and the failure is about the file (or a
// folder removed under it), which the caller records as usual.
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
	// A canceled job must not read as an unresponsive mount; each probe keeps
	// its own timeout.
	if len(containing) == 0 || nearestExistingFolderListsEntries(context.WithoutCancel(ctx), filepath.Dir(path), containing) {
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

// nearestExistingFolderListsEntries walks up from dir to the first folder that
// exists, going no higher than roots, and reports whether it shows the storage
// is mounted: it lists entries, or it exists but cannot be listed. A folder
// reached through a symlink out of roots says nothing about their storage, and
// the path escapes them, so it counts as mounted and the job fails.
func nearestExistingFolderListsEntries(ctx context.Context, dir string, roots []string) bool {
	for {
		probe := rootcheck.ProbeWithTimeout(ctx, dir, rootcheck.DefaultProbeTimeout)
		switch {
		case probe.Reachable:
			if _, _, err := localImagePathResolvedWithinRoots(dir, roots); errors.Is(err, errLocalImageOutsideRoots) {
				return true
			}
			return !probe.Empty
		// A folder that exists but cannot be listed, or is not a folder,
		// shows the storage is there; the failure is the file's.
		case probe.ErrorCode == rootcheck.ErrCodePermissionDenied || probe.ErrorCode == rootcheck.ErrCodeNotDirectory:
			return true
		case probe.ErrorCode != rootcheck.ErrCodeNotFound || slices.Contains(roots, dir):
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

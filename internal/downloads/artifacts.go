package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	artifactLease       = 2 * time.Minute
	artifactHeartbeat   = 40 * time.Second
	artifactMaxAttempts = 3
)

// EncodePreparer produces a single finalized file for an artifact. The default
// implementation calls playback.PrepareFile; tests substitute a fake.
type EncodePreparer interface {
	PrepareFile(ctx context.Context, opts playback.TranscodeOpts, outputPath string) error
}

type playbackPreparer struct{}

func (playbackPreparer) PrepareFile(ctx context.Context, opts playback.TranscodeOpts, outputPath string) error {
	return playback.PrepareFile(ctx, opts, outputPath)
}

// NewPlaybackPreparer returns the production EncodePreparer (ffmpeg-backed).
func NewPlaybackPreparer() EncodePreparer { return playbackPreparer{} }

// ArtifactNotifier publishes an event when a linked download changes state.
type ArtifactNotifier func(ctx context.Context, d *Download)

// ArtifactManager owns the durable encode queue: it ensures/deduplicates encode
// jobs, drains them through a bounded worker pool with leased heartbeats, and
// recovers stranded jobs on startup.
type ArtifactManager struct {
	repo      *ArtifactRepository
	downloads *Repository
	fileRepo  FileResolver
	preparer  EncodePreparer
	owner     string
	liveCfg   func() *config.Config
	notify    ArtifactNotifier

	mu             sync.Mutex
	kick           func()
	lastDiskSweep  time.Time
	lastStaleSweep time.Time
}

// maintenanceInterval spaces the disk-presence and stale-row sweeps: both are
// O(cache size) (stats / extra queries) and their failure modes self-heal, so
// running them on every 30s task tick is steady-state waste that grows with
// the artifact cache. The first run after startup always executes.
const maintenanceInterval = time.Hour

// maintenanceDue reports whether the sweep guarded by last is due, advancing
// the stamp when it is.
func (m *ArtifactManager) maintenanceDue(last *time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !last.IsZero() && time.Since(*last) < maintenanceInterval {
		return false
	}
	*last = time.Now()
	return true
}

// NewArtifactManager constructs an ArtifactManager. liveCfg reads the current
// config (artifact dir, worker-pool size, byte budget, ffmpeg/hwaccel); owner is
// this node's id for lease ownership; notify (optional) publishes ready/failed.
func NewArtifactManager(
	repo *ArtifactRepository,
	downloadRepo *Repository,
	fileRepo FileResolver,
	preparer EncodePreparer,
	owner string,
	liveCfg func() *config.Config,
	notify ArtifactNotifier,
) *ArtifactManager {
	if preparer == nil {
		preparer = playbackPreparer{}
	}
	if owner == "" {
		owner = "node"
	}
	return &ArtifactManager{
		repo: repo, downloads: downloadRepo, fileRepo: fileRepo, preparer: preparer,
		owner: owner, liveCfg: liveCfg, notify: notify,
	}
}

// SetKick wires a low-latency drain trigger (e.g. taskmanager RunTask) invoked
// when a new job is enqueued.
func (m *ArtifactManager) SetKick(kick func()) {
	m.mu.Lock()
	m.kick = kick
	m.mu.Unlock()
}

// Ready returns a ready artifact for serving and bumps its LRU timestamp.
// Returns ErrDownloadNotActive when the artifact is not yet ready.
func (m *ArtifactManager) Ready(ctx context.Context, id string) (*Artifact, error) {
	a, err := m.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Status != ArtifactReady {
		return nil, fmt.Errorf("artifact is %s: %w", a.Status, ErrDownloadNotActive)
	}
	_ = m.repo.TouchLastUsed(ctx, id)
	return a, nil
}

func (m *ArtifactManager) downloadConfig() config.DownloadConfig {
	if m.liveCfg != nil {
		if c := m.liveCfg(); c != nil {
			return c.Download
		}
	}
	return config.DownloadConfig{}
}

// artifactDir resolves the effective output directory for prepared artifacts,
// defaulting under the transcode dir when download.artifact_dir is unset so
// encodes never write relative to the process working directory.
func (m *ArtifactManager) artifactDir() string {
	var artifactDir, transcodeDir string
	if m.liveCfg != nil {
		if c := m.liveCfg(); c != nil {
			artifactDir = c.Download.ArtifactDir
			transcodeDir = c.Playback.TranscodeDir
		}
	}
	return effectiveArtifactDir(artifactDir, transcodeDir)
}

// Ensure deduplicates and (when new) enqueues an encode job for file in the
// given format, returning the current artifact row. The deterministic
// output_path keeps a reclaimed job idempotent.
func (m *ArtifactManager) Ensure(ctx context.Context, file *models.MediaFile, format string, target playback.PrepareTarget) (*Artifact, error) {
	hash := paramsHash(format, target.Container, target.CodecVideo, target.CodecAudio, target.Resolution, target.AudioTrackIndex, target.TargetBitrateKbps, false)
	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	a := &Artifact{
		ID:                id,
		MediaFileID:       file.ID,
		Format:            format,
		ParamsHash:        hash,
		Container:         target.Container,
		CodecVideo:        target.CodecVideo,
		CodecAudio:        target.CodecAudio,
		Resolution:        target.Resolution,
		AudioTrackIndex:   target.AudioTrackIndex,
		TargetBitrateKbps: target.TargetBitrateKbps,
		OutputPath:        artifactOutputPath(m.artifactDir(), file.ID, format, hash),
		MaxAttempts:       artifactMaxAttempts,
	}
	row, created, err := m.repo.EnsureQueued(ctx, a)
	if err != nil {
		return nil, err
	}
	if row.Status == ArtifactReady {
		_ = m.repo.TouchLastUsed(ctx, row.ID)
		return row, nil
	}
	// A terminally-failed dedup row would otherwise strand every new download
	// linked to it in 'preparing' forever (no drain is triggered for an existing
	// row). Requeue it for a fresh attempt so the new download can resolve — or
	// fail cleanly via reconciliation once the encode is exhausted again.
	if row.Status == ArtifactFailed {
		switch err := m.repo.Requeue(ctx, row.ID); {
		case errors.Is(err, ErrNotFound):
			// The failed row was swept between EnsureQueued and Requeue:
			// create a fresh job instead of linking to a dead artifact id.
			if row, _, err = m.repo.EnsureQueued(ctx, a); err != nil {
				return nil, err
			}
		case err != nil:
			return nil, err
		default:
			row.Status = ArtifactQueued
		}
		m.triggerDrain()
		return row, nil
	}
	if created {
		m.triggerDrain()
	}
	return row, nil
}

func (m *ArtifactManager) triggerDrain() {
	m.mu.Lock()
	kick := m.kick
	m.mu.Unlock()
	if kick != nil {
		// Ensure is called on request goroutines and the kick runs the encode
		// task to completion (the task manager serializes concurrent runs), so
		// it must never execute inline: a POST /downloads would otherwise block
		// on the entire queue drain, ffmpeg encodes included.
		go kick()
	}
}

// RunOnce performs a startup-safe recovery sweep and then drains the queue
// until empty. It is safe to call concurrently across nodes; FOR UPDATE SKIP
// LOCKED prevents double-encoding.
func (m *ArtifactManager) RunOnce(ctx context.Context) error {
	m.recover(ctx)
	return m.drain(ctx)
}

// recover repairs state a crash or lost lease can leave behind: it reclaims
// expired-lease running rows (back to queued, or failed when attempts are
// exhausted), reconciles linked downloads against terminal artifact states, and
// re-queues ready artifacts whose output file is missing on disk. Safe to run
// repeatedly (each step is idempotent).
func (m *ArtifactManager) recover(ctx context.Context) {
	if _, err := m.repo.ReclaimExpiredLeases(ctx); err != nil {
		slog.WarnContext(ctx, "download artifact lease reclaim failed", "component", "downloads", "error", err)
	}

	// Reconcile downloads stranded in 'preparing' against their artifact's
	// terminal state: this closes the non-transactional window between an
	// artifact's MarkReady and its MarkLinkedDownloadsReady, and fails the links
	// of any artifact that reached 'failed' (including the rows just reclaimed to
	// failed above) so a download can never sit 'preparing' forever.
	readyFlipped, failedFlipped, err := m.downloads.ReconcileLinkedDownloads(ctx)
	if err != nil {
		slog.WarnContext(ctx, "reconciling linked downloads failed", "component", "downloads", "error", err)
	} else {
		for _, d := range readyFlipped {
			m.publish(ctx, d)
		}
		for _, d := range failedFlipped {
			m.publish(ctx, d)
		}
	}

	// Disk-presence sweep: stats every ready file, so it runs on the startup
	// pass and then hourly rather than on every tick.
	if !m.maintenanceDue(&m.lastDiskSweep) {
		return
	}
	ready, err := m.repo.ListReady(ctx)
	if err != nil {
		slog.WarnContext(ctx, "download artifact ready scan failed", "component", "downloads", "error", err)
		return
	}
	for _, a := range ready {
		if a.OutputPath == "" {
			continue
		}
		if _, statErr := os.Stat(a.OutputPath); statErr != nil {
			slog.WarnContext(ctx, "download artifact output missing, re-queuing", "component", "downloads", "artifact_id", a.ID, "path", a.OutputPath)
			if err := m.repo.Requeue(ctx, a.ID); err != nil {
				slog.WarnContext(ctx, "re-queue artifact failed", "component", "downloads", "artifact_id", a.ID, "error", err)
				continue
			}
			m.triggerDrain()
		}
	}
}

// drain claims and encodes jobs through a bounded worker pool until the queue is
// empty or the context is canceled.
func (m *ArtifactManager) drain(ctx context.Context) error {
	maxConcurrent := m.downloadConfig().MaxConcurrentPrepares
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for {
		// Acquire a worker slot BEFORE claiming. A claimed job is leased but only
		// heartbeated once encodeOne runs; claiming first and then blocking for a
		// slot would leave the job leased-but-unattended, so its lease could lapse
		// while it waits — letting another node steal it and encode the same
		// output path concurrently. Reserving the slot first closes that window.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		job, err := m.repo.ClaimNext(ctx, m.owner, artifactLease)
		if err != nil {
			<-sem // release the slot we reserved but won't use
			if errors.Is(err, ErrNoArtifactJob) {
				break
			}
			wg.Wait()
			return err // includes context cancellation (pgx honors ctx)
		}
		wg.Add(1)
		go func(a *Artifact) {
			defer wg.Done()
			defer func() { <-sem }()
			m.encodeOne(ctx, a)
		}(job)
	}
	wg.Wait()
	return nil
}

// encodeOne runs one claimed job to completion, extending its lease via a
// heartbeat, and links/notifies the dependent download rows on the outcome.
func (m *ArtifactManager) encodeOne(ctx context.Context, a *Artifact) {
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	// heartbeatLoop cancels hbCtx if the lease is lost; PrepareFile runs on hbCtx
	// so that cancellation aborts ffmpeg, ensuring we never keep writing the
	// output path after another worker has taken the job.
	go m.heartbeatLoop(hbCtx, cancelHB, a.ID)

	file, err := m.fileRepo.GetByID(ctx, a.MediaFileID)
	if err != nil || file == nil {
		m.failJob(ctx, a, "source media file unavailable")
		return
	}

	opts := m.buildOpts(file, a)
	if err := m.preparer.PrepareFile(hbCtx, opts, a.OutputPath); err != nil {
		switch {
		case ctx.Err() != nil:
			// Parent shutting down: leave the job 'running'; its lease expires and
			// recovery (here or on another node) reclaims it.
			return
		case hbCtx.Err() != nil:
			// We lost the lease mid-encode; another worker now owns the job.
			slog.WarnContext(ctx, "download artifact encode aborted; lease lost", "component", "downloads", "artifact_id", a.ID)
			return
		default:
			slog.WarnContext(ctx, "download artifact encode failed", "component", "downloads", "artifact_id", a.ID, "error", err)
			m.failJob(ctx, a, err.Error())
			return
		}
	}

	var size int64
	if fi, statErr := os.Stat(a.OutputPath); statErr == nil {
		size = fi.Size()
	}
	// Fenced on lease ownership: if we lost the lease between encode and commit,
	// applied is false and the current owner is responsible for flipping links —
	// do not flip them here or we would race/duplicate that owner's work.
	applied, err := m.repo.MarkReady(ctx, a.ID, m.owner, a.OutputPath, size)
	if err != nil {
		slog.ErrorContext(ctx, "marking artifact ready failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return
	}
	if !applied {
		slog.WarnContext(ctx, "download artifact ready skipped; lease lost", "component", "downloads", "artifact_id", a.ID)
		return
	}
	flipped, err := m.downloads.MarkLinkedDownloadsReady(ctx, a.ID, size)
	if err != nil {
		slog.ErrorContext(ctx, "flipping linked downloads ready failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return
	}
	for _, d := range flipped {
		m.publish(ctx, d)
	}
}

func (m *ArtifactManager) failJob(ctx context.Context, a *Artifact, msg string) {
	terminal, applied, err := m.repo.MarkFailedOrRetry(ctx, a.ID, m.owner, msg, backoffFor(a.Attempts))
	if err != nil {
		slog.ErrorContext(ctx, "marking artifact failed/retry errored", "component", "downloads", "artifact_id", a.ID, "error", err)
		return
	}
	if !applied {
		// Lease lost; the current owner is responsible for the job's outcome.
		return
	}
	if terminal {
		m.failLinkedDownloads(ctx, a.ID, msg)
	} else {
		m.triggerDrain()
	}
}

func (m *ArtifactManager) failLinkedDownloads(ctx context.Context, artifactID, msg string) {
	flipped, err := m.downloads.MarkLinkedDownloadsFailed(ctx, artifactID, msg)
	if err != nil {
		slog.ErrorContext(ctx, "flipping linked downloads failed errored", "component", "downloads", "artifact_id", artifactID, "error", err)
		return
	}
	for _, d := range flipped {
		m.publish(ctx, d)
	}
}

func (m *ArtifactManager) publish(ctx context.Context, d *Download) {
	if m.notify != nil {
		m.notify(ctx, d)
	}
}

// heartbeatLoop extends the job's lease until ctx is done. If the lease is lost
// (another worker stole it, or the row is gone) it calls cancel to abort the
// encode so two workers never write the same output path. A transient DB error
// is retried on the next tick rather than aborting a healthy encode.
func (m *ArtifactManager) heartbeatLoop(ctx context.Context, cancel context.CancelFunc, id string) {
	ticker := time.NewTicker(artifactHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := m.repo.Heartbeat(ctx, id, m.owner, artifactLease)
			switch {
			case err != nil && ctx.Err() != nil:
				return // encode finished or shutting down
			case err != nil:
				slog.WarnContext(ctx, "download artifact heartbeat errored", "component", "downloads", "artifact_id", id, "error", err)
			case !ok:
				slog.WarnContext(ctx, "download artifact lease lost; aborting encode", "component", "downloads", "artifact_id", id)
				cancel()
				return
			}
		}
	}
}

func (m *ArtifactManager) buildOpts(file *models.MediaFile, a *Artifact) playback.TranscodeOpts {
	cfg := config.Config{}
	if m.liveCfg != nil {
		if c := m.liveCfg(); c != nil {
			cfg = *c
		}
	}
	return playback.TranscodeOpts{
		InputPath:          file.FilePath,
		SourceVideoCodec:   file.CodecVideo,
		TargetCodecVideo:   a.CodecVideo,
		TargetCodecAudio:   a.CodecAudio,
		TargetResolution:   a.Resolution,
		TargetBitrateKbps:  a.TargetBitrateKbps,
		AudioTrackIndex:    a.AudioTrackIndex,
		SubtitleTrackIndex: -1,
		FFmpegPath:         cfg.Playback.FFmpegPath,
		HWAccel:            cfg.Playback.HWAccel,
		HWDevice:           cfg.Playback.HWDevice,
		TotalDuration:      float64(file.Duration),
	}
}

// Hygiene retention windows. These remove only rows nothing can serve again —
// terminally-failed jobs (linked downloads already flipped to failed by
// reconciliation) and ready artifacts whose every referencing download row was
// deleted — plus ephemeral web rows past their convenience-record lifetime.
// The server-disk *quota* is download.artifact_max_bytes (see the download
// limits & restrictions design); this sweep is not a quota.
const (
	failedArtifactRetention    = 24 * time.Hour
	unlinkedArtifactRetention  = 30 * 24 * time.Hour
	ephemeralDownloadRetention = 7 * 24 * time.Hour
)

// Cleanup runs the hygiene sweep, then evicts ready artifacts (LRU first) once
// the total exceeds the byte budget, never removing one still linked by any
// active download row (managed or ephemeral) — only artifacts whose links are
// all terminal are evictable.
func (m *ArtifactManager) Cleanup(ctx context.Context) error {
	m.sweepStale(ctx)
	budget := m.downloadConfig().ArtifactMaxBytes
	if budget <= 0 {
		return nil // unlimited
	}
	total, err := m.repo.TotalReadyBytes(ctx)
	if err != nil {
		return err
	}
	if total <= budget {
		return nil
	}
	candidates, err := m.repo.ListReady(ctx) // least-recently-used first
	if err != nil {
		return err
	}
	for _, a := range candidates {
		if total <= budget {
			break
		}
		active, err := m.repo.HasActiveLink(ctx, a.ID)
		if err != nil {
			slog.WarnContext(ctx, "artifact link check failed", "component", "downloads", "artifact_id", a.ID, "error", err)
			continue
		}
		if active {
			continue
		}
		if a.OutputPath != "" {
			if err := os.Remove(a.OutputPath); err != nil && !os.IsNotExist(err) {
				slog.WarnContext(ctx, "removing evicted artifact file failed", "component", "downloads", "artifact_id", a.ID, "error", err)
			}
		}
		if err := m.repo.DeleteArtifact(ctx, a.ID); err != nil {
			slog.WarnContext(ctx, "deleting evicted artifact row failed", "component", "downloads", "artifact_id", a.ID, "error", err)
			continue
		}
		slog.InfoContext(ctx, "evicted download artifact (LRU)", "component", "downloads", "artifact_id", a.ID, "bytes", a.FileSize)
		total -= a.FileSize
	}
	return nil
}

// sweepStale is the age-based hygiene pass: cold terminally-failed artifacts
// (with their leftover .part files), orphaned ready artifacts no download row
// references, and expired ephemeral download rows. Best-effort; every step
// logs and continues.
func (m *ArtifactManager) sweepStale(ctx context.Context) {
	if !m.maintenanceDue(&m.lastStaleSweep) {
		return
	}
	now := time.Now()
	if failed, err := m.repo.ListFailedBefore(ctx, now.Add(-failedArtifactRetention)); err != nil {
		slog.WarnContext(ctx, "failed-artifact sweep list failed", "component", "downloads", "error", err)
	} else {
		for _, a := range failed {
			m.removeArtifact(ctx, a, "failed")
		}
	}
	if orphans, err := m.repo.ListUnlinkedReadyBefore(ctx, now.Add(-unlinkedArtifactRetention)); err != nil {
		slog.WarnContext(ctx, "unlinked-artifact sweep list failed", "component", "downloads", "error", err)
	} else {
		for _, a := range orphans {
			m.removeArtifact(ctx, a, "unlinked")
		}
	}
	if m.downloads != nil {
		if n, err := m.downloads.PruneEphemeralOlderThan(ctx, now.Add(-ephemeralDownloadRetention)); err != nil {
			slog.WarnContext(ctx, "ephemeral download prune failed", "component", "downloads", "error", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "pruned expired ephemeral downloads", "component", "downloads", "rows", n)
		}
	}
}

// removeArtifact deletes an artifact's output file, its .part leftover, and
// its row. Used by the hygiene sweep for rows nothing can serve again.
func (m *ArtifactManager) removeArtifact(ctx context.Context, a *Artifact, reason string) {
	if a.OutputPath != "" {
		if err := os.Remove(a.OutputPath); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, "removing swept artifact file failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		}
		if err := os.Remove(a.OutputPath + ".part"); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, "removing swept artifact partial failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		}
	}
	if err := m.repo.DeleteArtifact(ctx, a.ID); err != nil {
		slog.WarnContext(ctx, "deleting swept artifact row failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return
	}
	slog.InfoContext(ctx, "swept stale download artifact", "component", "downloads", "artifact_id", a.ID, "reason", reason, "bytes", a.FileSize)
}

// backoffFor returns the retry delay for the next attempt after a failure.
func backoffFor(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(attempts) * 30 * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

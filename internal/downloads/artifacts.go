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

	mu   sync.Mutex
	kick func()
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

// Ensure deduplicates and (when new) enqueues an encode job for file in the
// given format, returning the current artifact row. The deterministic
// output_path keeps a reclaimed job idempotent.
func (m *ArtifactManager) Ensure(ctx context.Context, file *models.MediaFile, format string, target playback.PrepareTarget) (*Artifact, error) {
	cfg := m.downloadConfig()
	hash := paramsHash(format, target.Container, target.CodecVideo, target.CodecAudio, target.Resolution, target.AudioTrackIndex, false)
	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	a := &Artifact{
		ID:              id,
		MediaFileID:     file.ID,
		Format:          format,
		ParamsHash:      hash,
		Container:       target.Container,
		CodecVideo:      target.CodecVideo,
		CodecAudio:      target.CodecAudio,
		Resolution:      target.Resolution,
		AudioTrackIndex: target.AudioTrackIndex,
		OutputPath:      artifactOutputPath(cfg.ArtifactDir, file.ID, format, hash),
		MaxAttempts:     artifactMaxAttempts,
	}
	row, created, err := m.repo.EnsureQueued(ctx, a)
	if err != nil {
		return nil, err
	}
	if row.Status == ArtifactReady {
		_ = m.repo.TouchLastUsed(ctx, row.ID)
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
		kick()
	}
}

// RunOnce performs a startup-safe recovery sweep and then drains the queue
// until empty. It is safe to call concurrently across nodes; FOR UPDATE SKIP
// LOCKED prevents double-encoding.
func (m *ArtifactManager) RunOnce(ctx context.Context) error {
	m.recover(ctx)
	return m.drain(ctx)
}

// recover is the startup sweep: reclaim expired-lease running rows, fail linked
// downloads for terminal ones, and re-queue ready artifacts whose output file
// is missing on disk.
func (m *ArtifactManager) recover(ctx context.Context) {
	reclaimed, err := m.repo.ReclaimExpiredLeases(ctx)
	if err != nil {
		slog.Warn("download artifact lease reclaim failed", "error", err)
	}
	for _, rc := range reclaimed {
		if rc.Terminal {
			m.failLinkedDownloads(ctx, rc.ID, "encode exhausted retries")
		}
	}

	ready, err := m.repo.ListReady(ctx)
	if err != nil {
		slog.Warn("download artifact ready scan failed", "error", err)
		return
	}
	for _, a := range ready {
		if a.OutputPath == "" {
			continue
		}
		if _, statErr := os.Stat(a.OutputPath); statErr != nil {
			slog.Warn("download artifact output missing, re-queuing", "artifact_id", a.ID, "path", a.OutputPath)
			if err := m.repo.Requeue(ctx, a.ID); err != nil {
				slog.Warn("re-queue artifact failed", "artifact_id", a.ID, "error", err)
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
		job, err := m.repo.ClaimNext(ctx, m.owner, artifactLease)
		if errors.Is(err, ErrNoArtifactJob) {
			break
		}
		if err != nil {
			wg.Wait()
			return err // includes context cancellation (pgx honors ctx)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
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
	go m.heartbeatLoop(hbCtx, a.ID)

	file, err := m.fileRepo.GetByID(ctx, a.MediaFileID)
	if err != nil || file == nil {
		m.failJob(ctx, a, "source media file unavailable")
		return
	}

	opts := m.buildOpts(file, a)
	if err := m.preparer.PrepareFile(ctx, opts, a.OutputPath); err != nil {
		slog.Warn("download artifact encode failed", "artifact_id", a.ID, "error", err)
		m.failJob(ctx, a, err.Error())
		return
	}

	var size int64
	if fi, statErr := os.Stat(a.OutputPath); statErr == nil {
		size = fi.Size()
	}
	if err := m.repo.MarkReady(ctx, a.ID, a.OutputPath, size); err != nil {
		slog.Error("marking artifact ready failed", "artifact_id", a.ID, "error", err)
		return
	}
	flipped, err := m.downloads.MarkLinkedDownloadsReady(ctx, a.ID, size)
	if err != nil {
		slog.Error("flipping linked downloads ready failed", "artifact_id", a.ID, "error", err)
		return
	}
	for _, d := range flipped {
		m.publish(ctx, d)
	}
}

func (m *ArtifactManager) failJob(ctx context.Context, a *Artifact, msg string) {
	terminal, err := m.repo.MarkFailedOrRetry(ctx, a.ID, msg, backoffFor(a.Attempts))
	if err != nil {
		slog.Error("marking artifact failed/retry errored", "artifact_id", a.ID, "error", err)
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
		slog.Error("flipping linked downloads failed errored", "artifact_id", artifactID, "error", err)
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

func (m *ArtifactManager) heartbeatLoop(ctx context.Context, id string) {
	ticker := time.NewTicker(artifactHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := m.repo.Heartbeat(ctx, id, m.owner, artifactLease)
			if err != nil || !ok {
				return // lost the lease (or row gone); stop heartbeating
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
		AudioTrackIndex:    a.AudioTrackIndex,
		SubtitleTrackIndex: -1,
		FFmpegPath:         cfg.Playback.FFmpegPath,
		HWAccel:            cfg.Playback.HWAccel,
		HWDevice:           cfg.Playback.HWDevice,
		TotalDuration:      float64(file.Duration),
	}
}

// Cleanup evicts ready artifacts (LRU first) once the total exceeds the byte
// budget, never removing one still linked by a non-terminal download.
func (m *ArtifactManager) Cleanup(ctx context.Context) error {
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
			slog.Warn("artifact link check failed", "artifact_id", a.ID, "error", err)
			continue
		}
		if active {
			continue
		}
		if a.OutputPath != "" {
			if err := os.Remove(a.OutputPath); err != nil && !os.IsNotExist(err) {
				slog.Warn("removing evicted artifact file failed", "artifact_id", a.ID, "error", err)
			}
		}
		if err := m.repo.DeleteArtifact(ctx, a.ID); err != nil {
			slog.Warn("deleting evicted artifact row failed", "artifact_id", a.ID, "error", err)
			continue
		}
		slog.Info("evicted download artifact (LRU)", "artifact_id", a.ID, "bytes", a.FileSize)
		total -= a.FileSize
	}
	return nil
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

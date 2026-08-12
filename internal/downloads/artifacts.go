package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	artifactLease       = 2 * time.Minute
	artifactHeartbeat   = 40 * time.Second
	artifactMaxAttempts = 3
)

// PreparedArtifact describes either a local prepared file or an opaque artifact
// retained by a transcode node.
type PreparedArtifact struct {
	OutputPath       string
	OriginNodeID     int
	OriginNodeURL    string
	OriginNodeGroup  string
	OriginArtifactID string
	FileSize         int64
}

func (a PreparedArtifact) Remote() bool {
	return a.OriginNodeID > 0 && a.OriginNodeURL != "" && a.OriginArtifactID != ""
}

// EncodePreparer produces a single finalized artifact. The default
// implementation calls playback.PrepareFile and returns a local path; pooled
// implementations may return an opaque remote locator instead.
type EncodePreparer interface {
	PrepareFile(ctx context.Context, artifactID string, opts playback.TranscodeOpts, outputPath string) (PreparedArtifact, error)
}

type playbackPreparer struct{}

func (playbackPreparer) PrepareFile(ctx context.Context, _ string, opts playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	if err := playback.PrepareFile(ctx, opts, outputPath); err != nil {
		return PreparedArtifact{}, err
	}
	stat, err := os.Stat(outputPath)
	if err != nil {
		return PreparedArtifact{}, fmt.Errorf("stat prepared artifact: %w", err)
	}
	return PreparedArtifact{OutputPath: outputPath, FileSize: stat.Size()}, nil
}

// NewPlaybackPreparer returns the production EncodePreparer (ffmpeg-backed).
func NewPlaybackPreparer() EncodePreparer { return playbackPreparer{} }

// remoteArtifactLifecycle is implemented by the pooled preparer so recovery
// and quota cleanup can manage node-local artifacts without filesystem access.
type remoteArtifactLifecycle interface {
	ResolveArtifact(ctx context.Context, artifact *Artifact) error
	StatArtifact(ctx context.Context, artifact *Artifact) (downloadprepare.Result, error)
	DeleteArtifact(ctx context.Context, artifact *Artifact) error
}

// ArtifactNotifier publishes an event when a linked download changes state.
type ArtifactNotifier func(ctx context.Context, d *Download)

// ArtifactManager owns the durable encode queue: it ensures/deduplicates encode
// jobs, drains them through a bounded worker pool with leased heartbeats, and
// recovers stranded jobs on startup.
type ArtifactManager struct {
	repo                 *ArtifactRepository
	downloads            *Repository
	fileRepo             FileResolver
	preparer             EncodePreparer
	owner                string
	liveCfg              func() *config.Config
	notify               ArtifactNotifier
	remoteRecoveryBudget time.Duration
	remoteCleanupBudget  time.Duration

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

// ReportRemoteArtifactMissing fences a proxy-observed 404 against the signed
// database-row and node locator, then atomically requeues the artifact and its
// linked downloads. Stale tokens are harmless: the exact-locator transition
// only applies while the complete locator still owns the ready row.
func (m *ArtifactManager) ReportRemoteArtifactMissing(ctx context.Context, artifactID, originNodeURL, originArtifactID string) error {
	if m == nil || m.repo == nil || strings.TrimSpace(artifactID) == "" ||
		strings.TrimSpace(originNodeURL) == "" || !downloadprepare.ValidArtifactID(originArtifactID) {
		return errors.New("remote artifact locator unavailable for missing report")
	}
	artifact, err := m.repo.GetByID(ctx, artifactID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if artifact.Status != ArtifactReady || artifact.OriginNodeURL != originNodeURL || artifact.OriginArtifactID != originArtifactID {
		return nil
	}
	linked, applied, err := m.repo.RequeueRemoteExactLocator(ctx, artifact)
	if err != nil || !applied {
		return err
	}
	for _, download := range linked {
		m.publish(ctx, download)
	}
	slog.WarnContext(ctx, "remote download artifact re-queued", "component", "downloads", "artifact_id", artifact.ID, "node", artifact.OriginNodeURL, "reason", "proxy observed remote output missing")
	m.triggerDrain()
	return nil
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
	if lifecycle, ok := m.preparer.(remoteArtifactLifecycle); ok && a.OriginArtifactID != "" {
		if err := m.resolveRemoteArtifact(ctx, lifecycle, a); err != nil {
			if !errors.Is(err, ErrArtifactOriginRemoved) {
				return nil, err
			}
			if m.requeueRemoteArtifact(ctx, a, "origin node removed") {
				return nil, fmt.Errorf("artifact origin was removed and preparation was requeued: %w", ErrDownloadNotActive)
			}
			return nil, fmt.Errorf("artifact origin was removed: %w", errors.Join(ErrDownloadNotActive, err))
		}
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

// RunOnce repairs queue state and drains pending work before probing ready
// remote artifacts. Network health checks therefore never block newly queued
// downloads at startup; any artifacts requeued by the bounded probe pass are
// drained in the second pass.
func (m *ArtifactManager) RunOnce(ctx context.Context) error {
	m.recoverQueueState(ctx)
	if err := m.drain(ctx); err != nil {
		return err
	}
	m.recoverReadyArtifacts(ctx)
	return m.drain(ctx)
}

// recover is retained as the focused recovery entrypoint used by tests.
func (m *ArtifactManager) recover(ctx context.Context) {
	m.recoverQueueState(ctx)
	m.recoverReadyArtifacts(ctx)
}

func (m *ArtifactManager) recoverQueueState(ctx context.Context) {
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
}

// recoverReadyArtifacts checks local paths directly and groups remote probes
// by origin. Origins run concurrently, but each origin is short-circuited after
// its first transient failure so one blackholed node costs one bounded timeout,
// never one timeout per artifact.
func (m *ArtifactManager) recoverReadyArtifacts(ctx context.Context) {
	if !m.maintenanceDue(&m.lastDiskSweep) {
		return
	}
	ready, err := m.repo.ListReady(ctx)
	if err != nil {
		slog.WarnContext(ctx, "download artifact ready scan failed", "component", "downloads", "error", err)
		return
	}
	remoteGroups := make(map[string][]*Artifact)
	for _, a := range ready {
		if a.OriginArtifactID != "" {
			key := fmt.Sprintf("%d\x00%s", a.OriginNodeID, a.OriginNodeURL)
			remoteGroups[key] = append(remoteGroups[key], a)
			continue
		}
		if a.OutputPath != "" {
			if _, statErr := os.Stat(a.OutputPath); statErr != nil {
				slog.WarnContext(ctx, "download artifact output missing, re-queuing", "component", "downloads", "artifact_id", a.ID, "path", a.OutputPath)
				if err := m.repo.Requeue(ctx, a.ID); err != nil {
					slog.WarnContext(ctx, "re-queue artifact failed", "component", "downloads", "artifact_id", a.ID, "error", err)
				}
			}
		}
	}
	if len(remoteGroups) == 0 {
		return
	}
	lifecycle, ok := m.preparer.(remoteArtifactLifecycle)
	if !ok {
		slog.WarnContext(ctx, "remote download artifacts cannot be checked", "component", "downloads")
		return
	}
	budget := m.remoteRecoveryBudget
	if budget <= 0 {
		budget = defaultRemoteRecoveryBudget
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	const maxConcurrentOriginProbes = 4
	sem := make(chan struct{}, maxConcurrentOriginProbes)
	var wg sync.WaitGroup
	for _, artifacts := range remoteGroups {
		artifacts := artifacts
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-recoveryCtx.Done():
				return
			}
			m.probeRemoteArtifactGroup(recoveryCtx, lifecycle, artifacts)
		}()
	}
	wg.Wait()
}

func (m *ArtifactManager) probeRemoteArtifactGroup(ctx context.Context, lifecycle remoteArtifactLifecycle, artifacts []*Artifact) {
	for _, a := range artifacts {
		if err := m.resolveRemoteArtifact(ctx, lifecycle, a); err != nil {
			if errors.Is(err, ErrArtifactOriginRemoved) {
				m.requeueRemoteArtifact(ctx, a, "origin node removed")
				continue
			}
			if errors.Is(err, ErrDownloadNotActive) {
				// The row changed concurrently; the origin itself is still healthy.
				continue
			}
			slog.WarnContext(ctx, "remote download artifact resolution failed; skipping remaining origin batch", "component", "downloads", "artifact_id", a.ID, "error", err)
			return
		}
		result, err := lifecycle.StatArtifact(ctx, a)
		switch {
		case errors.Is(err, ErrArtifactOriginRemoved):
			m.requeueRemoteArtifact(ctx, a, "origin node removed")
		case errors.Is(err, downloadprepare.ErrArtifactNotFound):
			m.requeueRemoteArtifact(ctx, a, "remote output missing")
		case err != nil:
			// One node-level failure suppresses the rest of this origin's batch.
			slog.WarnContext(ctx, "remote download artifact check failed; skipping remaining origin batch", "component", "downloads", "artifact_id", a.ID, "node", a.OriginNodeURL, "error", err)
			return
		case result.FileSize != a.FileSize:
			m.requeueWrongSizedRemoteArtifact(ctx, lifecycle, a)
		}
	}
}

// requeueWrongSizedRemoteArtifact commits the durable fence before deleting
// bytes, but delays replacement work until the known-invalid locator has been
// quarantined. If deletion fails, the transaction's orphan row retains the
// exact locator for the regular retrying cleanup pass.
func (m *ArtifactManager) requeueWrongSizedRemoteArtifact(ctx context.Context, lifecycle remoteArtifactLifecycle, a *Artifact) {
	applied, err := m.requeueRemoteArtifactWithFence(ctx, a, "remote output size mismatch", false, false)
	if err != nil {
		slog.WarnContext(ctx, "re-queue remote artifact failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return
	}
	if !applied {
		return
	}
	if err := lifecycle.DeleteArtifact(ctx, a); err != nil {
		slog.WarnContext(ctx, "deleting rejected remote artifact failed", "component", "downloads", "artifact_id", a.ID, "node", a.OriginNodeURL, "error", err)
	}
	m.triggerDrain()
}

func (m *ArtifactManager) resolveRemoteArtifact(ctx context.Context, lifecycle remoteArtifactLifecycle, artifact *Artifact) error {
	oldURL, oldGroup := artifact.OriginNodeURL, artifact.OriginNodeGroup
	if err := lifecycle.ResolveArtifact(ctx, artifact); err != nil {
		return err
	}
	if artifact.OriginNodeURL == oldURL && artifact.OriginNodeGroup == oldGroup {
		return nil
	}
	applied, err := m.repo.RefreshRemoteLocator(ctx, artifact)
	if err != nil {
		return err
	}
	if !applied {
		return fmt.Errorf("remote artifact changed while refreshing its origin: %w", ErrDownloadNotActive)
	}
	return nil
}

func (m *ArtifactManager) requeueRemoteArtifact(ctx context.Context, a *Artifact, reason string) bool {
	applied, err := m.requeueRemoteArtifactNow(ctx, a, reason)
	if err != nil {
		slog.WarnContext(ctx, "re-queue remote artifact failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return false
	}
	return applied
}

// requeueRemoteArtifactNow synchronously fences a stale remote locator, resets
// every linked download, and schedules the abandoned node-local file for
// cleanup. Request paths use the returned error so a failed state transition is
// never disguised as an ordinary missing catalog item.
func (m *ArtifactManager) requeueRemoteArtifactNow(ctx context.Context, a *Artifact, reason string) (bool, error) {
	return m.requeueRemoteArtifactWithFence(ctx, a, reason, false, true)
}

func (m *ArtifactManager) requeueRemoteArtifactExactNow(ctx context.Context, a *Artifact, reason string) (bool, error) {
	return m.requeueRemoteArtifactWithFence(ctx, a, reason, true, true)
}

func (m *ArtifactManager) requeueRemoteArtifactWithFence(ctx context.Context, a *Artifact, reason string, exactURL, triggerDrain bool) (bool, error) {
	if m == nil || m.repo == nil || a == nil || a.ID == "" || a.OriginNodeID <= 0 || a.OriginArtifactID == "" {
		return false, errors.New("remote artifact locator unavailable for requeue")
	}
	var linked []*Download
	var applied bool
	var err error
	if exactURL {
		linked, applied, err = m.repo.RequeueRemoteExactLocator(ctx, a)
	} else {
		linked, applied, err = m.repo.RequeueRemote(ctx, a)
	}
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	for _, download := range linked {
		m.publish(ctx, download)
	}
	slog.WarnContext(ctx, "remote download artifact re-queued", "component", "downloads", "artifact_id", a.ID, "node", a.OriginNodeURL, "reason", reason)
	if triggerDrain {
		m.triggerDrain()
	}
	return true, nil
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
	// Each lease attempt owns a distinct node-local object. A worker that loses
	// the readiness fence can therefore queue its object for deletion without
	// racing the replacement worker's output on the same node.
	remoteAttemptID := a.ID + "-" + uuid.NewString()
	prepared, err := m.preparer.PrepareFile(hbCtx, remoteAttemptID, opts, a.OutputPath)
	if err != nil {
		if prepared.Remote() {
			m.enqueueRemoteCleanup(ctx, a.ID, prepared, true)
		}
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

	remoteFieldsPresent := prepared.OriginNodeID != 0 || prepared.OriginNodeURL != "" || prepared.OriginNodeGroup != "" || prepared.OriginArtifactID != ""
	if prepared.FileSize <= 0 || (remoteFieldsPresent && !prepared.Remote()) || (!prepared.Remote() && prepared.OutputPath == "") {
		msg := "prepared artifact returned an invalid storage locator"
		slog.WarnContext(ctx, "prepared artifact returned an invalid storage locator", "component", "downloads", "artifact_id", a.ID)
		if prepared.Remote() {
			m.enqueueRemoteCleanup(ctx, a.ID, prepared, true)
		}
		m.failJob(ctx, a, msg)
		return
	}
	size := prepared.FileSize
	// Fenced on lease ownership: if we lost the lease between encode and commit,
	// applied is false and the current owner is responsible for flipping links —
	// do not flip them here or we would race/duplicate that owner's work.
	outputPath := prepared.OutputPath
	if prepared.Remote() {
		// Keep the deterministic API-local fallback path in the row so a later
		// remote-missing requeue can safely fall back to integrated preparation.
		outputPath = a.OutputPath
	}
	applied, err := m.repo.MarkReady(ctx, a.ID, m.owner, outputPath, prepared.OriginNodeID, prepared.OriginNodeURL, prepared.OriginNodeGroup, prepared.OriginArtifactID, size)
	if err != nil {
		slog.ErrorContext(ctx, "marking artifact ready failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		m.cleanupRejectedPrepared(ctx, a.ID, prepared)
		return
	}
	if !applied {
		slog.WarnContext(ctx, "download artifact ready skipped; lease lost", "component", "downloads", "artifact_id", a.ID)
		m.enqueueRemoteCleanup(ctx, a.ID, prepared, true)
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

func (m *ArtifactManager) cleanupRejectedPrepared(ctx context.Context, artifactID string, prepared PreparedArtifact) {
	if !prepared.Remote() {
		return
	}
	// Exec can report a transport error after PostgreSQL committed. Re-read the
	// row before classifying this attempt as abandoned so cleanup never deletes
	// the locator that actually won the readiness fence.
	current, err := m.repo.GetByID(ctx, artifactID)
	if err == nil {
		if current.Status == ArtifactReady &&
			current.OriginNodeID == prepared.OriginNodeID &&
			current.OriginArtifactID == prepared.OriginArtifactID {
			return
		}
	}
	// A successful reread (or a definitively missing row) proves this locator
	// lost. Other database errors leave MarkReady's outcome indeterminate, so a
	// failed queue write must not fall back to deleting potentially-owned bytes.
	safeImmediateDelete := err == nil || errors.Is(err, ErrNotFound)
	m.enqueueRemoteCleanup(ctx, artifactID, prepared, safeImmediateDelete)
}

func (m *ArtifactManager) enqueueRemoteCleanup(ctx context.Context, artifactID string, prepared PreparedArtifact, safeImmediateDelete bool) bool {
	if !prepared.Remote() {
		return true
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.repo.EnqueueRemoteOrphan(cleanupCtx, artifactID, prepared.OriginNodeID, prepared.OriginNodeURL, prepared.OriginArtifactID); err != nil {
		slog.WarnContext(ctx, "persisting abandoned remote artifact failed", "component", "downloads", "artifact_id", prepared.OriginArtifactID, "node", prepared.OriginNodeURL, "error", err)
		// A unique attempt ID permits best-effort immediate deletion when the
		// caller knows the readiness write did not commit. An indeterminate write
		// must retain the bytes if the verification queue is unavailable.
		if safeImmediateDelete {
			lifecycle, ok := m.preparer.(remoteArtifactLifecycle)
			if !ok {
				return false
			}
			artifact := &Artifact{OriginNodeID: prepared.OriginNodeID, OriginNodeURL: prepared.OriginNodeURL, OriginNodeGroup: prepared.OriginNodeGroup, OriginArtifactID: prepared.OriginArtifactID}
			if deleteErr := lifecycle.DeleteArtifact(cleanupCtx, artifact); deleteErr == nil {
				return true
			}
		}
		return false
	}
	return true
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
	sourceVideoCodec, sourceVideoProfile, sourceVideoBitDepth := playback.SourceVideoTranscodeFacts(file)
	return playback.TranscodeOpts{
		InputPath:           file.FilePath,
		SourceVideoCodec:    sourceVideoCodec,
		SourceVideoProfile:  sourceVideoProfile,
		SourceVideoBitDepth: sourceVideoBitDepth,
		TargetCodecVideo:    a.CodecVideo,
		TargetCodecAudio:    a.CodecAudio,
		TargetResolution:    a.Resolution,
		TargetBitrateKbps:   a.TargetBitrateKbps,
		AudioTrackIndex:     a.AudioTrackIndex,
		SubtitleTrackIndex:  -1,
		FFmpegPath:          cfg.Playback.FFmpegPath,
		HWAccel:             cfg.Playback.HWAccel,
		HWDevice:            cfg.Playback.HWDevice,
		TotalDuration:       float64(file.Duration),
	}
}

// Hygiene retention windows. These remove only rows nothing can serve again —
// terminally-failed jobs (linked downloads already flipped to failed by
// reconciliation) and ready artifacts whose every referencing download row was
// deleted — plus ephemeral web rows past their convenience-record lifetime.
// The server-disk *quota* is download.artifact_max_bytes (see the download
// limits & restrictions design); this sweep is not a quota.
const (
	failedArtifactRetention     = 24 * time.Hour
	unlinkedArtifactRetention   = 30 * 24 * time.Hour
	ephemeralDownloadRetention  = 7 * 24 * time.Hour
	defaultRemoteRecoveryBudget = 15 * time.Second
	defaultRemoteCleanupBudget  = 15 * time.Second
)

// Cleanup runs the hygiene sweep, then evicts ready artifacts (LRU first) once
// the total exceeds the byte budget, never removing one still linked by any
// active download row (managed or ephemeral) — only artifacts whose links are
// all terminal are evictable.
func (m *ArtifactManager) Cleanup(ctx context.Context) error {
	m.cleanupRemoteOrphans(ctx)
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
		if !m.deleteArtifactBytes(ctx, a) {
			continue
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

func (m *ArtifactManager) cleanupRemoteOrphans(ctx context.Context) {
	lifecycle, ok := m.preparer.(remoteArtifactLifecycle)
	if !ok {
		return
	}
	budget := m.remoteCleanupBudget
	if budget <= 0 {
		budget = defaultRemoteCleanupBudget
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	orphans, err := m.repo.ListRemoteOrphansDue(cleanupCtx, 100)
	if err != nil {
		slog.WarnContext(ctx, "listing abandoned remote artifacts failed", "component", "downloads", "error", err)
		return
	}
	groups := make(map[string][]RemoteArtifactOrphan)
	for _, orphan := range orphans {
		key := fmt.Sprintf("%d\x00%s", orphan.OriginNodeID, orphan.OriginNodeURL)
		groups[key] = append(groups[key], orphan)
	}
	const maxConcurrentOriginDeletes = 4
	sem := make(chan struct{}, maxConcurrentOriginDeletes)
	var wg sync.WaitGroup
	for _, group := range groups {
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-cleanupCtx.Done():
				return
			}
			for _, orphan := range group {
				owned, claimed, err := m.repo.PrepareRemoteOrphanCleanup(cleanupCtx, orphan)
				if err != nil {
					slog.WarnContext(ctx, "claiming abandoned remote artifact cleanup failed; skipping remaining origin batch", "component", "downloads", "artifact_id", orphan.OriginArtifactID, "error", err)
					return
				}
				if !claimed || owned {
					continue
				}
				artifact := &Artifact{
					OriginNodeID: orphan.OriginNodeID, OriginNodeURL: orphan.OriginNodeURL,
					OriginArtifactID: orphan.OriginArtifactID,
				}
				if err := lifecycle.DeleteArtifact(cleanupCtx, artifact); err != nil {
					slog.WarnContext(ctx, "abandoned remote artifact cleanup failed; skipping remaining origin batch", "component", "downloads", "artifact_id", orphan.OriginArtifactID, "node", orphan.OriginNodeURL, "error", err)
					return
				}
				if err := m.repo.DeleteRemoteOrphan(cleanupCtx, orphan.ID); err != nil {
					slog.WarnContext(ctx, "clearing abandoned remote artifact cleanup row failed", "component", "downloads", "artifact_id", orphan.OriginArtifactID, "error", err)
					return
				}
			}
		}()
	}
	wg.Wait()
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
	if !m.deleteArtifactBytes(ctx, a) {
		return
	}
	if err := m.repo.DeleteArtifact(ctx, a.ID); err != nil {
		slog.WarnContext(ctx, "deleting swept artifact row failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return
	}
	slog.InfoContext(ctx, "swept stale download artifact", "component", "downloads", "artifact_id", a.ID, "reason", reason, "bytes", a.FileSize)
}

func (m *ArtifactManager) deleteArtifactBytes(ctx context.Context, a *Artifact) bool {
	if a.OriginArtifactID != "" {
		lifecycle, ok := m.preparer.(remoteArtifactLifecycle)
		if !ok {
			slog.WarnContext(ctx, "remote artifact cleanup unavailable", "component", "downloads", "artifact_id", a.ID)
			return false
		}
		if err := lifecycle.DeleteArtifact(ctx, a); err != nil {
			slog.WarnContext(ctx, "removing remote artifact failed", "component", "downloads", "artifact_id", a.ID, "node", a.OriginNodeURL, "error", err)
			return false
		}
		return true
	}
	if a.OutputPath == "" {
		return true
	}
	if err := os.Remove(a.OutputPath); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, "removing artifact file failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return false
	}
	if err := os.Remove(a.OutputPath + ".part"); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, "removing artifact partial failed", "component", "downloads", "artifact_id", a.ID, "error", err)
		return false
	}
	return true
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

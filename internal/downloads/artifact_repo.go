package downloads

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const artifactColumns = `id, media_file_id, format, params_hash, container, codec_video, codec_audio, audio_recipe_version,
	resolution, audio_track_index, target_bitrate_kbps, tone_map_policy, tone_map_mode, tone_map_source_kind, tone_map_recipe_version, tone_map_preflight_required, tone_map_source_revision,
	tone_map_dv_config_present, tone_map_dv_bl_compat_id_present, tone_map_dv_bl_present, tone_map_dv_rpu_present, output_path,
	origin_node_id, origin_node_url, origin_node_group, origin_artifact_id, file_size, status, error_message,
	attempts, max_attempts, lease_owner, lease_expires_at, next_retry_at,
	created_at, completed_at, last_used_at`

// ArtifactRepository provides CRUD + durable-queue operations for
// download_artifacts.
type ArtifactRepository struct {
	pool *pgxpool.Pool
}

// RemoteArtifactOrphan is a node-local cleanup candidate. The durable queue
// retains abandoned locators and verifies indeterminate readiness writes before
// deleting bytes, so transient failures cannot leak or delete a winning file.
type RemoteArtifactOrphan struct {
	ID                 int64
	DownloadArtifactID string
	OriginNodeID       int
	OriginNodeURL      string
	OriginArtifactID   string
	Attempts           int
}

// NewArtifactRepository creates an ArtifactRepository.
func NewArtifactRepository(pool *pgxpool.Pool) *ArtifactRepository {
	return &ArtifactRepository{pool: pool}
}

// scanArtifact decodes one artifact row from the repository query shape.
func scanArtifact(row pgx.Row) (*Artifact, error) {
	var a Artifact
	var leaseOwner *string
	if err := row.Scan(
		&a.ID, &a.MediaFileID, &a.Format, &a.ParamsHash, &a.Container, &a.CodecVideo, &a.CodecAudio, &a.AudioRecipeVersion,
		&a.Resolution, &a.AudioTrackIndex, &a.TargetBitrateKbps, &a.ToneMapPolicy, &a.ToneMapMode, &a.ToneMapSourceKind, &a.ToneMapRecipeVersion, &a.ToneMapPreflightRequired, &a.ToneMapSourceRevision,
		&a.ToneMapDVConfigPresent, &a.ToneMapDVBLCompatIDPresent, &a.ToneMapDVBLPresent, &a.ToneMapDVRPUPresent, &a.OutputPath,
		&a.OriginNodeID, &a.OriginNodeURL, &a.OriginNodeGroup, &a.OriginArtifactID, &a.FileSize, &a.Status, &a.ErrorMessage,
		&a.Attempts, &a.MaxAttempts, &leaseOwner, &a.LeaseExpiresAt, &a.NextRetryAt,
		&a.CreatedAt, &a.CompletedAt, &a.LastUsedAt,
	); err != nil {
		return nil, err
	}
	a.LeaseOwner = deref(leaseOwner)
	return &a, nil
}

// EnsureQueued inserts the artifact if no row exists for its
// (media_file_id, format, params_hash), then returns the current row (existing
// or freshly queued) and whether it was newly created.
func (r *ArtifactRepository) EnsureQueued(ctx context.Context, a *Artifact) (*Artifact, bool, error) {
	if a.ToneMapPolicy == "" {
		a.ToneMapPolicy = tonemap.PolicyNone
	}
	status := queuedArtifactStatus(a.ToneMapMode, a.AudioRecipeVersion)
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO download_artifacts
			(id, media_file_id, format, params_hash, container, codec_video, codec_audio, audio_recipe_version,
			 resolution, audio_track_index, target_bitrate_kbps, tone_map_policy, tone_map_mode, tone_map_source_kind, tone_map_recipe_version, tone_map_preflight_required, tone_map_source_revision,
			 tone_map_dv_config_present, tone_map_dv_bl_compat_id_present, tone_map_dv_bl_present, tone_map_dv_rpu_present, output_path, status, max_attempts)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
		 ON CONFLICT (media_file_id, format, params_hash) DO NOTHING`,
		a.ID, a.MediaFileID, a.Format, a.ParamsHash, a.Container, a.CodecVideo, a.CodecAudio, a.AudioRecipeVersion,
		a.Resolution, a.AudioTrackIndex, a.TargetBitrateKbps, a.ToneMapPolicy, a.ToneMapMode, a.ToneMapSourceKind, a.ToneMapRecipeVersion, a.ToneMapPreflightRequired, a.ToneMapSourceRevision,
		a.ToneMapDVConfigPresent, a.ToneMapDVBLCompatIDPresent, a.ToneMapDVBLPresent, a.ToneMapDVRPUPresent, a.OutputPath, status, a.MaxAttempts,
	)
	if err != nil {
		return nil, false, fmt.Errorf("ensuring artifact: %w", err)
	}
	row, err := r.GetByKey(ctx, a.MediaFileID, a.Format, a.ParamsHash)
	if err != nil {
		return nil, false, err
	}
	return row, tag.RowsAffected() > 0, nil
}

// GetByID returns an artifact by id, or ErrNotFound.
func (r *ArtifactRepository) GetByID(ctx context.Context, id string) (*Artifact, error) {
	a, err := scanArtifact(r.pool.QueryRow(ctx, `SELECT `+artifactColumns+` FROM download_artifacts WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting artifact: %w", err)
	}
	return a, nil
}

// GetByKey returns the artifact for a (media_file_id, format, params_hash), or ErrNotFound.
func (r *ArtifactRepository) GetByKey(ctx context.Context, mediaFileID int, format, paramsHash string) (*Artifact, error) {
	a, err := scanArtifact(r.pool.QueryRow(ctx,
		`SELECT `+artifactColumns+` FROM download_artifacts
		 WHERE media_file_id = $1 AND format = $2 AND params_hash = $3`,
		mediaFileID, format, paramsHash,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting artifact by key: %w", err)
	}
	return a, nil
}

// ClaimNext atomically claims one runnable job: a queued row whose backoff has
// elapsed, or a running row whose lease has expired (lease stealing). FOR UPDATE
// SKIP LOCKED makes concurrent workers (and nodes) safe without double-encoding.
// Returns ErrNoArtifactJob when nothing is claimable.
func (r *ArtifactRepository) ClaimNext(ctx context.Context, owner string, lease time.Duration) (*Artifact, error) {
	leaseSecs := int(lease.Seconds())
	if leaseSecs <= 0 {
		leaseSecs = 60
	}
	a, err := scanArtifact(r.pool.QueryRow(ctx,
		`UPDATE download_artifacts
		 SET status = CASE
		                  WHEN status IN ('audio_v2_queued', 'audio_v2_running') THEN 'audio_v2_running'
		                  WHEN status IN ('tone_map_queued', 'tone_map_running') THEN 'tone_map_running'
		                  ELSE 'running'
		              END,
		     lease_owner = $1, lease_expires_at = now() + make_interval(secs => $2),
		     attempts = attempts + 1
		 WHERE id = (
		     SELECT id FROM download_artifacts
		     WHERE (status IN ('queued', 'tone_map_queued', 'audio_v2_queued') AND (next_retry_at IS NULL OR next_retry_at <= now()))
		        OR (status IN ('running', 'tone_map_running', 'audio_v2_running') AND lease_expires_at < now())
		     ORDER BY created_at
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING `+artifactColumns,
		owner, leaseSecs,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoArtifactJob
		}
		return nil, fmt.Errorf("claiming artifact job: %w", err)
	}
	return a, nil
}

// Heartbeat extends a running job's lease while it is still owned by owner.
// Returns false when the lease was lost (another worker stole it).
func (r *ArtifactRepository) Heartbeat(ctx context.Context, id, owner string, lease time.Duration) (bool, error) {
	leaseSecs := int(lease.Seconds())
	if leaseSecs <= 0 {
		leaseSecs = 60
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE download_artifacts SET lease_expires_at = now() + make_interval(secs => $3)
		 WHERE id = $1 AND lease_owner = $2 AND status IN ('running', 'tone_map_running', 'audio_v2_running')`,
		id, owner, leaseSecs,
	)
	if err != nil {
		return false, fmt.Errorf("heartbeating artifact: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkReady transitions a job to ready, records its size/path, and clears the
// lease. The write is fenced on (lease_owner, status='running') so a worker that
// lost its lease — e.g. a slow encode whose lease expired and was reclaimed by
// another node — cannot flip a row it no longer owns. Returns false when the
// fence rejected the write (the lease was lost); the caller must then NOT flip
// linked downloads, leaving that to the current owner.
func (r *ArtifactRepository) MarkReady(ctx context.Context, id, owner, outputPath string, originNodeID int, originNodeURL, originNodeGroup, originArtifactID string, fileSize int64) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE download_artifacts
		 SET status = CASE
		                  WHEN audio_recipe_version <> '' THEN 'audio_v2_ready'
		                  WHEN tone_map_mode <> '' THEN 'tone_map_ready'
		                  ELSE 'ready'
		              END,
		     output_path = $2, origin_node_id = $3, origin_node_url = $4,
		     origin_node_group = $5, origin_artifact_id = $6, file_size = $7, error_message = '',
		     completed_at = now(), last_used_at = now(),
		     lease_owner = NULL, lease_expires_at = NULL, next_retry_at = NULL
		 WHERE id = $1 AND lease_owner = $8 AND status IN ('running', 'tone_map_running', 'audio_v2_running')`,
		id, outputPath, originNodeID, originNodeURL, originNodeGroup, originArtifactID, fileSize, owner,
	)
	if err != nil {
		return false, fmt.Errorf("marking artifact ready: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkFailedOrRetry records a failed attempt. attempts was already incremented
// at claim time, so attempts >= max_attempts means terminal (status=failed);
// otherwise the row returns to queued behind a backoff gate. The write is fenced
// on (lease_owner, status='running'): terminal reports whether the job went
// terminal, and applied is false when the fence rejected the write (lease lost),
// in which case the caller must not fail linked downloads.
func (r *ArtifactRepository) MarkFailedOrRetry(ctx context.Context, id, owner, errMsg string, backoff time.Duration) (terminal bool, applied bool, err error) {
	backoffSecs := int(backoff.Seconds())
	if backoffSecs <= 0 {
		backoffSecs = 30
	}
	err = r.pool.QueryRow(ctx,
		`UPDATE download_artifacts
		 SET status = CASE WHEN attempts >= max_attempts THEN 'failed'
		                   WHEN status = 'audio_v2_running' THEN 'audio_v2_queued'
		                   WHEN status = 'tone_map_running' THEN 'tone_map_queued'
		                   ELSE 'queued' END,
		     error_message = $2,
		     next_retry_at = CASE WHEN attempts >= max_attempts THEN NULL ELSE now() + make_interval(secs => $3) END,
		     completed_at = CASE WHEN attempts >= max_attempts THEN now() ELSE NULL END,
		     lease_owner = NULL, lease_expires_at = NULL
		 WHERE id = $1 AND lease_owner = $4 AND status IN ('running', 'tone_map_running', 'audio_v2_running')
		 RETURNING status = 'failed'`,
		id, errMsg, backoffSecs, owner,
	).Scan(&terminal)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil // lease lost; the current owner resolves the job
	}
	if err != nil {
		return false, false, fmt.Errorf("marking artifact failed/retry: %w", err)
	}
	return terminal, true, nil
}

// reclaimedArtifact reports a row recovered by the startup sweep.
type reclaimedArtifact struct {
	ID       string
	Terminal bool // true when the row exhausted attempts and is now failed
}

// ReclaimExpiredLeases resets running rows whose lease has expired: back to
// queued, or to failed when attempts are exhausted. This is the startup sweep
// that guarantees no crash can strand a job in `running` forever.
func (r *ArtifactRepository) ReclaimExpiredLeases(ctx context.Context) ([]reclaimedArtifact, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE download_artifacts
		 SET status = CASE WHEN attempts >= max_attempts THEN 'failed'
		                   WHEN status = 'audio_v2_running' THEN 'audio_v2_queued'
		                   WHEN status = 'tone_map_running' THEN 'tone_map_queued'
		                   ELSE 'queued' END,
		     lease_owner = NULL, lease_expires_at = NULL,
		     error_message = CASE WHEN attempts >= max_attempts THEN 'exceeded max attempts after lease expiry' ELSE error_message END,
		     completed_at = CASE WHEN attempts >= max_attempts THEN now() ELSE completed_at END
		 WHERE status IN ('running', 'tone_map_running', 'audio_v2_running') AND lease_expires_at < now()
		 RETURNING id, status = 'failed'`,
	)
	if err != nil {
		return nil, fmt.Errorf("reclaiming expired leases: %w", err)
	}
	defer rows.Close()
	var out []reclaimedArtifact
	for rows.Next() {
		var rc reclaimedArtifact
		if err := rows.Scan(&rc.ID, &rc.Terminal); err != nil {
			return nil, fmt.Errorf("scanning reclaimed artifact: %w", err)
		}
		out = append(out, rc)
	}
	return out, rows.Err()
}

// Requeue forces a ready/failed artifact back to queued (e.g. when its
// output_path is missing on disk). The deterministic output_path is preserved.
// Returns ErrNotFound when the row no longer exists (e.g. a concurrent sweep
// deleted it) so callers never keep using a dead artifact id.
func (r *ArtifactRepository) Requeue(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE download_artifacts
		 SET status = CASE
		                  WHEN audio_recipe_version <> '' THEN 'audio_v2_queued'
		                  WHEN tone_map_mode <> '' THEN 'tone_map_queued'
		                  ELSE 'queued'
		              END,
		     attempts = 0, error_message = '', next_retry_at = NULL,
		     lease_owner = NULL, lease_expires_at = NULL, completed_at = NULL,
		     origin_node_id = 0, origin_node_url = '', origin_node_group = '', origin_artifact_id = ''
		 WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("requeuing artifact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RequeueRemote atomically transfers a ready remote locator into the cleanup
// queue and clears it from the artifact row. The locator can therefore never
// be deleted while the row still advertises it if either database write fails.
// applied is false when the row changed concurrently or no longer exists.
func (r *ArtifactRepository) RequeueRemote(ctx context.Context, artifact *Artifact) (linked []*Download, applied bool, err error) {
	return r.requeueRemote(ctx, artifact, false)
}

// RequeueRemoteExactLocator additionally fences on the origin URL. Proxy miss
// reports use it so an older signed URL cannot requeue a row after an
// administrator has moved the same node/artifact locator to a new endpoint.
func (r *ArtifactRepository) RequeueRemoteExactLocator(ctx context.Context, artifact *Artifact) (linked []*Download, applied bool, err error) {
	return r.requeueRemote(ctx, artifact, true)
}

func (r *ArtifactRepository) requeueRemote(ctx context.Context, artifact *Artifact, fenceURL bool) (linked []*Download, applied bool, err error) {
	if artifact == nil {
		return nil, false, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning remote artifact requeue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	query := `UPDATE download_artifacts
		 SET status = CASE
		                  WHEN audio_recipe_version <> '' THEN 'audio_v2_queued'
		                  WHEN tone_map_mode <> '' THEN 'tone_map_queued'
		                  ELSE 'queued'
		              END,
		     attempts = 0, error_message = '', next_retry_at = NULL,
		     lease_owner = NULL, lease_expires_at = NULL, completed_at = NULL,
		     origin_node_id = 0, origin_node_url = '', origin_node_group = '', origin_artifact_id = ''
		 WHERE id = $1 AND status IN ('ready', 'tone_map_ready', 'audio_v2_ready') AND origin_node_id = $2 AND origin_artifact_id = $3`
	args := []any{artifact.ID, artifact.OriginNodeID, artifact.OriginArtifactID}
	if fenceURL {
		query += ` AND origin_node_url = $4`
		args = append(args, artifact.OriginNodeURL)
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("requeuing remote artifact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, false, nil
	}
	rows, err := tx.Query(ctx,
		`UPDATE downloads
		 SET status = 'preparing', bytes_sent = 0, completed_at = NULL,
		     error_message = '', updated_at = now()
		 WHERE artifact_id = $1 AND status NOT IN ('cancelled', 'failed', 'revoked')
		 RETURNING `+downloadColumns,
		artifact.ID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("resetting linked downloads for remote artifact requeue: %w", err)
	}
	linked, err = scanDownloads(rows)
	rows.Close()
	if err != nil {
		return nil, false, fmt.Errorf("scanning reset downloads for remote artifact requeue: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO download_artifact_orphans (download_artifact_id, origin_node_id, origin_node_url, origin_artifact_id)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (origin_node_id, origin_artifact_id) DO UPDATE
		 SET download_artifact_id = EXCLUDED.download_artifact_id,
		     origin_node_url = EXCLUDED.origin_node_url, next_retry_at = NULL`,
		artifact.ID, artifact.OriginNodeID, artifact.OriginNodeURL, artifact.OriginArtifactID,
	); err != nil {
		return nil, false, fmt.Errorf("enqueueing remote artifact cleanup during requeue: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing remote artifact requeue: %w", err)
	}
	return linked, true, nil
}

// TouchLastUsed bumps last_used_at for LRU accounting (called on serve).
func (r *ArtifactRepository) TouchLastUsed(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE download_artifacts SET last_used_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("touching artifact: %w", err)
	}
	return nil
}

// RefreshRemoteLocator persists an enabled node's current URL/group while
// fencing against a concurrent requeue or replacement locator.
func (r *ArtifactRepository) RefreshRemoteLocator(ctx context.Context, artifact *Artifact) (bool, error) {
	if artifact == nil {
		return false, nil
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE download_artifacts
		 SET origin_node_url = $2, origin_node_group = $3
		 WHERE id = $1 AND status IN ('ready', 'tone_map_ready', 'audio_v2_ready')
		   AND origin_node_id = $4 AND origin_artifact_id = $5`,
		artifact.ID, artifact.OriginNodeURL, artifact.OriginNodeGroup,
		artifact.OriginNodeID, artifact.OriginArtifactID,
	)
	if err != nil {
		return false, fmt.Errorf("refreshing remote artifact locator: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListReady returns ready artifacts ordered by least-recently-used first.
func (r *ArtifactRepository) ListReady(ctx context.Context) ([]*Artifact, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+artifactColumns+` FROM download_artifacts WHERE status IN ('ready', 'tone_map_ready', 'audio_v2_ready') ORDER BY last_used_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing ready artifacts: %w", err)
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

func scanArtifacts(rows pgx.Rows) ([]*Artifact, error) {
	var out []*Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning artifact row: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TotalReadyBytes returns the sum of ready artifact sizes (LRU budget input).
func (r *ArtifactRepository) TotalReadyBytes(ctx context.Context) (int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(file_size), 0) FROM download_artifacts WHERE status IN ('ready', 'tone_map_ready', 'audio_v2_ready')`).Scan(&total); err != nil {
		return 0, fmt.Errorf("summing ready artifacts: %w", err)
	}
	return total, nil
}

// HasActiveLink reports whether any valid download row — managed or ephemeral
// (device-less web) — still references the artifact. Completed rows are
// retained because they remain re-downloadable handles; evicting an artifact a
// live row references would 404 a download the API advertises as servable.
func (r *ArtifactRepository) HasActiveLink(ctx context.Context, artifactID string) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM downloads
		 WHERE artifact_id = $1
		   AND status NOT IN ('cancelled','failed','revoked'))`,
		artifactID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking artifact links: %w", err)
	}
	return exists, nil
}

// ListFailedBefore returns terminally-failed artifacts cold since cutoff
// (last_used_at). Their linked downloads were already flipped to 'failed' by
// reconciliation, so the rows serve nothing and only block re-attempts.
func (r *ArtifactRepository) ListFailedBefore(ctx context.Context, cutoff time.Time) ([]*Artifact, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+artifactColumns+` FROM download_artifacts
		 WHERE status = 'failed' AND last_used_at < $1`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("listing failed artifacts: %w", err)
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

// ListUnlinkedReadyBefore returns ready artifacts referenced by NO download
// row at all (every linking row deleted) and unused since cutoff. These are
// pure orphans: nothing can ever serve them again.
func (r *ArtifactRepository) ListUnlinkedReadyBefore(ctx context.Context, cutoff time.Time) ([]*Artifact, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+artifactColumns+` FROM download_artifacts a
		 WHERE a.status IN ('ready', 'tone_map_ready', 'audio_v2_ready') AND a.last_used_at < $1
		   AND NOT EXISTS (SELECT 1 FROM downloads d WHERE d.artifact_id = a.id)`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("listing unlinked artifacts: %w", err)
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

// DeleteArtifact removes an artifact row.
func (r *ArtifactRepository) DeleteArtifact(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting artifact: %w", err)
	}
	return nil
}

// EnqueueRemoteOrphan durably records an abandoned attempt before its owning
// artifact lease or locator is discarded.
func (r *ArtifactRepository) EnqueueRemoteOrphan(ctx context.Context, downloadArtifactID string, nodeID int, nodeURL, artifactID string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO download_artifact_orphans (download_artifact_id, origin_node_id, origin_node_url, origin_artifact_id, next_retry_at)
		 VALUES ($1, $2, $3, $4, now() + interval '30 seconds')
		 ON CONFLICT (origin_node_id, origin_artifact_id) DO UPDATE
		 SET download_artifact_id = EXCLUDED.download_artifact_id,
		     origin_node_url = EXCLUDED.origin_node_url,
		     next_retry_at = EXCLUDED.next_retry_at`,
		downloadArtifactID, nodeID, nodeURL, artifactID,
	)
	if err != nil {
		return fmt.Errorf("enqueueing remote artifact cleanup: %w", err)
	}
	return nil
}

// ListRemoteOrphansDue interleaves due candidates across origins so a large
// unreachable-node backlog cannot starve cleanup for healthy origins while a
// single healthy origin can still use the full batch.
func (r *ArtifactRepository) ListRemoteOrphansDue(ctx context.Context, limit int) ([]RemoteArtifactOrphan, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`WITH due AS (
		    SELECT id, download_artifact_id, origin_node_id, origin_node_url, origin_artifact_id, attempts,
		           row_number() OVER (
		               PARTITION BY origin_node_id, origin_node_url
		               ORDER BY attempts, created_at, id
		           ) AS origin_rank,
		           created_at
		    FROM download_artifact_orphans
		    WHERE next_retry_at IS NULL OR next_retry_at <= now()
		 )
		 SELECT id, download_artifact_id, origin_node_id, origin_node_url, origin_artifact_id, attempts
		 FROM due
		 ORDER BY origin_rank, attempts, created_at, id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing remote artifact cleanup queue: %w", err)
	}
	defer rows.Close()
	out := make([]RemoteArtifactOrphan, 0, limit)
	for rows.Next() {
		var orphan RemoteArtifactOrphan
		if err := rows.Scan(&orphan.ID, &orphan.DownloadArtifactID, &orphan.OriginNodeID, &orphan.OriginNodeURL, &orphan.OriginArtifactID, &orphan.Attempts); err != nil {
			return nil, fmt.Errorf("scanning remote artifact cleanup row: %w", err)
		}
		out = append(out, orphan)
	}
	return out, rows.Err()
}

// PrepareRemoteOrphanCleanup serializes cleanup against requeue and verifies
// that a locator from an indeterminate MarkReady result did not actually win
// the artifact row. owned locators have their stale cleanup row removed;
// abandoned locators are retry-leased before the caller performs remote I/O.
func (r *ArtifactRepository) PrepareRemoteOrphanCleanup(ctx context.Context, orphan RemoteArtifactOrphan) (owned, claimed bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, false, fmt.Errorf("beginning remote artifact cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var originNodeID int
	var originArtifactID string
	err = tx.QueryRow(ctx,
		`SELECT origin_node_id, origin_artifact_id
		 FROM download_artifacts WHERE id = $1 FOR UPDATE`,
		orphan.DownloadArtifactID,
	).Scan(&originNodeID, &originArtifactID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, false, fmt.Errorf("checking remote artifact ownership: %w", err)
	}
	var present int64
	if err := tx.QueryRow(ctx, `SELECT id FROM download_artifact_orphans WHERE id = $1 FOR UPDATE`, orphan.ID).Scan(&present); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("locking remote artifact cleanup row: %w", err)
	}
	owned = originNodeID == orphan.OriginNodeID && originArtifactID == orphan.OriginArtifactID
	if owned {
		if _, err := tx.Exec(ctx, `DELETE FROM download_artifact_orphans WHERE id = $1`, orphan.ID); err != nil {
			return false, false, fmt.Errorf("clearing owned remote artifact cleanup row: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE download_artifact_orphans
			 SET attempts = attempts + 1, next_retry_at = now() + interval '2 minutes'
			 WHERE id = $1`, orphan.ID); err != nil {
			return false, false, fmt.Errorf("claiming remote artifact cleanup row: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, fmt.Errorf("committing remote artifact cleanup claim: %w", err)
	}
	return owned, true, nil
}

func (r *ArtifactRepository) DeleteRemoteOrphan(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM download_artifact_orphans WHERE id = $1`, id); err != nil {
		return fmt.Errorf("deleting remote artifact cleanup row: %w", err)
	}
	return nil
}

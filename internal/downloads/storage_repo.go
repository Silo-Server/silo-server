package downloads

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
)

// ArtifactExpired marks a prepared file whose bytes were deleted because no
// download was waiting on it. The row keeps its frozen recipe.
const ArtifactExpired = "expired"

// storageMaintenanceLockClassID / storageMaintenanceLockObjID name the advisory
// lock that lets one replica at a time enforce storage budgets. Expiry is safe
// to race (every write is fenced), but two replicas each freeing the same
// overage would delete twice as much as needed.
const (
	storageMaintenanceLockClassID = 0x51_10_d1
	storageMaintenanceLockObjID   = 1
)

// ListExpirable returns ready artifacts stored at one location (nodeID 0 is the
// API server) that no in-flight download needs and nothing used since cutoff,
// least recently used first.
func (r *ArtifactRepository) ListExpirable(ctx context.Context, nodeID int, cutoff time.Time, limit int) ([]*Artifact, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+qualifiedColumns(artifactColumns, "a")+` FROM download_artifacts a
		 WHERE a.status IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')
		   AND a.origin_node_id = $1 AND a.last_used_at < $2
		   AND NOT EXISTS (SELECT 1 FROM downloads d WHERE d.artifact_id = a.id AND `+inFlightLinkPredicate+`)
		 ORDER BY a.last_used_at, a.id
		 LIMIT $3`, nodeID, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("listing expirable artifacts: %w", err)
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

// ExpireReady expires one ready artifact when no in-flight download needs it
// and nothing used it within grace. A node-local file's locator moves to the
// remote cleanup queue in the same transaction. It returns false when the row
// changed first: a create linked it, recovery requeued it, or another replica
// expired it.
func (r *ArtifactRepository) ExpireReady(ctx context.Context, a *Artifact, grace time.Duration) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning artifact expiry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE download_artifacts a SET `+expireArtifactAssignment+`
		 WHERE a.id = $1 AND `+unusedReadyArtifactPredicate+`
		   AND a.origin_node_id = $3 AND a.origin_artifact_id = $4`,
		a.ID, grace.Seconds(), a.OriginNodeID, a.OriginArtifactID,
	)
	if err != nil {
		return false, fmt.Errorf("expiring artifact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if a.OriginArtifactID != "" {
		if err := enqueueRemoteArtifactCleanup(ctx, tx, a); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing artifact expiry: %w", err)
	}
	return true, nil
}

// RequeueExpiredWithWaiters requeues expired artifacts that a preparing
// download is linked to. It repairs a link made by a replica that predates
// expiry (it sees 'expired' as neither ready nor failed and links anyway), so
// no download waits on a file nobody will prepare.
func (r *ArtifactRepository) RequeueExpiredWithWaiters(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE download_artifacts a
		 SET status = CASE
		                  WHEN track_recipe_version <> '' THEN 'tracks_v1_queued'
		                  WHEN audio_recipe_version <> '' THEN 'audio_v2_queued'
		                  WHEN tone_map_mode <> '' THEN 'tone_map_queued'
		                  ELSE 'queued'
		              END,
		     attempts = 0, error_message = '', next_retry_at = NULL, completed_at = NULL
		 WHERE a.status = 'expired'
		   AND EXISTS (SELECT 1 FROM downloads d WHERE d.artifact_id = a.id AND d.status = 'preparing')
		 RETURNING a.id`)
	if err != nil {
		return nil, fmt.Errorf("requeuing expired artifacts with waiting downloads: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteUnreferencedExpired deletes expired rows no download row references:
// nothing will ever read their recipe again.
func (r *ArtifactRepository) DeleteUnreferencedExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM download_artifacts a
		 WHERE a.status = 'expired' AND NOT EXISTS (SELECT 1 FROM downloads d WHERE d.artifact_id = a.id)`)
	if err != nil {
		return 0, fmt.Errorf("deleting unreferenced expired artifacts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReadyBytesByLocation sums ready artifact sizes per origin node id; 0 is the
// API server.
func (r *ArtifactRepository) ReadyBytesByLocation(ctx context.Context) (map[int]int64, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT origin_node_id, COALESCE(SUM(file_size), 0)::bigint FROM download_artifacts
		 WHERE status IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')
		 GROUP BY origin_node_id`)
	if err != nil {
		return nil, fmt.Errorf("summing ready artifacts by location: %w", err)
	}
	defer rows.Close()
	out := make(map[int]int64)
	for rows.Next() {
		var nodeID int
		var bytes int64
		if err := rows.Scan(&nodeID, &bytes); err != nil {
			return nil, err
		}
		out[nodeID] = bytes
	}
	return out, rows.Err()
}

// WithMaintenanceLock runs fn while this process holds the storage maintenance
// advisory lock. It returns false without running fn when another replica
// holds it. The lock is held on a dedicated session for the length of fn.
func (r *ArtifactRepository) WithMaintenanceLock(ctx context.Context, fn func(context.Context) error) (bool, error) {
	// The lock lives on its own session, not a pooled connection: fn runs its
	// queries through the pool, which a held pooled connection could exhaust
	// (database.max_connections may be 1). Closing the session releases the
	// lock, whatever state a canceled caller left it in.
	session, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig)
	if err != nil {
		return false, fmt.Errorf("opening storage maintenance lock session: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = session.Close(closeCtx)
	}()
	var locked bool
	if err := session.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, storageMaintenanceLockClassID, storageMaintenanceLockObjID).Scan(&locked); err != nil {
		return false, fmt.Errorf("taking storage maintenance lock: %w", err)
	}
	if !locked {
		return false, nil
	}
	return true, fn(ctx)
}

// storageSampleUntracked is the reconciliation half of a storage sample.
type storageSampleUntracked struct {
	Files int
	Bytes int64
	At    time.Time
	// Failed counts files a removal could not delete; they stay untracked.
	Failed int
}

// serverSampleRetention is how long a replica's measurement of the server's
// directory outlives the replica: replicas that restart under a new name stop
// updating their rows.
const serverSampleRetention = 7 * 24 * time.Hour

// PruneServerStorageSamples deletes server measurements no replica has
// updated since cutoff.
func (r *ArtifactRepository) PruneServerStorageSamples(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM download_storage_samples WHERE node_id IS NULL AND updated_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning server storage samples: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpsertStorageSample records the latest measurement of one location's
// directory. nodeID 0 is the API server, reported per replica under reporter.
// A nil untracked keeps the last reconciliation result.
func (r *ArtifactRepository) UpsertStorageSample(ctx context.Context, nodeID int, reporter string, usage downloadstorage.Usage, untracked *storageSampleUntracked) error {
	encoded, err := json.Marshal(usage)
	if err != nil {
		return fmt.Errorf("encoding storage sample: %w", err)
	}
	var node *int
	if nodeID > 0 {
		node = &nodeID
	}
	var files *int
	var bytes *int64
	var at *time.Time
	if untracked != nil {
		files, bytes, at = &untracked.Files, &untracked.Bytes, &untracked.At
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO download_storage_samples (node_id, reporter, usage, untracked_files, untracked_bytes, reconciled_at, updated_at)
		 VALUES ($1, $2, $3, COALESCE($4::integer, 0), COALESCE($5::bigint, 0), $6, now())
		 ON CONFLICT (COALESCE(node_id, 0), reporter) DO UPDATE
		 SET usage = EXCLUDED.usage, updated_at = now(),
		     untracked_files = CASE WHEN $6::timestamptz IS NULL THEN download_storage_samples.untracked_files ELSE EXCLUDED.untracked_files END,
		     untracked_bytes = CASE WHEN $6::timestamptz IS NULL THEN download_storage_samples.untracked_bytes ELSE EXCLUDED.untracked_bytes END,
		     reconciled_at = COALESCE($6, download_storage_samples.reconciled_at)`,
		node, reporter, encoded, files, bytes, at,
	)
	if err != nil {
		return fmt.Errorf("recording storage sample: %w", err)
	}
	return nil
}

// trackedArtifactFiles returns the file names a location's directory is
// expected to hold: finished files of ready rows, plus, on the API server, the
// files of jobs that are queued or running there (a running encode writes
// <name>.part). A file outside this set is untracked once it is old enough.
// For a node the set covers every node's files, because nodes may share one
// volume, and a file another node owns is not this node's to delete.
func (r *ArtifactRepository) trackedArtifactFiles(ctx context.Context, nodeID int) (map[string]bool, error) {
	var rows pgx.Rows
	var err error
	if nodeID > 0 {
		rows, err = r.pool.Query(ctx,
			`SELECT origin_artifact_id || '.mp4' FROM download_artifacts
			 WHERE origin_node_id > 0 AND status IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')
			 UNION ALL
			 SELECT origin_artifact_id || '.mp4' FROM download_artifact_orphans WHERE origin_node_id > 0`)
	} else {
		rows, err = r.pool.Query(ctx,
			`SELECT output_path FROM download_artifacts
			 WHERE origin_node_id = 0 AND output_path <> '' AND status NOT IN ('expired', 'failed')`)
	}
	if err != nil {
		return nil, fmt.Errorf("listing tracked artifact files: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[filepath.Base(name)] = true
	}
	return out, rows.Err()
}

package downloads

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/idgen"
)

// Storage locations. A prepared file lives on the API server or on one
// transcode node; device events record copies on people's devices.
const (
	LocationServer = "server"
	LocationDevice = "device"
	// serverLocationName is how the API server's location is labeled.
	serverLocationName = "Server"
)

// NodeLocationKey is the location key of a transcode node.
func NodeLocationKey(nodeID int) string { return "node:" + strconv.Itoa(nodeID) }

// ParseLocationKey returns the node id of a prepared-file location: 0 for the
// API server. ok is false for anything else.
func ParseLocationKey(key string) (nodeID int, ok bool) {
	if key == LocationServer {
		return 0, true
	}
	rest, found := strings.CutPrefix(key, "node:")
	if !found {
		return 0, false
	}
	// Node ids are int4 in the database.
	id, err := strconv.ParseInt(rest, 10, 32)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != rest {
		return 0, false
	}
	return int(id), true
}

func locationKeyForNode(nodeID int) string {
	if nodeID > 0 {
		return NodeLocationKey(nodeID)
	}
	return LocationServer
}

// Reasons recorded in download_storage_events.
const (
	StorageReasonCacheExpired  = "cache_expired"
	StorageReasonBudget        = "budget"
	StorageReasonDiskCeiling   = "disk_ceiling"
	StorageReasonAdminDelete   = "admin_delete"
	StorageReasonUntracked     = "untracked"
	StorageReasonMissing       = "missing"
	StorageReasonRevoked       = "revoked"
	StorageReasonDeviceRemoved = "device_removed"
)

// storageEventRetention is how long clean-up history is kept.
const storageEventRetention = 90 * 24 * time.Hour

func newStorageBatchID() string {
	id, err := idgen.NextID()
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return id
}

// storageEventTitleSQL renders a catalog title for a media file row (alias f)
// joined to its episode (ep) and item (mi): "Series · S2 E5" for an episode.
const storageEventTitleSQL = `COALESCE(mi.title, '') ||
	CASE WHEN ep.content_id IS NOT NULL THEN ' · S' || ep.season_number || ' E' || ep.episode_number ELSE '' END`

// RecordArtifactEvent records what happened to one prepared file, with the
// catalog title it had. History is best effort: it never blocks clean-up, so
// callers log a failure and move on. location is where the bytes were, which
// the caller knows even after the row's locator was cleared.
func (r *ArtifactRepository) RecordArtifactEvent(ctx context.Context, batchID, reason, location, artifactID string, bytes int64, actor *int, detail string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO download_storage_events
			(batch_id, reason, location_key, location_name, artifact_id, content_id, episode_id, title, bytes, actor_user_id, detail)
		 SELECT $1, $2, $3,
		        COALESCE((SELECT n.name FROM stream_nodes n WHERE 'node:' || n.id = $3), ''),
		        $4, COALESCE(f.content_id, ''), COALESCE(f.episode_id, ''), `+storageEventTitleSQL+`,
		        $5, $6, $7
		 FROM (SELECT $4::text AS id) want
		 LEFT JOIN download_artifacts a ON a.id = want.id
		 LEFT JOIN media_files f ON f.id = a.media_file_id
		 LEFT JOIN episodes ep ON ep.content_id = f.episode_id
		 LEFT JOIN media_items mi ON mi.content_id = COALESCE(ep.series_id, f.content_id)`,
		batchID, reason, location, artifactID, max(bytes, 0), actor, detail,
	)
	if err != nil {
		return fmt.Errorf("recording storage event: %w", err)
	}
	return nil
}

// RecordFileEvent records a file deleted without an artifact row, such as an
// untracked file.
func (r *ArtifactRepository) RecordFileEvent(ctx context.Context, batchID, reason, location string, bytes int64, actor *int, detail string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO download_storage_events (batch_id, reason, location_key, location_name, bytes, actor_user_id, detail)
		 VALUES ($1, $2, $3, COALESCE((SELECT n.name FROM stream_nodes n WHERE 'node:' || n.id = $3), ''), $4, $5, $6)`,
		batchID, reason, location, max(bytes, 0), actor, detail,
	)
	if err != nil {
		return fmt.Errorf("recording storage event: %w", err)
	}
	return nil
}

// LastRemovalByLocation returns, per location key, when files were last
// removed there before the given time, for any reason.
func (r *ArtifactRepository) LastRemovalByLocation(ctx context.Context, before time.Time) (map[string]time.Time, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT location_key, max(occurred_at) FROM download_storage_events WHERE occurred_at < $1 GROUP BY location_key`, before)
	if err != nil {
		return nil, fmt.Errorf("reading the last storage removals: %w", err)
	}
	defer rows.Close()
	out := make(map[string]time.Time)
	for rows.Next() {
		var key string
		var at time.Time
		if err := rows.Scan(&key, &at); err != nil {
			return nil, fmt.Errorf("reading the last storage removals: %w", err)
		}
		out[key] = at
	}
	return out, rows.Err()
}

// PruneStorageEvents deletes history older than cutoff.
func (r *ArtifactRepository) PruneStorageEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM download_storage_events WHERE occurred_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning storage events: %w", err)
	}
	return tag.RowsAffected(), nil
}

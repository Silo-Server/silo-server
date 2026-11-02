package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// maxRevokeReasonLength bounds, in characters, the note an administrator
// attaches to a revoke.
const maxRevokeReasonLength = 500

// RevokeRequest selects device copies for an administrator to revoke: the
// listed managed rows, or with no ids every row on one device.
type RevokeRequest struct {
	IDs       []string
	UserID    int
	ProfileID string
	DeviceID  string
	// PauseMonitors pauses the device's series monitors too. It applies only
	// to a whole-device revoke; revoking single episodes already keeps a
	// monitor from adding them back.
	PauseMonitors bool
	Reason        string
	Actor         int
}

// RevokeResult reports what a revoke changed.
type RevokeResult struct {
	Revoked        []*Download
	Bytes          int64 // finished copies only
	PausedMonitors int
}

// ErrInvalidRevoke reports a revoke request that selects nothing.
var ErrInvalidRevoke = errors.New("revoke needs download ids or one device")

// RevokeDeviceDownloads revokes device copies for an administrator. The app
// deletes its local copy at its next sync and confirms with DELETE; until then
// the row stays revoked, and the server refuses its file. A revoked episode of
// a monitored series is excluded from the monitor so the next sync does not
// register it again. A preparation only the revoked rows were waiting on is
// canceled.
func (s *Service) RevokeDeviceDownloads(ctx context.Context, req RevokeRequest) (*RevokeResult, error) {
	req.IDs = uniqueIDs(req.IDs)
	if len(req.IDs) == 0 && (req.UserID <= 0 || req.ProfileID == "" || req.DeviceID == "") {
		return nil, ErrInvalidRevoke
	}
	// Characters, not bytes: cutting a byte slice could split a character,
	// and Postgres refuses invalid UTF-8.
	req.Reason = strings.TrimSpace(req.Reason)
	if reason := []rune(req.Reason); len(reason) > maxRevokeReasonLength {
		req.Reason = string(reason[:maxRevokeReasonLength])
	}
	result, err := s.repo.RevokeManaged(ctx, req, newStorageBatchID())
	if err != nil {
		return nil, err
	}
	if s.artifacts != nil {
		s.artifacts.cancelAbandonedPreparations(ctx, result.Revoked)
		for _, d := range result.Revoked {
			s.artifacts.publish(ctx, d)
		}
		s.artifacts.notifyStorageChanged(ctx)
	}
	if len(result.Revoked) > 0 {
		slog.InfoContext(ctx, "device downloads revoked", "component", "downloads", "count", len(result.Revoked), "actor", req.Actor)
	}
	return result, nil
}

// cancelAbandonedPreparations cancels unfinished preparations that no other
// download needs any more, so revoked rows do not leave a node encoding for
// nobody. A preparation any live download refers to is left alone, including
// a finished one: canceling deletes the row, and that download's manifest and
// any later prepare-again read its recipe.
func (m *ArtifactManager) cancelAbandonedPreparations(ctx context.Context, revoked []*Download) {
	var ids []string
	for _, d := range revoked {
		if d.ArtifactID != "" {
			ids = append(ids, d.ArtifactID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if _, err := m.cancelPreparations(ctx, ids, m.repo.CancelAbandonedPreparations); err != nil {
		slog.WarnContext(ctx, "canceling preparations of revoked downloads failed", "component", "downloads", "error", err)
	}
}

// revokeManagedSQL revokes the selected managed rows, excludes revoked
// episodes from the device's series monitors, and records one history event
// per row, in one statement. $1 ids (empty for a whole device), $2 user,
// $3 profile, $4 device, $5 actor, $6 reason, $7 batch.
const revokeManagedSQL = `WITH revoked AS (
	UPDATE downloads d
	SET status = 'revoked', revoked_at = now(), revoked_by = $5, revoked_reason = $6, updated_at = now()
	WHERE d.device_id IS NOT NULL AND d.status <> 'revoked'
	  AND ((cardinality($1::text[]) > 0 AND d.id = ANY($1::text[]))
	       OR (cardinality($1::text[]) = 0 AND d.user_id = $2 AND d.profile_id = $3 AND d.device_id = $4))
	RETURNING ` + "%s" + `
), monitor AS (
	SELECT s.id, r.episode_id FROM revoked r
	JOIN download_subscriptions s
	  ON s.user_id = r.user_id AND s.profile_id = r.profile_id AND s.device_id = r.device_id AND s.series_id = r.content_id
	WHERE r.episode_id IS NOT NULL
	FOR KEY SHARE OF s
), excluded AS (
	INSERT INTO download_subscription_exclusions (subscription_id, episode_id)
	SELECT id, episode_id FROM monitor
	ON CONFLICT DO NOTHING
), events AS (
	INSERT INTO download_storage_events
		(batch_id, reason, location_key, location_name, download_id, user_id, content_id, episode_id, title, bytes, actor_user_id, detail)
	SELECT $7, 'revoked', 'device', COALESCE(u.device_name, ''), r.id, r.user_id, r.content_id, COALESCE(r.episode_id, ''),
	       ` + storageEventTitleSQL + `, CASE WHEN r.completed_at IS NULL THEN 0 ELSE GREATEST(r.file_size, 0) END, $5, $6
	FROM revoked r
	LEFT JOIN user_devices u ON u.user_id = r.user_id AND u.profile_id = r.profile_id AND u.device_id = r.device_id
	LEFT JOIN episodes ep ON ep.content_id = r.episode_id
	LEFT JOIN media_items mi ON mi.content_id = r.content_id
)
SELECT ` + "%s" + ` FROM revoked`

// RevokeManaged revokes managed rows and, for a whole-device revoke with
// PauseMonitors, pauses the device's monitors, in one transaction.
func (r *Repository) RevokeManaged(ctx context.Context, req RevokeRequest, batchID string) (*RevokeResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning revoke: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids := req.IDs
	if ids == nil {
		ids = []string{}
	}
	var actor *int
	if req.Actor > 0 {
		actor = &req.Actor
	}
	query := fmt.Sprintf(revokeManagedSQL, qualifiedColumns(downloadColumns, "d"), downloadColumns)
	rows, err := tx.Query(ctx, query, ids, req.UserID, req.ProfileID, req.DeviceID, actor, req.Reason, batchID)
	if err != nil {
		return nil, fmt.Errorf("revoking downloads: %w", err)
	}
	revoked, err := scanDownloads(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := &RevokeResult{Revoked: revoked}
	for _, d := range revoked {
		// Only a finished copy takes space on the device. Preparing again
		// clears completed_at, so a row being fetched again does not count.
		if d.CompletedAt != nil {
			result.Bytes += max(d.FileSize, 0)
		}
	}
	if req.PauseMonitors && len(req.IDs) == 0 {
		tag, err := tx.Exec(ctx,
			`UPDATE download_subscriptions SET active = false, updated_at = now()
			 WHERE user_id = $1 AND profile_id = $2 AND device_id = $3 AND active`,
			req.UserID, req.ProfileID, req.DeviceID)
		if err != nil {
			return nil, fmt.Errorf("pausing device monitors: %w", err)
		}
		result.PausedMonitors = int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing revoke: %w", err)
	}
	return result, nil
}

// PruneRevokedOlderThan deletes revoked rows whose device never confirmed the
// local copy was deleted.
func (r *Repository) PruneRevokedOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM downloads WHERE status = 'revoked' AND revoked_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning revoked downloads: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TouchDeviceSeen records that a device synced its download registry. It
// writes at most once an hour per device: the stale-device view needs days,
// not seconds.
func (r *Repository) TouchDeviceSeen(ctx context.Context, userID int, profileID, deviceID string) error {
	if profileID == "" || deviceID == "" {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE user_devices SET last_seen_at = now()
		 WHERE user_id = $1 AND profile_id = $2 AND device_id = $3
		   AND (last_seen_at IS NULL OR last_seen_at < now() - interval '1 hour')`,
		userID, profileID, deviceID)
	if err != nil {
		return fmt.Errorf("recording device sync: %w", err)
	}
	return nil
}

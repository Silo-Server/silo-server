package watchtogether

import (
	"context"
	"time"
)

// Host expiry must run even when this process has never seen the room. A node
// can lose every socket without publishing a disconnect or retaining a timer.
func (s *Service) runHostExpirySweeper() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.janitorStop:
			return
		case <-ticker.C:
			s.sweepExpiredHosts(context.Background())
		}
	}
}

func (s *Service) sweepExpiredHosts(ctx context.Context) {
	repo, shared := s.repo.(*Repository)
	if !shared {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	roomIDs, err := repo.listExpiredHostRoomIDs(ctx, s.now().Add(-s.hostDisconnectTTL), 100)
	if err != nil {
		return
	}
	// Reconciliation rechecks presence under the room lock, so a host that
	// reconnects after this query cannot be closed by its stale result.
	s.reconcileRooms(ctx, roomIDs)
}

func (r *Repository) listExpiredHostRoomIDs(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM watch_together_rooms
		CROSS JOIN LATERAL (
			SELECT runtime->'members'->(host_user_id::text || ':' || host_profile_id) AS host
		) AS presence
		WHERE phase IN ('lobby', 'playing')
		  AND NULLIF(CASE WHEN (host->>'connected')::boolean
			THEN host->>'lease_until' ELSE host->>'disconnected_at' END,
			'0001-01-01T00:00:00Z')::timestamptz <= $1
		ORDER BY id
		LIMIT $2`, cutoff.UTC(), limit)
	if err != nil {
		return nil, err
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

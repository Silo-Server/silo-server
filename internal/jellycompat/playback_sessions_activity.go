package jellycompat

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// NativeSessionActivity bridges durable compatibility pings to the process
// owning each native playback session. Matching the deterministic pseudo-user
// checks both account and household profile, not just a session identifier.
func (d *DurableCompatPlaybackStore) NativeSessionActivity(ctx context.Context, sessions []playback.Session) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	if len(sessions) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(sessions))
	owners := make(map[string]string, len(sessions))
	for _, session := range sessions {
		if session.IsJellyfinCompat {
			ids = append(ids, session.ID)
			owners[session.ID] = PseudoUserID(session.UserID, session.ProfileID).String()
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT data->>'UpstreamSessionID', user_id, (data->>'UpdatedAt')::timestamptz FROM jellycompat_playback_sessions WHERE data->>'UpstreamSessionID'=ANY($1) AND expires_at>$2 AND COALESCE((data->>'Terminal')::boolean,false)=false`, ids, d.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, owner string
		var stamp time.Time
		if err := rows.Scan(&id, &owner, &stamp); err != nil {
			return nil, err
		}
		if owners[id] == owner && stamp.After(result[id]) {
			result[id] = stamp
		}
	}
	return result, rows.Err()
}

// ClaimNativeSessionExpiry locks matching rows and retires only those still
// inactive. The row lock serializes with ping UPDATE: a winning ping prevents
// retirement; a winning retirement makes the later ping return NotFound.
func (d *DurableCompatPlaybackStore) ClaimNativeSessionExpiry(ctx context.Context, candidates []playback.SessionExpiryCandidate) (map[string]bool, error) {
	ids := make([]string, len(candidates))
	owners := make([]string, len(candidates))
	cutoffs := make([]time.Time, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
		owners[i] = PseudoUserID(c.UserID, c.ProfileID).String()
		cutoffs[i] = c.InactiveBefore
	}
	rows, err := d.pool.Query(ctx, `WITH candidates AS (
 SELECT * FROM unnest($1::text[],$2::text[],$3::timestamptz[]) AS c(native_id,owner,cutoff)
 ), locked AS MATERIALIZED (
 SELECT p.id,p.data,c.native_id,c.cutoff FROM jellycompat_playback_sessions p
 JOIN candidates c ON p.data->>'UpstreamSessionID'=c.native_id AND p.user_id=c.owner
 WHERE p.expires_at>$4 AND COALESCE((p.data->>'Terminal')::boolean,false)=false
 ORDER BY p.id FOR UPDATE OF p
 ), eligible AS (
 SELECT c.native_id FROM candidates c LEFT JOIN locked l ON l.native_id=c.native_id
 GROUP BY c.native_id HAVING bool_and(l.id IS NULL OR (l.data->>'UpdatedAt')::timestamptz<=l.cutoff)
 ), retired AS (
 UPDATE jellycompat_playback_sessions p SET data=jsonb_set(p.data,'{Terminal}','true'::jsonb)
 FROM locked l JOIN eligible e ON e.native_id=l.native_id WHERE p.id=l.id RETURNING p.id,p.compat_token,p.data->>'UpstreamSessionID' AS native_id
 ) SELECT e.native_id,COALESCE(r.id,''),COALESCE(r.compat_token,'') FROM eligible e LEFT JOIN retired r ON r.native_id=e.native_id`, ids, owners, cutoffs, d.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	var retired [][2]string
	for rows.Next() {
		var id, compatID, token string
		if err := rows.Scan(&id, &compatID, &token); err != nil {
			return nil, err
		}
		result[id] = true
		if compatID != "" {
			retired = append(retired, [2]string{compatID, token})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, row := range retired {
		d.invalidateValidation(row[0], row[1])
		d.bumpCacheGenerations(row[0], row[1])
	}
	return result, nil
}

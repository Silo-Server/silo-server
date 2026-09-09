package jellycompat

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// ListActiveForToken exposes only the caller's unexpired playback mappings.
func (s *PlaybackSessionStore) ListActiveForToken(ctx context.Context, token string) ([]PlaybackSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessions := make([]PlaybackSession, 0)
	for _, session := range s.sessions {
		if session.CompatToken == token && !session.Terminal && session.ExpiresAt.After(s.now()) {
			sessions = append(sessions, session)
		}
	}
	slices.SortFunc(sessions, func(a, b PlaybackSession) int { return strings.Compare(a.ID, b.ID) })
	return sessions, nil
}

// ListActiveForToken reads durable rows on every poll, so a request routed to a
// different API process sees sessions created or stopped on the owning process.
func (d *DurableCompatPlaybackStore) ListActiveForToken(ctx context.Context, token string) ([]PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ListActiveForToken(ctx, token)
	}
	rows, err := d.pool.Query(ctx, `SELECT data FROM jellycompat_playback_sessions WHERE compat_token=$1 AND expires_at>$2 ORDER BY id`, token, d.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]PlaybackSession, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var session PlaybackSession
		if err := json.Unmarshal(raw, &session); err != nil {
			return nil, err
		}
		if !session.Terminal && session.CompatToken == token && session.ExpiresAt.After(d.now()) {
			sessions = append(sessions, session)
		}
	}
	return sessions, rows.Err()
}

// TouchActiveForToken persists a keepalive before acknowledging it; a database
// outage must not turn this endpoint into a successful process-local no-op.
// It refreshes activity, not the absolute grant lifetime shared with stream
// tokens. Playback must negotiate a new grant once that lifetime ends.
func (s *PlaybackSessionStore) TouchActiveForToken(ctx context.Context, id, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Update(id, func(session *PlaybackSession) error {
		if session.CompatToken != token {
			return ErrSessionNotFound
		}
		return nil
	})
}

func (d *DurableCompatPlaybackStore) TouchActiveForToken(ctx context.Context, id, token string) error {
	if d.pool == nil {
		return d.mem.TouchActiveForToken(ctx, id, token)
	}
	now := d.now()
	stamp, err := json.Marshal(now)
	if err != nil {
		return err
	}
	tag, err := d.pool.Exec(ctx, `UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,'{UpdatedAt}',to_jsonb(GREATEST((data->>'UpdatedAt')::timestamptz,($3::jsonb #>> '{}')::timestamptz))) WHERE id=$1 AND compat_token=$2 AND expires_at>$4 AND COALESCE((data->>'Terminal')::boolean,false)=false`, id, token, stamp, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	// The cache may be empty on a different API node. Updating an existing entry
	// preserves its fields; a later Get rehydrates absent entries from Postgres.
	_ = d.mem.TouchActiveForToken(ctx, id, token)
	return nil
}

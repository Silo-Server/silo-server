package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	_ CompatPlaybackStore = (*PlaybackSessionStore)(nil)
	_ CompatPlaybackStore = (*DurableCompatPlaybackStore)(nil)
)

// DurableCompatPlaybackStore is a CompatPlaybackStore that persists compat
// playback sessions to Postgres so the PlaySessionId -> upstream-session mapping
// (and the negotiated media sources) survives a server restart. It wraps an
// in-memory PlaybackSessionStore as a write-through cache so the hot segment path
// (Get on every segment request) stays in-process; a cache miss falls back to a
// DB read and repopulates the cache. A Redis swap would reimplement this same
// interface, leaving every caller unchanged.
type DurableCompatPlaybackStore struct {
	mem  *PlaybackSessionStore
	pool *pgxpool.Pool
	ttl  time.Duration
	now  func() time.Time
}

// NewDurableCompatPlaybackStore returns a Postgres-backed compat store. pool must
// be non-nil (callers fall back to the in-memory store when there is no DB).
func NewDurableCompatPlaybackStore(pool *pgxpool.Pool, ttl time.Duration, now func() time.Time) *DurableCompatPlaybackStore {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &DurableCompatPlaybackStore{
		mem:  NewPlaybackSessionStore(ttl, now),
		pool: pool,
		ttl:  ttl,
		now:  now,
	}
}

// Put writes through to both the cache and Postgres.
func (d *DurableCompatPlaybackStore) Put(session PlaybackSession) {
	d.mem.Put(session)
	// Re-read the cached copy so the persisted row carries the timestamps the
	// in-memory store just normalized (CreatedAt/UpdatedAt/ExpiresAt).
	if cached, ok := d.mem.Get(session.ID); ok {
		d.upsert(*cached)
	}
}

// Get returns the cached session, falling back to Postgres on a miss (e.g. after
// a restart) and repopulating the cache.
func (d *DurableCompatPlaybackStore) Get(id string) (*PlaybackSession, bool) {
	if s, ok := d.mem.Get(id); ok {
		return s, true
	}
	s, ok := d.load(id)
	if !ok {
		return nil, false
	}
	d.mem.Put(*s)
	return d.mem.Get(id)
}

// Delete removes the session from both the cache and Postgres.
func (d *DurableCompatPlaybackStore) Delete(id string) {
	d.mem.Delete(id)
	if d.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := d.pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id); err != nil {
		slog.Warn("delete compat playback session failed", "error", err, "play_session_id", id)
	}
}

// Update modifies the session in place under the cache's lock (in-process
// atomicity), then persists the result. The session is loaded from Postgres into
// the cache first when absent so an update after a restart still applies.
func (d *DurableCompatPlaybackStore) Update(id string, fn func(*PlaybackSession) error) error {
	if _, ok := d.mem.Get(id); !ok {
		if s, ok := d.load(id); ok {
			d.mem.Put(*s)
		}
	}
	if err := d.mem.Update(id, fn); err != nil {
		return err
	}
	if s, ok := d.mem.Get(id); ok {
		d.upsert(*s)
	}
	return nil
}

// FindByRoute resolves a route id, checking the cache first and falling back to
// loading the matching compat-token rows from Postgres into the cache.
func (d *DurableCompatPlaybackStore) FindByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool) {
	if s, src, ok := d.mem.FindByRoute(compatToken, routeID); ok {
		return s, src, ok
	}
	d.loadByCompatToken(compatToken)
	return d.mem.FindByRoute(compatToken, routeID)
}

// DeleteExpired physically removes lapsed rows. Reads already filter on
// expires_at, so this only bounds table growth; run it on the janitor cadence.
func (d *DurableCompatPlaybackStore) DeleteExpired(ctx context.Context) (int64, error) {
	if d.pool == nil {
		return 0, nil
	}
	tag, err := d.pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (d *DurableCompatPlaybackStore) upsert(session PlaybackSession) {
	if d.pool == nil {
		return
	}
	data, err := json.Marshal(session)
	if err != nil {
		slog.Warn("marshal compat playback session failed", "error", err, "play_session_id", session.ID)
		return
	}
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = d.now().Add(d.ttl)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const q = `
		INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			compat_token = EXCLUDED.compat_token,
			user_id      = EXCLUDED.user_id,
			data         = EXCLUDED.data,
			expires_at   = EXCLUDED.expires_at`
	if _, err := d.pool.Exec(ctx, q, session.ID, session.CompatToken, session.UserID, data, expiresAt); err != nil {
		slog.Warn("persist compat playback session failed", "error", err, "play_session_id", session.ID)
	}
}

func (d *DurableCompatPlaybackStore) load(id string) (*PlaybackSession, bool) {
	if d.pool == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var raw []byte
	err := d.pool.QueryRow(ctx,
		`SELECT data FROM jellycompat_playback_sessions WHERE id = $1 AND expires_at > now()`, id,
	).Scan(&raw)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("load compat playback session failed", "error", err, "play_session_id", id)
		}
		return nil, false
	}
	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		slog.Warn("unmarshal compat playback session failed", "error", err, "play_session_id", id)
		return nil, false
	}
	return &session, true
}

// loadByCompatToken loads all live rows for a compat token (or every live row
// when the token is empty, matching the in-memory FindByRoute scan) into the
// cache so a subsequent cache scan can resolve the route.
func (d *DurableCompatPlaybackStore) loadByCompatToken(compatToken string) {
	if d.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var (
		rows pgx.Rows
		err  error
	)
	if compatToken == "" {
		rows, err = d.pool.Query(ctx, `SELECT data FROM jellycompat_playback_sessions WHERE expires_at > now()`)
	} else {
		rows, err = d.pool.Query(ctx, `SELECT data FROM jellycompat_playback_sessions WHERE compat_token = $1 AND expires_at > now()`, compatToken)
	}
	if err != nil {
		slog.Warn("load compat playback sessions by token failed", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			slog.Warn("scan compat playback session failed", "error", err)
			return
		}
		var session PlaybackSession
		if err := json.Unmarshal(raw, &session); err != nil {
			continue
		}
		d.mem.Put(session)
	}
}

package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Put writes through to both the cache and Postgres. putNormalized returns the
// stored copy carrying the timestamps the cache just assigned (CreatedAt /
// UpdatedAt / ExpiresAt), so the persisted row matches the cache without a second
// Get (extra lock + full struct copy) to recover them.
//
// The DB upsert is intentionally kept synchronous: restart resilience depends on
// the just-negotiated session being durable before the client's next request
// (which may arrive after a restart), and the codebase relies on read-your-write
// across a fresh instance. The upsert itself is still best-effort (a DB failure
// is logged, not propagated); the cache holds the authoritative in-process state.
func (d *DurableCompatPlaybackStore) Put(session PlaybackSession) {
	stored := d.mem.putNormalized(session)
	if d.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.upsert(ctx, stored)
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
//
// The DB persist is atomic against concurrent writers: it re-reads the
// authoritative row under SELECT ... FOR UPDATE inside a transaction, re-applies
// fn to that row, and upserts the result before committing. This stops two
// processes (or a cache-evicted writer racing another) from clobbering each
// other's JSON with a blind whole-document upsert — e.g. a transcode-recipe
// write being lost to a concurrent upstream-session write. The DB step is still
// best-effort for availability: a DB failure is logged and the in-memory mutation
// stands, but a successful DB read-modify-write is never silently lost.
func (d *DurableCompatPlaybackStore) Update(id string, fn func(*PlaybackSession) error) error {
	if _, ok := d.mem.Get(id); !ok {
		if s, ok := d.load(id); ok {
			d.mem.Put(*s)
		}
	}
	if err := d.mem.Update(id, fn); err != nil {
		return err
	}
	committed, err := d.updateDB(id, fn)
	if committed != nil {
		// Refresh the cache from the DB-authoritative committed row so the cache
		// reflects any concurrent writer's fields that fn merged on top of.
		d.mem.Put(*committed)
	}
	if err != nil {
		// The in-memory mutation stands (live state is correct), but the durable
		// row was NOT updated: surface the failure so durability-sensitive callers
		// (recipe/upstream-session writes that promise restart resilience) can roll
		// back or fail rather than reporting a session as restart-safe when a later
		// restart would reload a stale row or 404. A genuinely absent/expired row
		// and a nil pool are not durability failures and return nil (best-effort,
		// unchanged) — only real DB round-trip errors propagate.
		return fmt.Errorf("durably persist compat playback session %s: %w", id, err)
	}
	return nil
}

// updateDB applies fn to the DB-authoritative row inside a transaction using
// SELECT ... FOR UPDATE, then upserts and commits. It returns the committed
// session when the round-trip succeeded. A nil pool or a genuinely
// missing/expired row returns (nil, nil): there is no durable row to update, so
// this is treated as best-effort (the caller keeps the in-memory mutation), not a
// durability failure. A real DB round-trip error returns (nil, err) so the caller
// can learn the session was not durably persisted. fn is expected to be
// idempotent — it is applied to the cache copy and again to the DB-authoritative
// copy.
func (d *DurableCompatPlaybackStore) updateDB(id string, fn func(*PlaybackSession) error) (*PlaybackSession, error) {
	if d.pool == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		slog.Warn("begin compat playback session update tx failed", "error", err, "play_session_id", id)
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var raw []byte
	err = tx.QueryRow(ctx,
		`SELECT data FROM jellycompat_playback_sessions WHERE id = $1 AND expires_at > $2 FOR UPDATE`,
		id, d.now(),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No durable row to update (never persisted or already expired): not a
			// durability failure — best-effort, the in-memory mutation stands.
			return nil, nil
		}
		slog.Warn("load compat playback session for update failed", "error", err, "play_session_id", id)
		return nil, err
	}

	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		slog.Warn("unmarshal compat playback session for update failed", "error", err, "play_session_id", id)
		return nil, err
	}
	if err := fn(&session); err != nil {
		// The mutation itself rejected the authoritative row; the cache mutation
		// (already applied) stands. This is a fn/data condition, not an
		// infrastructure failure, so it is not surfaced as a durability error.
		slog.Warn("apply compat playback session update failed", "error", err, "play_session_id", id)
		return nil, nil
	}
	session.UpdatedAt = d.now()

	data, err := json.Marshal(session)
	if err != nil {
		slog.Warn("marshal compat playback session for update failed", "error", err, "play_session_id", id)
		return nil, err
	}
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = d.now().Add(d.ttl)
	}
	if _, err := tx.Exec(ctx, upsertSessionQuery, session.ID, session.CompatToken, session.UserID, data, expiresAt); err != nil {
		slog.Warn("persist compat playback session update failed", "error", err, "play_session_id", id)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("commit compat playback session update failed", "error", err, "play_session_id", id)
		return nil, err
	}
	return &session, nil
}

// FindByRoute resolves a route id, checking the cache first and falling back to
// loading the matching compat-token rows from Postgres into the cache.
func (d *DurableCompatPlaybackStore) FindByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool) {
	if s, src, ok := d.mem.FindByRoute(compatToken, routeID); ok {
		return s, src, ok
	}
	// An empty compat token cannot be pushed into a bounded, indexed DB query, so
	// the only DB fallback would be loading every live row and scanning it on this
	// request goroutine — an O(table) cliff. The sole caller
	// (resolvePlaybackRoute) always passes a non-empty compat token, so the
	// empty-token DB fallback is never load-bearing for route resolution; return
	// the in-memory result rather than incurring a full-table scan.
	if compatToken == "" {
		return nil, nil, false
	}
	d.loadByCompatToken(compatToken)
	return d.mem.FindByRoute(compatToken, routeID)
}

// FindByClientPlaySessionID resolves the client-generated PlaySessionId alias,
// checking the cache first and falling back to loading the compat token's live
// rows from Postgres into the cache (same bounded fallback as FindByRoute; the
// alias uniqueness check runs against the repopulated cache).
func (d *DurableCompatPlaybackStore) FindByClientPlaySessionID(compatToken, clientPlaySessionID string) (*PlaybackSession, bool) {
	if s, ok := d.mem.FindByClientPlaySessionID(compatToken, clientPlaySessionID); ok {
		return s, ok
	}
	if compatToken == "" {
		return nil, false
	}
	d.loadByCompatToken(compatToken)
	return d.mem.FindByClientPlaySessionID(compatToken, clientPlaySessionID)
}

// DeleteExpired physically removes lapsed rows. Reads already filter on
// expires_at, so this only bounds table growth; run it on the janitor cadence.
func (d *DurableCompatPlaybackStore) DeleteExpired(ctx context.Context) (int64, error) {
	if d.pool == nil {
		return 0, nil
	}
	tag, err := d.pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE expires_at < $1`, d.now())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const upsertSessionQuery = `
	INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (id) DO UPDATE SET
		compat_token = EXCLUDED.compat_token,
		user_id      = EXCLUDED.user_id,
		data         = EXCLUDED.data,
		expires_at   = EXCLUDED.expires_at`

// upsert persists a session on the given context. It is best-effort: a DB
// failure is logged and swallowed (the cache holds the authoritative in-process
// state). Callers own the context and its timeout.
func (d *DurableCompatPlaybackStore) upsert(ctx context.Context, session PlaybackSession) {
	if d.pool == nil {
		return
	}
	data, err := json.Marshal(session)
	if err != nil {
		slog.WarnContext(ctx, "marshal compat playback session failed", "component", "jellycompat", "error", err, "play_session_id", session.ID)
		return
	}
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = d.now().Add(d.ttl)
	}
	if _, err := d.pool.Exec(ctx, upsertSessionQuery, session.ID, session.CompatToken, session.UserID, data, expiresAt); err != nil {
		slog.WarnContext(ctx, "persist compat playback session failed", "component", "jellycompat", "error", err, "play_session_id", session.ID)
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
		`SELECT data FROM jellycompat_playback_sessions WHERE id = $1 AND expires_at > $2`, id, d.now(),
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

// loadByCompatToken loads the live rows for a (non-empty) compat token into the
// cache so a subsequent cache scan can resolve the route. The query is bounded by
// the indexed compat_token predicate; FindByRoute never calls it with an empty
// token (that would be an unbounded full-table load).
func (d *DurableCompatPlaybackStore) loadByCompatToken(compatToken string) {
	if d.pool == nil || compatToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := d.pool.Query(ctx,
		`SELECT data FROM jellycompat_playback_sessions WHERE compat_token = $1 AND expires_at > $2`,
		compatToken, d.now())
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

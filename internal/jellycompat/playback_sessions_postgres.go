package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// compatCacheRevalidationInterval bounds how long one process may retain an
// active routing view after another process terminalizes the durable row. It
// also keeps two-second HLS segment requests on the in-memory hot path instead
// of adding a Postgres round trip to every segment.
const compatCacheRevalidationInterval = 5 * time.Second

const compatValidationCacheLimit = 16_384

type pendingCompatPlaybackUpdate struct {
	sequence    uint64
	compatToken string
	apply       func(*PlaybackSession) error
}

type compatCacheGeneration struct {
	epoch uint64
	value uint64
}

var (
	_ CompatPlaybackStore = (*PlaybackSessionStore)(nil)
	_ CompatPlaybackStore = (*DurableCompatPlaybackStore)(nil)
)

// DurableCompatPlaybackStore is a CompatPlaybackStore that persists compat
// playback sessions to Postgres so the PlaySessionId -> upstream-session mapping
// (and the negotiated media sources) survives a server restart. The in-memory
// store remains a write-through working set. Active routing periodically
// revalidates durable rows so cross-process terminal transitions invalidate a
// stale cache within a bounded window without putting Postgres on every segment
// request's hot path.
type DurableCompatPlaybackStore struct {
	mem  *PlaybackSessionStore
	pool *pgxpool.Pool
	ttl  time.Duration
	now  func() time.Time

	validationMu          sync.Mutex
	validatedIDs          map[string]time.Time
	validatedTokens       map[string]time.Time
	unpersistedIDs        map[string]struct{}
	pendingUpdateMu       sync.Mutex
	pendingUpdateSequence uint64
	pendingUpdates        map[string][]pendingCompatPlaybackUpdate
	pendingCursorMu       sync.Mutex
	pendingCursor         string

	// cacheMutationMu lets unrelated writes proceed concurrently while durable
	// read snapshots take an exclusive lock only for their in-memory apply step.
	// Per-ID/token generations discard snapshots that overlapped a mutation in
	// the same routing scope.
	cacheMutationMu  sync.RWMutex
	sessionMutations [256]sync.Mutex
	generationMu     sync.Mutex
	generationEpoch  uint64
	idGenerations    map[string]uint64
	tokenGenerations map[string]uint64
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
		mem:              NewPlaybackSessionStore(ttl, now),
		pool:             pool,
		ttl:              ttl,
		now:              now,
		validatedIDs:     make(map[string]time.Time),
		validatedTokens:  make(map[string]time.Time),
		unpersistedIDs:   make(map[string]struct{}),
		pendingUpdates:   make(map[string][]pendingCompatPlaybackUpdate),
		idGenerations:    make(map[string]uint64),
		tokenGenerations: make(map[string]uint64),
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
	if d.pool == nil {
		d.mem.Put(session)
		return
	}
	unlockSession := d.lockSessionMutation(session.ID)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	stored := d.mem.putNormalized(session)
	defer d.finishCacheMutation(stored.ID, stored.CompatToken)
	d.markIDValidated(stored.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.upsert(ctx, stored); err != nil {
		d.markUnpersisted(stored.ID)
	} else {
		d.clearUnpersisted(stored.ID)
	}
}

// PutNegotiated persists a new PlaybackInfo session while removing older,
// unstarted negotiations for the same compat token, client device, and item.
// The advisory transaction lock makes the replacement atomic across Silo
// processes; the in-memory mutation is likewise atomic for the local process.
func (d *DurableCompatPlaybackStore) PutNegotiated(session PlaybackSession) {
	if d.pool == nil {
		d.mem.PutNegotiated(session)
		return
	}

	scope := "negotiated\x00" + session.CompatToken + "\x00" + session.ClientDeviceID + "\x00" + session.RouteItemID
	unlockSession := d.lockSessionMutation(scope)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	stored, locallyRemoved := d.mem.putNegotiatedNormalized(session)
	defer d.finishCacheMutation(stored.ID, stored.CompatToken)
	d.markIDValidated(stored.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	durablyRemoved, err := d.replaceUnstartedNegotiation(ctx, stored)
	if err != nil {
		d.markUnpersisted(stored.ID)
		slog.WarnContext(ctx, "persist negotiated compat playback session failed",
			"component", "jellycompat",
			"error", err,
			"play_session_id", stored.ID,
		)
	} else {
		d.clearUnpersisted(stored.ID)
	}

	removed := make(map[string]struct{}, len(locallyRemoved)+len(durablyRemoved))
	for _, id := range locallyRemoved {
		removed[id] = struct{}{}
	}
	for _, id := range durablyRemoved {
		removed[id] = struct{}{}
	}
	for id := range removed {
		d.invalidateValidation(id, "")
		d.clearUnpersisted(id)
		d.clearPendingUpdates(id)
		d.bumpCacheGenerations(id, "")
	}
	d.invalidateValidation("", stored.CompatToken)
}

func (d *DurableCompatPlaybackStore) replaceUnstartedNegotiation(
	ctx context.Context,
	session PlaybackSession,
) ([]string, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var removed []string
	if session.CompatToken != "" && session.ClientDeviceID != "" && session.RouteItemID != "" {
		scope := session.CompatToken + "\x00" + session.ClientDeviceID + "\x00" + session.RouteItemID
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, scope); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `
			DELETE FROM jellycompat_playback_sessions
			WHERE id <> $1
				AND compat_token = $2
				AND data->>'ClientDeviceID' = $3
				AND data->>'RouteItemID' = $4
				AND COALESCE(data->>'UpstreamSessionID', '') = ''
				AND COALESCE((data->>'Terminal')::boolean, false) = false
				AND expires_at > $5
			RETURNING id
		`, session.ID, session.CompatToken, session.ClientDeviceID, session.RouteItemID, d.now())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			removed = append(removed, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	data, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = d.now().Add(d.ttl)
	}
	if _, err := tx.Exec(
		ctx, upsertSessionQuery,
		session.ID, session.CompatToken, session.UserID, data, expiresAt,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return removed, nil
}

// Get periodically revalidates the durable row before returning an active
// session. Query failures preserve a still-valid cache entry: a temporary DB
// outage must not interrupt an already-playing stream.
func (d *DurableCompatPlaybackStore) Get(id string) (*PlaybackSession, bool) {
	if d.pool == nil {
		return d.mem.Get(id)
	}
	cached, cachedOK := d.mem.Get(id)
	if cachedOK && cached.UpstreamSessionID != "" && !d.shouldRevalidateID(id) {
		return cached, cachedOK
	}
	if !cachedOK {
		// Cold callers must load (or share a future single-flight); another
		// request reserving the throttle window is not proof the row is absent.
		_ = d.shouldRevalidateID(id)
	}
	generation := d.idGenerationSnapshot(id)
	s, ok, err := d.load(id)
	if err != nil {
		return cached, cachedOK
	}
	d.cacheMutationMu.Lock()
	defer d.cacheMutationMu.Unlock()
	if generation != d.idGenerationSnapshot(id) {
		d.invalidateValidation(id, "")
		return d.mem.Get(id)
	}
	if ok && s.Terminal {
		d.clearPendingUpdates(id)
	} else if ok && d.hasPendingUpdates(id) {
		if current, currentOK := d.mem.Get(id); currentOK {
			return current, true
		}
	}
	if ok && !s.Terminal {
		if local, localOK := d.mem.GetFinalizable(id, s.CompatToken); localOK && local.Terminal {
			return nil, false
		}
	}
	if !ok {
		compatToken := d.mem.compatTokenForID(id)
		if current, currentOK := d.mem.GetFinalizable(id, compatToken); currentOK && d.repairUnpersisted(current) {
			return d.mem.Get(id)
		}
		d.clearUnpersisted(id)
		d.clearPendingUpdates(id)
		d.mem.Delete(id)
		if cachedOK || compatToken != "" {
			d.bumpCacheGenerations(id, "")
		}
		return nil, false
	}
	d.clearUnpersisted(id)
	d.mem.Put(*s)
	d.bumpCacheGenerations(id, s.CompatToken)
	return d.mem.Get(id)
}

// Delete removes the session from both the cache and Postgres.
func (d *DurableCompatPlaybackStore) Delete(id string) {
	if d.pool == nil {
		d.mem.Delete(id)
		return
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	compatToken := d.mem.compatTokenForID(id)
	defer d.finishCacheMutation(id, compatToken)
	d.invalidateValidation(id, compatToken)
	d.clearUnpersisted(id)
	d.clearPendingUpdates(id)
	d.mem.Delete(id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := d.pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id); err != nil {
		slog.Warn("delete compat playback session failed", "error", err, "play_session_id", id)
	}
}

// HideFromRouting immediately makes the local routing cache terminal. It is
// intentionally independent of Postgres so a staging outage cannot let a
// stopped client reconstruct a fresh upstream session.
func (d *DurableCompatPlaybackStore) HideFromRouting(id, compatToken string) error {
	if d.pool == nil {
		return d.mem.HideFromRouting(id, compatToken)
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	defer d.finishCacheMutation(id, compatToken)
	if err := d.mem.HideFromRouting(id, compatToken); err != nil {
		return err
	}
	d.clearPendingUpdates(id)
	if d.isUnpersisted(id) {
		cached, ok := d.mem.GetFinalizable(id, compatToken)
		if !ok {
			return ErrSessionNotFound
		}
		inserted, err := d.insertIfAbsent(cached)
		if err != nil {
			return err
		}
		d.clearUnpersisted(id)
		if inserted {
			return nil
		}
		// A row appeared after the failed Put. Fall through and terminalize that
		// durable row rather than leaving another process able to route it.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tag, err := d.pool.Exec(ctx, `
		UPDATE jellycompat_playback_sessions
		SET data = jsonb_set(data, '{Terminal}', 'true'::jsonb, true)
		WHERE id = $1 AND compat_token = $2 AND expires_at > $3
	`, id, compatToken, d.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// StageTerminal hides a session and persists the provider event under a row
// lock. This merge keeps an authoritative Stopped event from being overwritten
// by a later ActiveEncodings fallback on another server process.
func (d *DurableCompatPlaybackStore) StageTerminal(
	id string,
	compatToken string,
	event watchsync.ScrobbleEvent,
	authoritative bool,
) (*PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.StageTerminal(id, compatToken, event, authoritative)
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	defer d.finishCacheMutation(id, compatToken)
	if err := d.mem.HideFromRouting(id, compatToken); err != nil {
		return nil, err
	}
	d.clearPendingUpdates(id)
	if d.isUnpersisted(id) {
		cached, ok := d.mem.GetFinalizable(id, compatToken)
		if !ok {
			return nil, ErrSessionNotFound
		}
		candidate := *cached
		eventCopy := event
		candidate.Terminal = true
		candidate.TerminalAuthoritative = authoritative
		candidate.TerminalScrobbleEvent = &eventCopy
		candidate.TerminalEventVersion++
		if _, err := d.insertIfAbsent(&candidate); err != nil {
			return nil, err
		}
		d.clearUnpersisted(id)
	}

	committed, err := d.stageTerminalDB(id, compatToken, event, authoritative)
	if err != nil {
		slog.Warn("stage durable compat terminal event failed", "error", err, "play_session_id", id)
		return nil, err
	}
	if committed == nil {
		d.mem.Delete(id)
		return nil, ErrSessionNotFound
	}
	d.mem.Delete(id)
	d.mem.Put(*committed)
	d.markIDValidated(committed.ID)
	return committed, nil
}

func (d *DurableCompatPlaybackStore) stageTerminalDB(
	id string,
	compatToken string,
	event watchsync.ScrobbleEvent,
	authoritative bool,
) (*PlaybackSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var raw []byte
	err = tx.QueryRow(ctx, `
		SELECT data
		FROM jellycompat_playback_sessions
		WHERE id = $1 AND compat_token = $2 AND expires_at > $3
		FOR UPDATE
	`, id, compatToken, d.now()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	if !session.TerminalAuthoritative || authoritative {
		eventCopy := event
		session.TerminalScrobbleEvent = &eventCopy
		session.TerminalAuthoritative = authoritative
		session.TerminalEventVersion++
	}
	session.Terminal = true
	session.UpdatedAt = d.now()
	data, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE jellycompat_playback_sessions SET data = $2 WHERE id = $1`, id, data); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &session, nil
}

// ClaimTerminal leases one pending terminal event across processes without
// deleting its retry state. Expired leases can be reclaimed after a crash.
func (d *DurableCompatPlaybackStore) ClaimTerminal(id, compatToken string, claimUntil time.Time) (*PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ClaimTerminal(id, compatToken, claimUntil)
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	defer d.finishCacheMutation(id, compatToken)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	leaseDuration := claimUntil.Sub(d.now())
	if leaseDuration <= 0 {
		leaseDuration = compatTerminalClaimLease
	}
	var dbNow time.Time
	if err := d.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return nil, err
	}
	durableClaimUntil := dbNow.Add(leaseDuration).UTC().Truncate(time.Microsecond)
	claimText := durableClaimUntil.Format(time.RFC3339Nano)
	var raw []byte
	err := d.pool.QueryRow(ctx, `
		UPDATE jellycompat_playback_sessions
		SET data = jsonb_set(
			jsonb_set(data, '{TerminalClaimUntil}', to_jsonb($4::text), true),
			'{TerminalClaimVersion}',
			to_jsonb(COALESCE((data->>'TerminalEventVersion')::bigint, 0)),
			true
		)
		WHERE id = $1
			AND compat_token = $2
			AND expires_at > $5
			AND COALESCE((data->>'Terminal')::boolean, false) = true
			AND COALESCE(data->'TerminalScrobbleEvent' <> 'null'::jsonb, false)
			AND COALESCE(
				NULLIF(data->>'TerminalClaimUntil', '0001-01-01T00:00:00Z')::timestamptz,
				'-infinity'::timestamptz
			) <= $3
			AND (
				COALESCE((data->>'TerminalFallbackSent')::boolean, false) = false
				OR COALESCE((data->>'TerminalAuthoritative')::boolean, false) = true
			)
		RETURNING data
	`, id, compatToken, dbNow, claimText, d.now()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		existsErr := d.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM jellycompat_playback_sessions
				WHERE id = $1 AND compat_token = $2 AND expires_at > $3
			)
		`, id, compatToken, d.now()).Scan(&exists)
		if existsErr != nil {
			return nil, existsErr
		}
		if exists {
			return nil, ErrTerminalClaimUnavailable
		}
		return nil, ErrSessionNotFound
	}
	if err != nil {
		slog.Warn("claim compat terminal event failed", "error", err, "play_session_id", id)
		return nil, err
	}

	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		slog.Warn("unmarshal claimed compat terminal event failed", "error", err, "play_session_id", id)
		return nil, err
	}
	d.mem.Delete(id)
	d.mem.Put(session)
	d.markIDValidated(session.ID)
	return &session, nil
}

// ReleaseTerminalClaim releases an exact lease and optionally records that the
// provisional ActiveEncodings fallback reached the durable watch-sync queue.
func (d *DurableCompatPlaybackStore) ReleaseTerminalClaim(
	id string,
	compatToken string,
	claimUntil time.Time,
	claimVersion int64,
	fallbackSent bool,
) {
	if d.pool == nil {
		d.mem.ReleaseTerminalClaim(id, compatToken, claimUntil, claimVersion, fallbackSent)
		return
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	defer d.finishCacheMutation(id, compatToken)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var raw []byte
	err := d.pool.QueryRow(ctx, `
		UPDATE jellycompat_playback_sessions
		SET data = CASE WHEN $5
			THEN jsonb_set(data - 'TerminalClaimUntil' - 'TerminalClaimVersion', '{TerminalFallbackSent}', 'true'::jsonb, true)
			ELSE data - 'TerminalClaimUntil' - 'TerminalClaimVersion'
		END
		WHERE id = $1
			AND compat_token = $2
			AND (data->>'TerminalClaimUntil')::timestamptz = $3
			AND COALESCE((data->>'TerminalClaimVersion')::bigint, 0) = $4
		RETURNING data
	`, id, compatToken, claimUntil, claimVersion, fallbackSent).Scan(&raw)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("release compat terminal event claim failed", "error", err, "play_session_id", id)
		}
		d.mem.ReleaseTerminalClaim(id, compatToken, claimUntil, claimVersion, fallbackSent)
		return
	}
	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err == nil {
		d.mem.Delete(id)
		d.mem.Put(session)
		d.markIDValidated(session.ID)
	}
}

// CompleteTerminal deletes an authoritatively queued event only while the
// caller still owns its exact lease.
func (d *DurableCompatPlaybackStore) CompleteTerminal(
	id string,
	compatToken string,
	claimUntil time.Time,
	claimVersion int64,
) {
	if d.pool == nil {
		d.mem.CompleteTerminal(id, compatToken, claimUntil, claimVersion)
		return
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	defer d.finishCacheMutation(id, compatToken)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tag, err := d.pool.Exec(ctx, `
		DELETE FROM jellycompat_playback_sessions
		WHERE id = $1
			AND compat_token = $2
			AND COALESCE((data->>'TerminalAuthoritative')::boolean, false) = true
			AND (data->>'TerminalClaimUntil')::timestamptz = $3
			AND COALESCE((data->>'TerminalClaimVersion')::bigint, 0) = $4
			AND COALESCE((data->>'TerminalEventVersion')::bigint, 0) = $4
	`, id, compatToken, claimUntil, claimVersion)
	if err != nil {
		slog.Warn("complete compat terminal event failed", "error", err, "play_session_id", id)
		return
	}
	if tag.RowsAffected() > 0 {
		d.mem.Delete(id)
		d.invalidateValidation(id, compatToken)
	}
}

// GetFinalizable reads an active or terminal caller-owned session, checking the
// process cache before the durable row.
func (d *DurableCompatPlaybackStore) GetFinalizable(id, compatToken string) (*PlaybackSession, bool) {
	if d.pool == nil {
		return d.mem.GetFinalizable(id, compatToken)
	}
	cached, cachedOK := d.mem.GetFinalizable(id, compatToken)
	if cachedOK && !d.shouldRevalidateID(id) {
		return cached, cachedOK
	}
	if !cachedOK {
		_ = d.shouldRevalidateID(id)
	}
	generation := d.idGenerationSnapshot(id)
	session, ok, err := d.load(id)
	if err != nil {
		return cached, cachedOK
	}
	d.cacheMutationMu.Lock()
	defer d.cacheMutationMu.Unlock()
	if generation != d.idGenerationSnapshot(id) {
		d.invalidateValidation(id, "")
		return d.mem.GetFinalizable(id, compatToken)
	}
	if ok && session.Terminal {
		d.clearPendingUpdates(id)
	} else if ok && d.hasPendingUpdates(id) {
		if current, currentOK := d.mem.GetFinalizable(id, compatToken); currentOK {
			return current, true
		}
	}
	if ok && !session.Terminal {
		if local, localOK := d.mem.GetFinalizable(id, compatToken); localOK && local.Terminal {
			return local, true
		}
	}
	if !ok {
		if current, currentOK := d.mem.GetFinalizable(id, compatToken); currentOK && d.repairUnpersisted(current) {
			return d.mem.GetFinalizable(id, compatToken)
		}
		d.clearUnpersisted(id)
		d.clearPendingUpdates(id)
		d.mem.Delete(id)
		if cachedOK {
			d.bumpCacheGenerations(id, "")
		}
		return nil, false
	}
	d.clearUnpersisted(id)
	d.mem.Put(*session)
	d.bumpCacheGenerations(id, session.CompatToken)
	return d.mem.GetFinalizable(id, compatToken)
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
	if d.pool == nil {
		return d.mem.Update(id, fn)
	}
	unlockSession := d.lockSessionMutation(id)
	defer unlockSession()
	d.cacheMutationMu.RLock()
	defer func() {
		d.finishCacheMutation(id, d.mem.compatTokenForID(id))
	}()
	if _, ok := d.mem.Get(id); !ok {
		if s, ok, err := d.load(id); err == nil && ok {
			d.mem.Put(*s)
			d.markIDValidated(s.ID)
		}
	}
	if err := d.mem.Update(id, fn); err != nil {
		return err
	}
	pending := d.pendingUpdatesSnapshot(id)
	committed, err := d.updateDB(id, func(session *PlaybackSession) error {
		for _, update := range pending {
			if err := update.apply(session); err != nil {
				return err
			}
		}
		return fn(session)
	})
	if committed != nil {
		// Refresh the cache from the DB-authoritative committed row so the cache
		// reflects any concurrent writer's fields that fn merged on top of.
		d.mem.Put(*committed)
		d.markIDValidated(committed.ID)
		if len(pending) > 0 {
			d.consumePendingUpdates(id, pending[len(pending)-1].sequence)
		}
	}
	if err != nil {
		d.appendPendingUpdate(id, d.mem.compatTokenForID(id), fn)
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

// FindByRoute periodically refreshes the caller's bounded durable row set before
// resolving a route. A refresh failure leaves the cached routing set intact.
func (d *DurableCompatPlaybackStore) FindByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool) {
	// An empty compat token cannot be pushed into a bounded, indexed DB query, so
	// the only DB fallback would be loading every live row and scanning it on this
	// request goroutine — an O(table) cliff. The sole caller
	// (resolvePlaybackRoute) always passes a non-empty compat token, so the
	// empty-token DB fallback is never load-bearing for route resolution; return
	// the in-memory result rather than incurring a full-table scan.
	if compatToken == "" {
		return d.mem.FindByRoute(compatToken, routeID)
	}
	cachedSession, cachedSource, cachedOK := d.mem.FindByRoute(compatToken, routeID)
	if cachedOK && !d.shouldRevalidateToken(compatToken) {
		return cachedSession, cachedSource, true
	}
	_ = d.loadByCompatToken(compatToken)
	return d.mem.FindByRoute(compatToken, routeID)
}

// FindFinalizableByRoute is the terminal-aware, uniqueness-enforcing route
// lookup used only by authenticated Stopped reports.
func (d *DurableCompatPlaybackStore) FindFinalizableByRoute(
	compatToken, routeID string,
) (*PlaybackSession, *PlaybackMediaSource, bool) {
	if compatToken == "" {
		return d.mem.FindFinalizableByRoute(compatToken, routeID)
	}
	cachedSession, cachedSource, cachedOK := d.mem.FindFinalizableByRoute(compatToken, routeID)
	if cachedOK && !d.shouldRevalidateToken(compatToken) {
		return cachedSession, cachedSource, true
	}
	_ = d.loadByCompatToken(compatToken)
	return d.mem.FindFinalizableByRoute(compatToken, routeID)
}

// FindByClientPlaySessionID resolves the client-generated PlaySessionId alias,
// checking the cache first and falling back to loading the compat token's live
// rows from Postgres into the cache (same bounded fallback as FindByRoute; the
// alias uniqueness check runs against the repopulated cache).
func (d *DurableCompatPlaybackStore) FindByClientPlaySessionID(compatToken, clientPlaySessionID string) (*PlaybackSession, bool) {
	if compatToken == "" {
		return d.mem.FindByClientPlaySessionID(compatToken, clientPlaySessionID)
	}
	cached, cachedOK := d.mem.FindByClientPlaySessionID(compatToken, clientPlaySessionID)
	if cachedOK && !d.shouldRevalidateToken(compatToken) {
		return cached, true
	}
	_ = d.loadByCompatToken(compatToken)
	return d.mem.FindByClientPlaySessionID(compatToken, clientPlaySessionID)
}

// FindFinalizableByClientPlaySessionID is the terminal-aware alias lookup used
// only by authenticated Stopped reports.
func (d *DurableCompatPlaybackStore) FindFinalizableByClientPlaySessionID(
	compatToken, clientPlaySessionID, routeItemID, mediaSourceID string,
) (*PlaybackSession, bool) {
	if compatToken == "" {
		return d.mem.FindFinalizableByClientPlaySessionID(
			compatToken, clientPlaySessionID, routeItemID, mediaSourceID,
		)
	}
	cached, cachedOK := d.mem.FindFinalizableByClientPlaySessionID(
		compatToken, clientPlaySessionID, routeItemID, mediaSourceID,
	)
	if cachedOK && !d.shouldRevalidateToken(compatToken) {
		return cached, true
	}
	_ = d.loadByCompatToken(compatToken)
	return d.mem.FindFinalizableByClientPlaySessionID(
		compatToken, clientPlaySessionID, routeItemID, mediaSourceID,
	)
}

// FindByUpstreamSessionID serves process-local lifecycle callbacks. A local
// ffmpeg crash can only belong to a session already present in this process's
// cache, so no unindexed JSON scan of the durable table is needed.
func (d *DurableCompatPlaybackStore) FindByUpstreamSessionID(upstreamSessionID string) (*PlaybackSession, bool) {
	candidate, ok := d.mem.FindByUpstreamSessionID(upstreamSessionID)
	if !ok || d.pool == nil {
		return candidate, ok
	}
	validated, ok := d.Get(candidate.ID)
	if !ok || validated.UpstreamSessionID != upstreamSessionID {
		return nil, false
	}
	return validated, true
}

// ListPendingTerminals loads durable terminal events that need first delivery
// or an authoritative retry. Successfully delivered provisional fallbacks stay
// stored for late Stopped replacement but are excluded from recovery scans.
func (d *DurableCompatPlaybackStore) ListPendingTerminals(ctx context.Context, limit int) ([]PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ListPendingTerminals(ctx, limit)
	}
	if limit <= 0 {
		limit = 100
	}
	d.pendingCursorMu.Lock()
	defer d.pendingCursorMu.Unlock()

	query := func(afterID string) ([]PlaybackSession, error) {
		rows, err := d.pool.Query(ctx, `
			SELECT data
			FROM jellycompat_playback_sessions
			WHERE expires_at > $1
				AND ($2 = '' OR id > $2)
				AND COALESCE((data->>'Terminal')::boolean, false) = true
				AND COALESCE(data->'TerminalScrobbleEvent' <> 'null'::jsonb, false)
				AND (
					COALESCE((data->>'TerminalFallbackSent')::boolean, false) = false
					OR COALESCE((data->>'TerminalAuthoritative')::boolean, false) = true
				)
			ORDER BY id ASC
			LIMIT $3
		`, d.now(), afterID, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		result := make([]PlaybackSession, 0, limit)
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return nil, err
			}
			var session PlaybackSession
			if err := json.Unmarshal(raw, &session); err != nil {
				return nil, err
			}
			result = append(result, session)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return result, nil
	}

	result, err := query(d.pendingCursor)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 && d.pendingCursor != "" {
		d.pendingCursor = ""
		result, err = query("")
		if err != nil {
			return nil, err
		}
	}
	if len(result) > 0 {
		d.pendingCursor = result[len(result)-1].ID
	} else {
		d.pendingCursor = ""
	}
	return result, nil
}

// DeleteExpired physically removes lapsed rows. Reads already filter on
// expires_at, so this only bounds table growth; run it on the janitor cadence.
func (d *DurableCompatPlaybackStore) DeleteExpired(ctx context.Context) (int64, error) {
	d.cacheMutationMu.Lock()
	expired := d.mem.deleteExpired()
	for id, compatToken := range expired {
		d.clearUnpersisted(id)
		d.clearPendingUpdates(id)
		d.invalidateValidation(id, compatToken)
		d.bumpCacheGenerations(id, compatToken)
	}
	d.cacheMutationMu.Unlock()
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

// upsert persists a session on the given context. The caller records failures
// while retaining the cache as the authoritative in-process state.
func (d *DurableCompatPlaybackStore) upsert(ctx context.Context, session PlaybackSession) error {
	if d.pool == nil {
		return nil
	}
	data, err := json.Marshal(session)
	if err != nil {
		slog.WarnContext(ctx, "marshal compat playback session failed", "component", "jellycompat", "error", err, "play_session_id", session.ID)
		return err
	}
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = d.now().Add(d.ttl)
	}
	if _, err := d.pool.Exec(ctx, upsertSessionQuery, session.ID, session.CompatToken, session.UserID, data, expiresAt); err != nil {
		slog.WarnContext(ctx, "persist compat playback session failed", "component", "jellycompat", "error", err, "play_session_id", session.ID)
		return err
	}
	return nil
}

// insertIfAbsent repairs a session whose initial upsert failed without reviving
// a row that another process created or terminalized in the meantime.
func (d *DurableCompatPlaybackStore) insertIfAbsent(session *PlaybackSession) (bool, error) {
	data, err := json.Marshal(session)
	if err != nil {
		return false, err
	}
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = d.now().Add(d.ttl)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tag, err := d.pool.Exec(ctx, `
		INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING
	`, session.ID, session.CompatToken, session.UserID, data, expiresAt)
	if err != nil {
		slog.Warn("repair unpersisted compat playback session failed", "error", err, "play_session_id", session.ID)
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// repairUnpersisted retries the creation write retained after Put failed. The
// insert never overwrites a row another process created or terminalized. The
// caller must hold cacheMutationMu.
func (d *DurableCompatPlaybackStore) repairUnpersisted(session *PlaybackSession) bool {
	if session == nil || !d.isUnpersisted(session.ID) {
		return false
	}
	if session.Terminal {
		// Terminal staging owns persistence because it also carries the provider
		// event needed for crash recovery. Never repair a terminal shell without
		// that event from an ordinary routing revalidation.
		return true
	}
	inserted, err := d.insertIfAbsent(session)
	if err != nil {
		return true
	}
	d.clearUnpersisted(session.ID)
	d.bumpCacheGenerations(session.ID, session.CompatToken)
	if inserted {
		return true
	}
	// A concurrent process created the row after the first load. Read its
	// authoritative state instead of overwriting it.
	durable, ok, err := d.load(session.ID)
	if err != nil {
		return true
	}
	if !ok {
		return false
	}
	d.mem.Put(*durable)
	return true
}

func (d *DurableCompatPlaybackStore) load(id string) (*PlaybackSession, bool, error) {
	if d.pool == nil {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var raw []byte
	err := d.pool.QueryRow(ctx,
		`SELECT data FROM jellycompat_playback_sessions WHERE id = $1 AND expires_at > $2`, id, d.now(),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		slog.Warn("load compat playback session failed", "error", err, "play_session_id", id)
		return nil, false, err
	}
	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		slog.Warn("unmarshal compat playback session failed", "error", err, "play_session_id", id)
		return nil, false, err
	}
	return &session, true, nil
}

// loadByCompatToken loads the live rows for a (non-empty) compat token into the
// cache so a subsequent cache scan can resolve the route. The query is bounded by
// the indexed compat_token predicate; FindByRoute never calls it with an empty
// token (that would be an unbounded full-table load).
func (d *DurableCompatPlaybackStore) loadByCompatToken(compatToken string) error {
	if d.pool == nil || compatToken == "" {
		return nil
	}
	generation := d.tokenGenerationSnapshot(compatToken)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := d.pool.Query(ctx,
		`SELECT data FROM jellycompat_playback_sessions WHERE compat_token = $1 AND expires_at > $2`,
		compatToken, d.now())
	if err != nil {
		slog.Warn("load compat playback sessions by token failed", "error", err)
		return err
	}
	defer rows.Close()
	var sessions []PlaybackSession
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			slog.Warn("scan compat playback session failed", "error", err)
			return err
		}
		var session PlaybackSession
		if err := json.Unmarshal(raw, &session); err != nil {
			slog.Warn("unmarshal compat playback session by token failed", "error", err)
			return err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("iterate compat playback sessions failed", "error", err)
		return err
	}
	d.applyCompatTokenSnapshot(compatToken, sessions, generation)
	return nil
}

func (d *DurableCompatPlaybackStore) applyCompatTokenSnapshot(
	compatToken string,
	sessions []PlaybackSession,
	generation compatCacheGeneration,
) bool {
	d.cacheMutationMu.Lock()
	defer d.cacheMutationMu.Unlock()
	if generation != d.tokenGenerationSnapshot(compatToken) {
		d.invalidateValidation("", compatToken)
		return false
	}
	preserveIDs := d.preservedSnapshotIDs(compatToken, sessions)
	affectedIDs := d.mem.replaceByCompatToken(compatToken, sessions, preserveIDs)
	for _, session := range sessions {
		if _, preserve := preserveIDs[session.ID]; preserve {
			d.clearUnpersisted(session.ID)
			continue
		}
		d.markIDValidated(session.ID)
	}
	d.markTokenValidated(compatToken)
	d.bumpCacheGenerations("", compatToken)
	for _, id := range affectedIDs {
		d.bumpCacheGenerations(id, "")
	}
	return true
}

// shouldRevalidateID and shouldRevalidateToken reserve one validation attempt
// per bounded interval. Reserving before I/O prevents concurrent segment
// requests from stampeding Postgres; failed attempts are also briefly throttled
// while callers continue from their last known-good in-memory state.
func (d *DurableCompatPlaybackStore) shouldRevalidateID(id string) bool {
	return d.shouldRevalidate(d.validatedIDs, id)
}

func (d *DurableCompatPlaybackStore) shouldRevalidateToken(compatToken string) bool {
	return d.shouldRevalidate(d.validatedTokens, compatToken)
}

func (d *DurableCompatPlaybackStore) shouldRevalidate(validated map[string]time.Time, key string) bool {
	if key == "" {
		return true
	}
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	now := d.now()
	if checkedAt, ok := validated[key]; ok {
		elapsed := now.Sub(checkedAt)
		if elapsed >= 0 && elapsed < compatCacheRevalidationInterval {
			return false
		}
	}
	d.makeValidationRoom(validated, key, now)
	validated[key] = now
	return true
}

func (d *DurableCompatPlaybackStore) markIDValidated(id string) {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	now := d.now()
	if id != "" {
		d.makeValidationRoom(d.validatedIDs, id, now)
		d.validatedIDs[id] = now
	}
}

func (d *DurableCompatPlaybackStore) markTokenValidated(compatToken string) {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	now := d.now()
	if compatToken != "" {
		d.makeValidationRoom(d.validatedTokens, compatToken, now)
		d.validatedTokens[compatToken] = now
	}
}

// makeValidationRoom keeps attacker-controlled missing IDs from growing the
// throttling maps without bound. Expired entries go first; at capacity an
// arbitrary old slot is reused.
func (d *DurableCompatPlaybackStore) makeValidationRoom(validated map[string]time.Time, key string, now time.Time) {
	if _, exists := validated[key]; exists || len(validated) < compatValidationCacheLimit {
		return
	}
	for candidate, checkedAt := range validated {
		if now.Sub(checkedAt) >= compatCacheRevalidationInterval {
			delete(validated, candidate)
		}
	}
	if len(validated) < compatValidationCacheLimit {
		return
	}
	for candidate := range validated {
		delete(validated, candidate)
		break
	}
}

func (d *DurableCompatPlaybackStore) invalidateValidation(id, compatToken string) {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	delete(d.validatedIDs, id)
	if compatToken != "" {
		delete(d.validatedTokens, compatToken)
	}
}

func (d *DurableCompatPlaybackStore) markUnpersisted(id string) {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	d.unpersistedIDs[id] = struct{}{}
}

func (d *DurableCompatPlaybackStore) clearUnpersisted(id string) {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	delete(d.unpersistedIDs, id)
}

func (d *DurableCompatPlaybackStore) isUnpersisted(id string) bool {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	_, ok := d.unpersistedIDs[id]
	return ok
}

func (d *DurableCompatPlaybackStore) unpersistedSnapshot() map[string]struct{} {
	d.validationMu.Lock()
	defer d.validationMu.Unlock()
	result := make(map[string]struct{}, len(d.unpersistedIDs))
	for id := range d.unpersistedIDs {
		result[id] = struct{}{}
	}
	return result
}

func (d *DurableCompatPlaybackStore) appendPendingUpdate(
	id string,
	compatToken string,
	update func(*PlaybackSession) error,
) {
	d.pendingUpdateMu.Lock()
	defer d.pendingUpdateMu.Unlock()
	d.pendingUpdateSequence++
	d.pendingUpdates[id] = append(d.pendingUpdates[id], pendingCompatPlaybackUpdate{
		sequence:    d.pendingUpdateSequence,
		compatToken: compatToken,
		apply:       update,
	})
}

func (d *DurableCompatPlaybackStore) pendingUpdatesSnapshot(id string) []pendingCompatPlaybackUpdate {
	d.pendingUpdateMu.Lock()
	defer d.pendingUpdateMu.Unlock()
	pending := d.pendingUpdates[id]
	result := make([]pendingCompatPlaybackUpdate, len(pending))
	copy(result, pending)
	return result
}

func (d *DurableCompatPlaybackStore) pendingUpdateIDsSnapshot(compatToken string) map[string]struct{} {
	d.pendingUpdateMu.Lock()
	defer d.pendingUpdateMu.Unlock()
	result := make(map[string]struct{}, len(d.pendingUpdates))
	for id, pending := range d.pendingUpdates {
		for _, update := range pending {
			if update.compatToken == compatToken {
				result[id] = struct{}{}
				break
			}
		}
	}
	return result
}

func (d *DurableCompatPlaybackStore) hasPendingUpdates(id string) bool {
	d.pendingUpdateMu.Lock()
	defer d.pendingUpdateMu.Unlock()
	return len(d.pendingUpdates[id]) > 0
}

func (d *DurableCompatPlaybackStore) consumePendingUpdates(id string, throughSequence uint64) {
	if throughSequence == 0 {
		return
	}
	d.pendingUpdateMu.Lock()
	defer d.pendingUpdateMu.Unlock()
	pending := d.pendingUpdates[id]
	firstRemaining := 0
	for firstRemaining < len(pending) && pending[firstRemaining].sequence <= throughSequence {
		firstRemaining++
	}
	if firstRemaining == len(pending) {
		delete(d.pendingUpdates, id)
		return
	}
	d.pendingUpdates[id] = pending[firstRemaining:]
}

func (d *DurableCompatPlaybackStore) clearPendingUpdates(id string) {
	d.pendingUpdateMu.Lock()
	defer d.pendingUpdateMu.Unlock()
	delete(d.pendingUpdates, id)
}

// preservedSnapshotIDs keeps uncertain local creations and monotonic local
// terminal markers from being replaced by an older active DB snapshot. A
// durable terminal row is safe to apply because it can only advance terminal
// event/claim state.
func (d *DurableCompatPlaybackStore) preservedSnapshotIDs(
	compatToken string,
	durable []PlaybackSession,
) map[string]struct{} {
	preserve := d.unpersistedSnapshot()
	pendingIDs := d.pendingUpdateIDsSnapshot(compatToken)
	durableIDs := make(map[string]struct{}, len(durable))
	durableTerminal := make(map[string]bool, len(durable))
	for _, session := range durable {
		durableIDs[session.ID] = struct{}{}
		durableTerminal[session.ID] = session.Terminal
		if session.Terminal {
			delete(preserve, session.ID)
			d.clearPendingUpdates(session.ID)
		} else if _, pending := pendingIDs[session.ID]; pending {
			preserve[session.ID] = struct{}{}
		}
	}
	for id := range pendingIDs {
		if _, exists := durableIDs[id]; !exists {
			d.clearPendingUpdates(id)
		}
	}
	for id := range d.mem.terminalIDsByCompatToken(compatToken) {
		if !durableTerminal[id] {
			preserve[id] = struct{}{}
		}
	}
	return preserve
}

func (d *DurableCompatPlaybackStore) idGenerationSnapshot(id string) compatCacheGeneration {
	d.generationMu.Lock()
	defer d.generationMu.Unlock()
	return compatCacheGeneration{epoch: d.generationEpoch, value: d.idGenerations[id]}
}

func (d *DurableCompatPlaybackStore) tokenGenerationSnapshot(compatToken string) compatCacheGeneration {
	d.generationMu.Lock()
	defer d.generationMu.Unlock()
	return compatCacheGeneration{epoch: d.generationEpoch, value: d.tokenGenerations[compatToken]}
}

func (d *DurableCompatPlaybackStore) bumpCacheGenerations(id, compatToken string) {
	d.generationMu.Lock()
	defer d.generationMu.Unlock()
	if id != "" {
		d.makeGenerationRoomLocked(d.idGenerations, id)
		d.idGenerations[id]++
	}
	if compatToken != "" {
		d.makeGenerationRoomLocked(d.tokenGenerations, compatToken)
		d.tokenGenerations[compatToken]++
	}
}

// makeGenerationRoomLocked bounds tombstones without letting an in-flight
// snapshot mistake an evicted generation for its original zero value. Advancing
// the epoch invalidates every captured stamp before the maps are reset.
func (d *DurableCompatPlaybackStore) makeGenerationRoomLocked(generations map[string]uint64, key string) {
	if _, exists := generations[key]; exists || len(generations) < compatValidationCacheLimit {
		return
	}
	d.generationEpoch++
	clear(d.idGenerations)
	clear(d.tokenGenerations)
}

// finishCacheMutation must be deferred only while cacheMutationMu is read-held.
func (d *DurableCompatPlaybackStore) finishCacheMutation(id, compatToken string) {
	d.bumpCacheGenerations(id, compatToken)
	d.cacheMutationMu.RUnlock()
}

func (d *DurableCompatPlaybackStore) lockSessionMutation(id string) func() {
	const fnvOffset32 = uint32(2166136261)
	const fnvPrime32 = uint32(16777619)
	hash := fnvOffset32
	for i := 0; i < len(id); i++ {
		hash ^= uint32(id[i])
		hash *= fnvPrime32
	}
	lock := &d.sessionMutations[int(hash%uint32(len(d.sessionMutations)))]
	lock.Lock()
	return lock.Unlock
}

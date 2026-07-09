// Package noderecipe is the recipe-handoff half of restart-resilient playback
// for jellycompat on dedicated transcode nodes. A native transcode carries its
// reconstruction recipe in the stream token, so a transcode node that restarts
// can rebuild ffmpeg from the token the client re-presents. The jellycompat
// node-hop token is server-minted and could carry the recipe too, but it
// deliberately does not: the recipe is mutated in place under a stable session id
// (a Jellyfin audio/subtitle switch restarts ffmpeg without re-minting the
// client's token), and a third-party Jellyfin client cannot be driven to refresh
// a stale token — so a token snapshot could reconstruct a stale rendition. The
// authoritative, mutable recipe therefore lives server-side (the central compat
// store), out of a restarted node's reach (Postgres).
//
// This store bridges that gap over the same shared Redis the offload topology
// already relies on (the node-session tracker): central writes the recipe keyed
// by the upstream session id when it starts a remote transcode, and the
// transcode node reads it on a reconstruct miss. It is
// off the hot path — written once at start, read only after a node restart.
package noderecipe

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// KeyPrefix namespaces per-session recipe keys: silo:noderecipe:<upstreamSessionID>.
const KeyPrefix = "silo:noderecipe:"

// DefaultTTL bounds how long a stored recipe survives. It matches the stream
// token lifetime (playback.MaxTokenTTL, 24h): past it no surviving token could
// still drive a reconstruct, so the recipe is safe to lapse.
const DefaultTTL = playback.MaxTokenTTL

// Store is the Redis-backed recipe store shared by central (writer, at remote
// transcode start) and the transcode nodes (reader, on a reconstruct miss).
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewStore wraps a Redis client. A nil client yields a disabled store whose
// writes no-op and whose reads miss, so a single integrated box (no Redis, no
// remote node) needs no special-casing.
func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{rdb: rdb, ttl: ttl}
}

func key(sessionID string) string { return KeyPrefix + sessionID }

// Put stores the reconstruction recipe for a remote transcode session. Best
// effort: a write error is returned for the caller to log, never fatal.
func (s *Store) Put(ctx context.Context, sessionID string, card playback.RecipeCard) error {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil
	}
	data, err := json.Marshal(card)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, key(sessionID), data, s.ttl).Err()
}

// Get returns the stored recipe for sessionID. It fails CLOSED — a miss or any
// error yields (nil, false) — because an absent recipe legitimately means the
// node cannot reconstruct and should 404, never rebuild from a bad recipe.
func (s *Store) Get(ctx context.Context, sessionID string) (*playback.RecipeCard, bool) {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil, false
	}
	data, err := s.rdb.Get(ctx, key(sessionID)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.WarnContext(ctx, "load node recipe failed", "component", "noderecipe", "error", err, "playback_session_id", sessionID)
		}
		return nil, false
	}
	var card playback.RecipeCard
	if err := json.Unmarshal(data, &card); err != nil {
		slog.WarnContext(ctx, "decode node recipe failed", "component", "noderecipe", "error", err, "playback_session_id", sessionID)
		return nil, false
	}
	return &card, true
}

// Delete removes the stored recipe for sessionID so an explicitly-stopped
// session cannot be resurrected from a leftover recipe after a node restart.
// Safe on a nil/disabled store and on a missing key (a missing key is not an
// error). A delete error is returned for the caller to log, never fatal.
func (s *Store) Delete(ctx context.Context, sessionID string) error {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil
	}
	return s.rdb.Del(ctx, key(sessionID)).Err()
}

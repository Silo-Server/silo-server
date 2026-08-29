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
//
// The same central→node recipe handoff serves a second, independent purpose
// under its own key space: a proxy grant. When an attempt negotiates
// authorized_media_origins_v1 the plan hands the client a credential-free proxy
// URL, so the proxy has no token to serve from — central writes the session's
// recipe here at plan time and the proxy reads it after authenticating the
// caller's own login session. NewStore and NewProxyGrantStore are the two key
// spaces; everything else about them is identical.
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

// ProxyGrantKeyPrefix namespaces per-session proxy grants:
// silo:proxygrant:<playbackSessionID>. It is deliberately a separate key space
// from KeyPrefix: the two are written by different flows, consumed by different
// node roles, and a lookup in one must never resolve the other's entry.
const ProxyGrantKeyPrefix = "silo:proxygrant:"

// DefaultTTL bounds how long a stored recipe survives. It matches the stream
// token lifetime (playback.MaxTokenTTL, 24h): past it no surviving token could
// still drive a reconstruct, so the recipe is safe to lapse.
const DefaultTTL = playback.MaxTokenTTL

const (
	toneMapEnvelopeVersion     = 1
	audioRecipeEnvelopeVersion = 2
)

type toneMapEnvelope struct {
	Version int                 `json:"version"`
	Recipe  playback.RecipeCard `json:"tone_map_recipe"`
}

// audioRecipeEnvelope deliberately uses a shape and version that pre-v2
// readers reject. Those readers ignore SourceAudioChannels when decoding a
// flat card, which would let a rolled-back proxy or transcode node recreate
// the old quiet downmix from a current grant or restart recipe.
type audioRecipeEnvelope struct {
	Version int                 `json:"version"`
	Recipe  playback.RecipeCard `json:"audio_recipe"`
}

// Store is the Redis-backed recipe store shared by central (writer) and the
// nodes (readers). One instance owns exactly one key prefix; see NewStore and
// NewProxyGrantStore for the two uses.
type Store struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

// NewStore wraps a Redis client for the transcode-node recipe handoff. A nil
// client yields a disabled store whose writes no-op and whose reads miss, so a
// single integrated box (no Redis, no remote node) needs no special-casing.
func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	return newStore(rdb, KeyPrefix, ttl)
}

// NewProxyGrantStore wraps a Redis client for the proxy-grant key space: the
// recipe a proxy serves a header-authenticated session from once it has
// authenticated the caller. Same nil-safety and TTL as NewStore.
func NewProxyGrantStore(rdb *redis.Client, ttl time.Duration) *Store {
	return newStore(rdb, ProxyGrantKeyPrefix, ttl)
}

func newStore(rdb *redis.Client, prefix string, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{rdb: rdb, prefix: prefix, ttl: ttl}
}

// Enabled reports whether this store can actually carry a recipe. A caller that
// hands out a URL only the stored recipe can serve must check it: a disabled
// store accepts Put silently (by design, for the Redis-less integrated box), so
// a successful write is not on its own evidence that the recipe exists.
func (s *Store) Enabled() bool { return s != nil && s.rdb != nil }

func (s *Store) key(sessionID string) string { return s.prefix + sessionID }

// Put stores the reconstruction recipe for a remote transcode session. Best
// effort: a write error is returned for the caller to log, never fatal.
func (s *Store) Put(ctx context.Context, sessionID string, card playback.RecipeCard) error {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil
	}
	data, err := marshalCard(card)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.key(sessionID), data, s.ttl).Err()
}

// Get returns the stored recipe for sessionID. It fails CLOSED — a miss or any
// error yields (nil, false) — because an absent recipe legitimately means the
// node cannot reconstruct and should 404, never rebuild from a bad recipe.
func (s *Store) Get(ctx context.Context, sessionID string) (*playback.RecipeCard, bool) {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil, false
	}
	data, err := s.rdb.Get(ctx, s.key(sessionID)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.WarnContext(ctx, "load node recipe failed", "component", "noderecipe", "key_prefix", s.prefix, "error", err, "playback_session_id", sessionID)
		}
		return nil, false
	}
	card, ok := unmarshalCard(data)
	if !ok {
		slog.WarnContext(ctx, "decode node recipe failed", "component", "noderecipe", "key_prefix", s.prefix, "playback_session_id", sessionID)
		return nil, false
	}
	return &card, true
}

func marshalCard(card playback.RecipeCard) ([]byte, error) {
	// Audio takes precedence when a card also tone-maps: a v1 tone-map-aware
	// reader understands that envelope but not the byte-affecting audio field.
	if audioRecipeCard(card) {
		return json.Marshal(audioRecipeEnvelope{Version: audioRecipeEnvelopeVersion, Recipe: card})
	}
	// SourceAudioChannels only has byte-affecting meaning as part of the exact
	// v2 recipe. Do not leak a partial recipe through a legacy flat card or
	// tone-map envelope where an older executor could interpret it broadly.
	card.SourceAudioChannels = 0
	if toneMapCard(card) {
		return json.Marshal(toneMapEnvelope{Version: toneMapEnvelopeVersion, Recipe: card})
	}
	return json.Marshal(card)
}

func unmarshalCard(data []byte) (playback.RecipeCard, bool) {
	var header struct {
		Version       int             `json:"version"`
		ToneMapRecipe json.RawMessage `json:"tone_map_recipe"`
		AudioRecipe   json.RawMessage `json:"audio_recipe"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return playback.RecipeCard{}, false
	}
	var card playback.RecipeCard
	if header.Version != 0 || len(header.ToneMapRecipe) != 0 || len(header.AudioRecipe) != 0 {
		switch header.Version {
		case toneMapEnvelopeVersion:
			if len(header.ToneMapRecipe) == 0 || len(header.AudioRecipe) != 0 || json.Unmarshal(header.ToneMapRecipe, &card) != nil || !toneMapCard(card) {
				return playback.RecipeCard{}, false
			}
		case audioRecipeEnvelopeVersion:
			if len(header.AudioRecipe) == 0 || len(header.ToneMapRecipe) != 0 || json.Unmarshal(header.AudioRecipe, &card) != nil || !audioRecipeCard(card) {
				return playback.RecipeCard{}, false
			}
		default:
			return playback.RecipeCard{}, false
		}
		return card, true
	}
	if err := json.Unmarshal(data, &card); err != nil {
		return playback.RecipeCard{}, false
	}
	return card, true
}

func toneMapCard(card playback.RecipeCard) bool {
	return card.ToneMapPolicy != "" || card.ToneMapMode != "" || card.ToneMapSourceKind != "" ||
		card.ToneMapRecipeVersion != "" || card.ToneMapPreflightRequired || !card.ToneMapSourceRevision.IsZero() ||
		card.ToneMapDVConfigPresent || card.ToneMapDVBLCompatIDPresent || card.ToneMapDVBLPresent || card.ToneMapDVRPUPresent
}

func audioRecipeCard(card playback.RecipeCard) bool {
	return card.TranscodeAudio && playback.IsAudioToAACStereoDownmixV3(card.SourceAudioChannels, card.TargetCodecAudio, card.TargetAudioChannels)
}

// Delete removes the stored recipe for sessionID so an explicitly-stopped
// session cannot be resurrected from a leftover recipe after a node restart.
// Safe on a nil/disabled store and on a missing key (a missing key is not an
// error). A delete error is returned for the caller to log, never fatal.
func (s *Store) Delete(ctx context.Context, sessionID string) error {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil
	}
	return s.rdb.Del(ctx, s.key(sessionID)).Err()
}

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
// transcode node reads it on a reconstruct miss. Transcode-executed progressive
// remuxes also use the record as durable active authority: central writes it
// before publishing the route, the node checks it before each remux response,
// and deliberate teardown deletes it so a surviving token cannot restart work.
// Each node record is stamped with a per-node authority generation; destructive
// force reload advances that generation so even a published remux URL which has
// not received its first request is revoked without enumerating recipe keys.
//
// The same central→node recipe handoff serves a second, independent purpose
// under its own key space: a proxy grant. When an attempt negotiates
// authorized_media_origins_v1 the plan hands the client a credential-free proxy
// URL, so the proxy has no token to serve from — central writes the session's
// recipe here at plan time and the proxy reads it after authenticating the
// caller's own login session. NewStore and NewProxyGrantStore are the two key
// spaces. Proxy grants deliberately do not participate in the transcode-node
// authority generation.
package noderecipe

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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

// nodeAuthorityGenerationKeyPrefix namespaces the per-node Redis timestamp
// generation that force reload advances to revoke every outstanding node
// recipe, including a progressive-remux URL with no request yet.
const nodeAuthorityGenerationKeyPrefix = "silo:noderecipe-authority-generation:"

// nodeAuthorityRecordGenerationKeyPrefix namespaces the generation stamped
// alongside one node recipe. Keeping it in a sidecar preserves the recipe's
// previous wire format for older transcode nodes during rolling upgrades.
const nodeAuthorityRecordGenerationKeyPrefix = "silo:noderecipe-authority-record:"

// nodeAuthorityRecordDigestKeyPrefix binds the generation sidecar to the
// recipe bytes it stamped. An older API may overwrite only the legacy recipe
// key; the digest lets a current node recognize that the surviving generation
// belongs to different bytes and validate the overwrite as a legacy write.
const nodeAuthorityRecordDigestKeyPrefix = "silo:noderecipe-authority-digest:"

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

// putNodeRecipeScript reads the node generation and writes the legacy-readable
// recipe plus generation and digest sidecars in one Redis operation. It
// therefore orders a concurrent Put and RevokeNode: a recipe written before
// the increment is stale, while one written after it carries the new generation.
var putNodeRecipeScript = redis.NewScript(`
local generation = redis.call("GET", KEYS[3])
if not generation then
  generation = "0"
end
redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
redis.call("SET", KEYS[2], generation, "PX", ARGV[2])
redis.call("SET", KEYS[4], ARGV[3], "PX", ARGV[2])
return generation
`)

// validateLegacyNodeRecipeScript accepts a record with missing or mismatched
// sidecars only when its Redis expiry proves that an older writer issued it
// after the latest reload. The exact recipe comparison makes the decision
// apply to the bytes Get decoded even if another writer updates the key
// concurrently.
var validateLegacyNodeRecipeScript = redis.NewScript(`
local recipe = redis.call("GET", KEYS[1])
if not recipe or recipe ~= ARGV[1] then
  return 0
end
local binding = redis.call("GET", KEYS[4])
if binding and binding == ARGV[3] then
  local stamped_generation = tonumber(redis.call("GET", KEYS[2]))
  local current_generation = tonumber(redis.call("GET", KEYS[3])) or 0
  if stamped_generation and stamped_generation == current_generation then
    return 1
  end
  return 0
end
local generation = tonumber(redis.call("GET", KEYS[3]))
if not generation or generation == 0 then
  return 1
end
local ttl = redis.call("PTTL", KEYS[1])
if ttl < 0 then
  return 0
end
local now = redis.call("TIME")
local now_ms = now[1] * 1000 + math.floor(now[2] / 1000)
local issued_at = now_ms - (tonumber(ARGV[2]) - ttl)
if issued_at > generation then
  return 1
end
return 0
`)

var revokeNodeScript = redis.NewScript(`
local now = redis.call("TIME")
local now_ms = now[1] * 1000 + math.floor(now[2] / 1000)
local current = tonumber(redis.call("GET", KEYS[1])) or 0
if now_ms <= current then
  now_ms = current + 1
end
redis.call("SET", KEYS[1], now_ms)
return now_ms
`)

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

func nodeAuthorityGenerationKey(nodeURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(nodeURL), "/")
	return nodeAuthorityGenerationKeyPrefix + normalized
}

func nodeAuthorityGenerationKeyForNodeID(nodeID int) string {
	return nodeAuthorityGenerationKeyPrefix + "node-id:" + strconv.Itoa(nodeID)
}

func nodeAuthorityGenerationKeyForCard(card playback.RecipeCard) string {
	if card.RoutingExecutionNodeID > 0 {
		return nodeAuthorityGenerationKeyForNodeID(card.RoutingExecutionNodeID)
	}
	if strings.TrimSpace(card.TranscodeNodeURL) != "" {
		return nodeAuthorityGenerationKey(card.TranscodeNodeURL)
	}
	return ""
}

func nodeAuthorityRecordGenerationKey(sessionID string) string {
	return nodeAuthorityRecordGenerationKeyPrefix + sessionID
}

func nodeAuthorityRecordDigestKey(sessionID string) string {
	return nodeAuthorityRecordDigestKeyPrefix + sessionID
}

func nodeAuthorityRecordDigest(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

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
	if authorityKey := nodeAuthorityGenerationKeyForCard(card); s.prefix == KeyPrefix && authorityKey != "" {
		ttlMillis := max(s.ttl.Milliseconds(), 1)
		return putNodeRecipeScript.Run(ctx, s.rdb, []string{
			s.key(sessionID),
			nodeAuthorityRecordGenerationKey(sessionID),
			authorityKey,
			nodeAuthorityRecordDigestKey(sessionID),
		}, data, ttlMillis, nodeAuthorityRecordDigest(data)).Err()
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
	data, generation, stamped, err := s.loadStoredCard(ctx, sessionID)
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
	if authorityKey := nodeAuthorityGenerationKeyForCard(card); s.prefix == KeyPrefix && authorityKey != "" {
		if stamped {
			currentGeneration, generationErr := s.currentNodeAuthorityGeneration(ctx, authorityKey)
			if generationErr != nil {
				slog.WarnContext(ctx, "load node authority generation failed", "component", "noderecipe", "error", generationErr, "playback_session_id", sessionID)
				return nil, false
			}
			if generation != currentGeneration {
				return nil, false
			}
		} else {
			valid, validationErr := s.legacyRecipeIssuedAfterRevocation(ctx, sessionID, data, authorityKey)
			if validationErr != nil {
				slog.WarnContext(ctx, "validate legacy node recipe authority failed", "component", "noderecipe", "error", validationErr, "playback_session_id", sessionID)
				return nil, false
			}
			if !valid {
				return nil, false
			}
		}
	}
	return &card, true
}

func (s *Store) loadStoredCard(ctx context.Context, sessionID string) ([]byte, int64, bool, error) {
	if s.prefix != KeyPrefix {
		data, err := s.rdb.Get(ctx, s.key(sessionID)).Bytes()
		return data, 0, false, err
	}
	values, err := s.rdb.MGet(ctx,
		s.key(sessionID),
		nodeAuthorityRecordGenerationKey(sessionID),
		nodeAuthorityRecordDigestKey(sessionID),
	).Result()
	if err != nil {
		return nil, 0, false, err
	}
	recipe, ok := values[0].(string)
	if !ok {
		return nil, 0, false, redis.Nil
	}
	if values[1] == nil || values[2] == nil {
		return []byte(recipe), 0, false, nil
	}
	recordDigest, ok := values[2].(string)
	if !ok || recordDigest != nodeAuthorityRecordDigest([]byte(recipe)) {
		return []byte(recipe), 0, false, nil
	}
	rawGeneration, ok := values[1].(string)
	if !ok {
		return nil, 0, false, errors.New("invalid node authority generation type")
	}
	generation, err := strconv.ParseInt(rawGeneration, 10, 64)
	if err != nil {
		return nil, 0, false, fmt.Errorf("parse node authority generation: %w", err)
	}
	return []byte(recipe), generation, true, nil
}

func (s *Store) legacyRecipeIssuedAfterRevocation(ctx context.Context, sessionID string, data []byte, authorityKey string) (bool, error) {
	ttlMillis := max(s.ttl.Milliseconds(), 1)
	valid, err := validateLegacyNodeRecipeScript.Run(ctx, s.rdb, []string{
		s.key(sessionID),
		nodeAuthorityRecordGenerationKey(sessionID),
		authorityKey,
		nodeAuthorityRecordDigestKey(sessionID),
	}, data, ttlMillis, nodeAuthorityRecordDigest(data)).Int()
	return valid == 1, err
}

func (s *Store) currentNodeAuthorityGeneration(ctx context.Context, authorityKey string) (int64, error) {
	generation, err := s.rdb.Get(ctx, authorityKey).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return generation, err
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
	keys := []string{s.key(sessionID)}
	if s.prefix == KeyPrefix {
		keys = append(keys, nodeAuthorityRecordGenerationKey(sessionID), nodeAuthorityRecordDigestKey(sessionID))
	}
	return s.rdb.Del(ctx, keys...).Err()
}

// RevokeNode invalidates every recipe previously issued for nodeURL. The
// generation intentionally outlives individual recipes: the Redis-server
// timestamp is bounded per configured node and distinguishes a pre-reload
// legacy record from one issued later by an old API.
func (s *Store) RevokeNode(ctx context.Context, nodeURL string) error {
	if s == nil || s.rdb == nil || s.prefix != KeyPrefix || strings.TrimSpace(nodeURL) == "" {
		return nil
	}
	return revokeNodeScript.Run(ctx, s.rdb, []string{
		nodeAuthorityGenerationKey(nodeURL),
	}).Err()
}

// RevokeNodeID invalidates current progressive-remux recipes for the stable
// stream_nodes row. Unlike a route URL, the row identity survives URL repoints
// and process replacement, so a failed reload can always retry it.
func (s *Store) RevokeNodeID(ctx context.Context, nodeID int) error {
	if s == nil || s.rdb == nil || s.prefix != KeyPrefix || nodeID <= 0 {
		return nil
	}
	return revokeNodeScript.Run(ctx, s.rdb, []string{
		nodeAuthorityGenerationKeyForNodeID(nodeID),
	}).Err()
}

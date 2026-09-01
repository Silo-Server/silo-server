package noderecipe

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// A nil-backed store (single integrated box, no Redis) must be safe: writes
// no-op and reads miss, so callers need no nil-guarding.
func TestNilStore_PutNoopGetMiss(t *testing.T) {
	var s *Store // nil receiver
	if err := s.Put(context.Background(), "sid", playback.RecipeCard{}); err != nil {
		t.Fatalf("nil store Put returned error: %v", err)
	}
	if card, ok := s.Get(context.Background(), "sid"); ok || card != nil {
		t.Fatalf("nil store Get = (%v, %v), want (nil, false)", card, ok)
	}

	disabled := NewStore(nil, 0)
	if err := disabled.Put(context.Background(), "sid", playback.RecipeCard{}); err != nil {
		t.Fatalf("disabled store Put returned error: %v", err)
	}
	if _, ok := disabled.Get(context.Background(), "sid"); ok {
		t.Fatalf("disabled store Get returned a hit, want miss")
	}
}

// Delete on a nil/disabled store must be a safe no-op (callers in teardown
// paths need no nil-guarding), and on either store a subsequent Get still
// misses, matching the delete-then-get-not-found and delete-missing-is-no-op
// contract.
func TestNilStore_DeleteNoop(t *testing.T) {
	var s *Store // nil receiver
	if err := s.Delete(context.Background(), "sid"); err != nil {
		t.Fatalf("nil store Delete returned error: %v", err)
	}
	if card, ok := s.Get(context.Background(), "sid"); ok || card != nil {
		t.Fatalf("nil store Get after Delete = (%v, %v), want (nil, false)", card, ok)
	}

	disabled := NewStore(nil, 0)
	// Delete of a missing key is a no-op success.
	if err := disabled.Delete(context.Background(), "sid"); err != nil {
		t.Fatalf("disabled store Delete returned error: %v", err)
	}
	if _, ok := disabled.Get(context.Background(), "sid"); ok {
		t.Fatalf("disabled store Get after Delete returned a hit, want miss")
	}
}

func TestNilStore_RevokeNodeNoop(t *testing.T) {
	var s *Store
	if err := s.RevokeNode(t.Context(), "http://node"); err != nil {
		t.Fatalf("nil store RevokeNode returned error: %v", err)
	}
	if err := NewStore(nil, 0).RevokeNode(t.Context(), "http://node"); err != nil {
		t.Fatalf("disabled store RevokeNode returned error: %v", err)
	}
	if err := s.RevokeNodeID(t.Context(), 42); err != nil {
		t.Fatalf("nil store RevokeNodeID returned error: %v", err)
	}
	if err := NewStore(nil, 0).RevokeNodeID(t.Context(), 42); err != nil {
		t.Fatalf("disabled store RevokeNodeID returned error: %v", err)
	}
}

func TestKeyNamespacing(t *testing.T) {
	if got := NewStore(nil, 0).key("abc"); got != "silo:noderecipe:abc" {
		t.Fatalf("key(abc) = %q, want silo:noderecipe:abc", got)
	}
}

func TestNodeAuthorityGenerationKeyNormalizesNodeURL(t *testing.T) {
	withoutSlash := nodeAuthorityGenerationKey("http://node:8070")
	if withSlash := nodeAuthorityGenerationKey(" http://node:8070/ "); withSlash != withoutSlash {
		t.Fatalf("generation keys differ: %q != %q", withSlash, withoutSlash)
	}
	if withoutSlash == nodeAuthorityGenerationKey("http://other-node:8070") {
		t.Fatal("different nodes share an authority generation key")
	}
}

func TestNodeAuthorityGenerationKeyUsesStableNodeIDWhenAvailable(t *testing.T) {
	card := playback.RecipeCard{TranscodeNodeURL: "http://node:8070", RoutingExecutionNodeID: 42}
	if got, want := nodeAuthorityGenerationKeyForCard(card), nodeAuthorityGenerationKeyForNodeID(42); got != want {
		t.Fatalf("card authority key = %q, want stable ID key %q", got, want)
	}
	if nodeAuthorityGenerationKeyForNodeID(42) == nodeAuthorityGenerationKey("http://node-id:42") {
		t.Fatal("stable node ID and route URL share an authority key")
	}
}

func TestNodeAuthorityRecordSidecarKeysAreSeparate(t *testing.T) {
	store := NewStore(nil, 0)
	if got := nodeAuthorityRecordGenerationKey("abc"); got == store.key("abc") {
		t.Fatalf("record generation key %q collides with recipe key", got)
	}
	if got := nodeAuthorityRecordDigestKey("abc"); got == store.key("abc") || got == nodeAuthorityRecordGenerationKey("abc") {
		t.Fatalf("record digest key %q collides with another authority key", got)
	}
}

// The two key spaces share one implementation, so nothing but the prefix may
// distinguish them: a proxy grant must never resolve a node recipe, and the
// transcode node's reconstruct lookup must never resolve a grant.
func TestProxyGrantStoreIsolatesItsKeySpace(t *testing.T) {
	grants := NewProxyGrantStore(nil, 0)
	if got := grants.key("abc"); got != "silo:proxygrant:abc" {
		t.Fatalf("proxy grant key(abc) = %q, want silo:proxygrant:abc", got)
	}
	if grants.key("abc") == NewStore(nil, 0).key("abc") {
		t.Fatal("proxy grants and node recipes share a key")
	}
	if grants.ttl != DefaultTTL {
		t.Fatalf("proxy grant ttl = %v, want %v", grants.ttl, DefaultTTL)
	}
}

// A disabled store accepts writes it cannot serve, so callers that publish a
// URL only the grant can satisfy need this distinction to stay on the API.
func TestProxyGrantStoreReportsWhetherItCanCarryAGrant(t *testing.T) {
	var missing *Store
	if missing.Enabled() {
		t.Fatal("nil store reported itself enabled")
	}
	disabled := NewProxyGrantStore(nil, 0)
	if disabled.Enabled() {
		t.Fatal("Redis-less store reported itself enabled")
	}
	if err := disabled.Put(context.Background(), "sid", playback.RecipeCard{}); err != nil {
		t.Fatalf("disabled proxy grant Put returned error: %v", err)
	}
	if _, ok := disabled.Get(context.Background(), "sid"); ok {
		t.Fatal("disabled proxy grant Get returned a hit, want miss")
	}
	if err := disabled.Delete(context.Background(), "sid"); err != nil {
		t.Fatalf("disabled proxy grant Delete returned error: %v", err)
	}
}

func TestDefaultTTLMatchesTokenLifetime(t *testing.T) {
	if DefaultTTL != playback.MaxTokenTTL {
		t.Fatalf("DefaultTTL = %v, want playback.MaxTokenTTL %v", DefaultTTL, playback.MaxTokenTTL)
	}
	if NewStore(nil, 0).ttl != DefaultTTL {
		t.Fatal("NewStore with ttl<=0 did not default to DefaultTTL")
	}
}

func TestNodeAuthorityKeepsPreviousRecipeWireFormat(t *testing.T) {
	card := playback.RecipeCard{
		SessionID: "sid", TranscodeNodeURL: "http://node:8070", PlayMethod: playback.PlayTranscode,
		TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2,
	}
	data, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok := unmarshalCard(data)
	if !ok || decoded != card {
		t.Fatalf("previous node decode = (%+v, %v), want (%+v, true)", decoded, ok, card)
	}
}

func TestNodeAuthorityGenerationRevokesDormantRecipes(t *testing.T) {
	rawURL := os.Getenv("SILO_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })

	unique := uuid.NewString()
	parsedNodeID, err := strconv.ParseInt(unique[:8], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := int(parsedNodeID%2_000_000_000) + 1
	nodeURL := "http://node-" + unique
	store := NewStore(client, time.Minute)
	grants := NewProxyGrantStore(client, time.Minute)
	oldSessionID := "old-" + unique
	newSessionID := "new-" + unique
	legacyBeforeSessionID := "legacy-before-" + unique
	legacyAfterSessionID := "legacy-after-" + unique
	legacyOverwriteBeforeSessionID := "legacy-overwrite-before-" + unique
	legacyOverwriteSessionID := "legacy-overwrite-" + unique
	grantID := "grant-" + unique
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		_ = client.Del(cleanupCtx,
			store.key(oldSessionID), store.key(newSessionID), store.key(legacyBeforeSessionID), store.key(legacyAfterSessionID), store.key(legacyOverwriteBeforeSessionID), store.key(legacyOverwriteSessionID),
			nodeAuthorityRecordGenerationKey(oldSessionID), nodeAuthorityRecordGenerationKey(newSessionID),
			nodeAuthorityRecordGenerationKey(legacyBeforeSessionID), nodeAuthorityRecordGenerationKey(legacyAfterSessionID), nodeAuthorityRecordGenerationKey(legacyOverwriteBeforeSessionID), nodeAuthorityRecordGenerationKey(legacyOverwriteSessionID),
			nodeAuthorityRecordDigestKey(oldSessionID), nodeAuthorityRecordDigestKey(newSessionID),
			nodeAuthorityRecordDigestKey(legacyBeforeSessionID), nodeAuthorityRecordDigestKey(legacyAfterSessionID), nodeAuthorityRecordDigestKey(legacyOverwriteBeforeSessionID), nodeAuthorityRecordDigestKey(legacyOverwriteSessionID),
			grants.key(grantID), nodeAuthorityGenerationKey(nodeURL), nodeAuthorityGenerationKeyForNodeID(nodeID),
		).Err()
	})

	card := playback.RecipeCard{
		SessionID: oldSessionID, TranscodeNodeURL: nodeURL, PlayMethod: playback.PlayRemux,
		RoutingExecutionNodeID: nodeID,
	}
	if err := store.Put(t.Context(), oldSessionID, card); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), oldSessionID); !ok {
		t.Fatal("fresh node recipe missed before revocation")
	}
	rawRecipe, err := client.Get(t.Context(), store.key(oldSessionID)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if decoded, ok := unmarshalCard(rawRecipe); !ok || decoded != card {
		t.Fatalf("stored recipe is not readable by the previous node: (%+v, %v)", decoded, ok)
	}
	legacyBeforeCard := card
	legacyBeforeCard.SessionID = legacyBeforeSessionID
	legacyBeforeData, err := marshalCard(legacyBeforeCard)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.key(legacyBeforeSessionID), legacyBeforeData, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	legacyOverwriteCard := card
	legacyOverwriteCard.SessionID = legacyOverwriteSessionID
	if err := store.Put(t.Context(), legacyOverwriteSessionID, legacyOverwriteCard); err != nil {
		t.Fatal(err)
	}
	legacyOverwriteBeforeCard := card
	legacyOverwriteBeforeCard.SessionID = legacyOverwriteBeforeSessionID
	if err := store.Put(t.Context(), legacyOverwriteBeforeSessionID, legacyOverwriteBeforeCard); err != nil {
		t.Fatal(err)
	}
	legacyOverwriteBeforeCard.InputPath = "/media/stale-legacy-overwrite.mkv"
	legacyOverwriteBeforeData, err := marshalCard(legacyOverwriteBeforeCard)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.key(legacyOverwriteBeforeSessionID), legacyOverwriteBeforeData, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeNodeID(t.Context(), nodeID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), oldSessionID); ok {
		t.Fatal("recipe issued before node revocation remained valid")
	}
	if _, ok := NewStore(client, time.Minute).Get(t.Context(), oldSessionID); ok {
		t.Fatal("fresh store instance forgot the stable node revocation")
	}

	card.SessionID = newSessionID
	if err := store.Put(t.Context(), newSessionID, card); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), newSessionID); !ok {
		t.Fatal("recipe issued after node revocation was rejected")
	}

	if _, ok := store.Get(t.Context(), legacyBeforeSessionID); ok {
		t.Fatal("legacy generation-zero recipe survived node revocation")
	}
	if _, ok := store.Get(t.Context(), legacyOverwriteBeforeSessionID); ok {
		t.Fatal("pre-reload legacy overwrite with stale sidecars survived node revocation")
	}

	revokedAt, err := client.Get(t.Context(), nodeAuthorityGenerationKeyForNodeID(nodeID)).Int64()
	if err != nil {
		t.Fatal(err)
	}
	for {
		redisTime, timeErr := client.Time(t.Context()).Result()
		if timeErr != nil {
			t.Fatal(timeErr)
		}
		if redisTime.UnixMilli() > revokedAt {
			break
		}
	}
	legacyAfterCard := card
	legacyAfterCard.SessionID = legacyAfterSessionID
	legacyAfterData, err := marshalCard(legacyAfterCard)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.key(legacyAfterSessionID), legacyAfterData, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), legacyAfterSessionID); !ok {
		t.Fatal("legacy recipe issued after node revocation was rejected")
	}
	legacyOverwriteCard.InputPath = "/media/updated-by-legacy-api.mkv"
	legacyOverwriteData, err := marshalCard(legacyOverwriteCard)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.key(legacyOverwriteSessionID), legacyOverwriteData, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Get(t.Context(), legacyOverwriteSessionID); !ok || got.InputPath != legacyOverwriteCard.InputPath {
		t.Fatalf("legacy overwrite with stale sidecars = (%+v, %t), want updated recipe", got, ok)
	}

	grant := playback.RecipeCard{SessionID: grantID, TranscodeNodeURL: nodeURL, PlayMethod: playback.PlayRemux}
	if err := grants.Put(t.Context(), grantID, grant); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevokeNode(t.Context(), nodeURL); err != nil {
		t.Fatal(err)
	}
	if _, ok := grants.Get(t.Context(), grantID); !ok {
		t.Fatal("node authority revocation affected proxy-grant key space")
	}

	if err := store.Delete(t.Context(), newSessionID); err != nil {
		t.Fatal(err)
	}
	remaining, err := client.Exists(t.Context(),
		store.key(newSessionID),
		nodeAuthorityRecordGenerationKey(newSessionID),
		nodeAuthorityRecordDigestKey(newSessionID),
	).Result()
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("node recipe delete left an authority sidecar behind")
	}
}

func TestToneMapRecipeEnvelopeFailsClosedOnLegacyReader(t *testing.T) {
	card := playback.RecipeCard{
		SessionID: "sid", PlayMethod: playback.PlayTranscode,
		InputPath: "/media/movie.mkv", SegmentDuration: 4, TargetCodecVideo: "h264",
		ToneMapMode: tonemap.ModeHardware,
	}
	data, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}

	var legacy playback.RecipeCard
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.SessionID != "" || legacy.SegmentDuration != 0 || legacy.TargetCodecVideo != "" {
		t.Fatalf("legacy flat decode = %+v, want incomplete recipe", legacy)
	}
	decoded, ok := unmarshalCard(data)
	if !ok || decoded != card {
		t.Fatalf("current decode = (%+v, %v), want original card", decoded, ok)
	}
}

func TestStereoDownmixRecipeEnvelopeFailsClosedOnLegacyReader(t *testing.T) {
	for _, card := range []playback.RecipeCard{
		{
			SessionID: "sid", PlayMethod: playback.PlayTranscode, TranscodeAudio: true,
			InputPath: "/media/movie.mkv", SegmentDuration: 4, TargetCodecVideo: "h264",
			TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2,
		},
		{
			SessionID: "sid", PlayMethod: playback.PlayTranscode, TranscodeAudio: true,
			InputPath: "/media/movie.mkv", SegmentDuration: 4, TargetCodecVideo: "h264",
			TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2, ToneMapMode: tonemap.ModeHardware,
		},
	} {
		data, err := marshalCard(card)
		if err != nil {
			t.Fatal(err)
		}

		var legacy playback.RecipeCard
		if err := json.Unmarshal(data, &legacy); err != nil {
			t.Fatal(err)
		}
		if legacy.SessionID != "" || legacy.SegmentDuration != 0 || legacy.TargetCodecVideo != "" {
			t.Fatalf("legacy flat decode = %+v, want incomplete recipe", legacy)
		}
		decoded, ok := unmarshalCard(data)
		if !ok || decoded != card {
			t.Fatalf("current decode = (%+v, %v), want original card", decoded, ok)
		}
	}
}

func TestOrdinaryRecipeRemainsLegacyFlatJSON(t *testing.T) {
	for _, card := range []playback.RecipeCard{
		{SessionID: "sid", PlayMethod: playback.PlayTranscode, InputPath: "/media/movie.mkv", SegmentDuration: 4, TargetCodecVideo: "h264"},
		{SessionID: "stereo-source", PlayMethod: playback.PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 2, TargetAudioChannels: 2},
		{SessionID: "copy-remux", PlayMethod: playback.PlayRemux, TranscodeAudio: false, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2},
		{SessionID: "surround-output", PlayMethod: playback.PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 6},
		{SessionID: "non-aac", PlayMethod: playback.PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "eac3", SourceAudioChannels: 6, TargetAudioChannels: 2},
		{SessionID: "opus", PlayMethod: playback.PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "opus", SourceAudioChannels: 6, TargetAudioChannels: 2},
		{SessionID: "unknown-codec", PlayMethod: playback.PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "unknown", SourceAudioChannels: 6, TargetAudioChannels: 2},
	} {
		data, err := marshalCard(card)
		if err != nil {
			t.Fatal(err)
		}
		var legacy playback.RecipeCard
		if err := json.Unmarshal(data, &legacy); err != nil {
			t.Fatal(err)
		}
		want := card
		want.SourceAudioChannels = 0
		if legacy != want {
			t.Fatalf("legacy flat decode = %+v, want sanitized %+v", legacy, want)
		}
		decoded, ok := unmarshalCard(data)
		if !ok || decoded != want {
			t.Fatalf("current decode = (%+v, %v), want sanitized %+v", decoded, ok, want)
		}
	}
}

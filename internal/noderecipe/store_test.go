package noderecipe

import (
	"context"
	"encoding/json"
	"os"
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

func TestNodeAuthorityEnvelopePreservesNestedRecipe(t *testing.T) {
	card := playback.RecipeCard{
		SessionID: "sid", TranscodeNodeURL: "http://node:8070", PlayMethod: playback.PlayTranscode,
		TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2,
	}
	inner, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(nodeAuthorityEnvelope{
		Version: nodeAuthorityEnvelopeVersion, AuthorityGeneration: 7, Recipe: inner,
	})
	if err != nil {
		t.Fatal(err)
	}

	decoded, generation, ok := unmarshalStoredCard(data, true)
	if !ok || decoded != card || generation != 7 {
		t.Fatalf("decode = (%+v, %d, %v), want (%+v, 7, true)", decoded, generation, ok, card)
	}
	if _, _, ok := unmarshalStoredCard(data, false); ok {
		t.Fatal("proxy-grant decoder accepted a node-authority envelope")
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
	nodeURL := "http://node-" + unique
	store := NewStore(client, time.Minute)
	grants := NewProxyGrantStore(client, time.Minute)
	oldSessionID := "old-" + unique
	newSessionID := "new-" + unique
	legacySessionID := "legacy-" + unique
	grantID := "grant-" + unique
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		_ = client.Del(cleanupCtx,
			store.key(oldSessionID), store.key(newSessionID), store.key(legacySessionID),
			grants.key(grantID), nodeAuthorityGenerationKey(nodeURL),
		).Err()
	})

	card := playback.RecipeCard{SessionID: oldSessionID, TranscodeNodeURL: nodeURL, PlayMethod: playback.PlayRemux}
	if err := store.Put(t.Context(), oldSessionID, card); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), oldSessionID); !ok {
		t.Fatal("fresh node recipe missed before revocation")
	}
	if err := store.RevokeNode(t.Context(), nodeURL); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), oldSessionID); ok {
		t.Fatal("recipe issued before node revocation remained valid")
	}

	card.SessionID = newSessionID
	if err := store.Put(t.Context(), newSessionID, card); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), newSessionID); !ok {
		t.Fatal("recipe issued after node revocation was rejected")
	}

	legacyCard := card
	legacyCard.SessionID = legacySessionID
	legacyData, err := marshalCard(legacyCard)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.key(legacySessionID), legacyData, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), legacySessionID); ok {
		t.Fatal("legacy generation-zero recipe survived node revocation")
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

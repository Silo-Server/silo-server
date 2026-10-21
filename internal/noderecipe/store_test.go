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
	sessions := NewSessionRecipeStore(nil, 0)
	if got := sessions.key("abc"); got != "silo:sessionrecipe:abc" {
		t.Fatalf("session recipe key(abc) = %q, want silo:sessionrecipe:abc", got)
	}
	if sessions.key("abc") == grants.key("abc") || sessions.key("abc") == NewStore(nil, 0).key("abc") {
		t.Fatal("session recipes share a key with another key space")
	}
}

// The session-recipe reader must tell a Redis outage from a missing session:
// answering an outage as "gone" makes a client tear its playback down. Get
// keeps failing closed for the node and proxy readers.
func TestLookupReportsAnUnreachableRedis(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := NewSessionRecipeStore(client, time.Minute)

	if card, ok, err := store.Lookup(context.Background(), "sid"); err == nil || ok || card != nil {
		t.Fatalf("Lookup against unreachable Redis = (%v, %v, %v), want an error", card, ok, err)
	}
	if card, ok := store.Get(context.Background(), "sid"); ok || card != nil {
		t.Fatalf("Get against unreachable Redis = (%v, %v), want a miss", card, ok)
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

// TestDeleteUnlessMovedKeepsAnotherNodesCard pins the delete a node runs on
// its own teardown: a card naming another node by row and URL stays readable,
// the node's own card is removed with its sidecars, a card it cannot tell
// apart from its own is removed, and a missing record is not an error.
func TestDeleteUnlessMovedKeepsAnotherNodesCard(t *testing.T) {
	var nilStore *Store
	if kept, err := nilStore.DeleteUnlessMoved(context.Background(), "sid", playback.RecipeExecutor{NodeID: 1}); kept || err != nil {
		t.Fatalf("nil store DeleteUnlessMoved = %v, %v", kept, err)
	}
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
	store := NewStore(client, time.Hour)
	unique := uuid.NewString()
	oldNode := playback.RecipeExecutor{NodeID: 7, NodeURL: "http://old-" + unique}
	newNode := playback.RecipeExecutor{NodeID: 8, NodeURL: "http://new-" + unique}
	moved := "moved-" + unique
	urlOnly := "url-" + unique
	t.Cleanup(func() {
		_ = client.Del(context.WithoutCancel(t.Context()), store.key(moved), store.key(urlOnly),
			nodeAuthorityRecordGenerationKey(moved), nodeAuthorityRecordDigestKey(moved)).Err()
	})

	if err := store.Put(t.Context(), moved, playback.RecipeCard{SessionID: moved, InputPath: "/media/a.mkv", TranscodeNodeURL: newNode.NodeURL, RoutingExecutionNodeID: newNode.NodeID}); err != nil {
		t.Fatal(err)
	}
	if kept, err := store.DeleteUnlessMoved(t.Context(), moved, oldNode); !kept || err != nil {
		t.Fatalf("old node DeleteUnlessMoved = %v, %v; want the moved card kept", kept, err)
	}
	if got, ok := store.Get(t.Context(), moved); !ok || got.TranscodeNodeURL != newNode.NodeURL {
		t.Fatalf("stored card = %+v (present %v), want the moved card readable", got, ok)
	}
	if kept, err := store.DeleteUnlessMoved(t.Context(), moved, newNode); kept || err != nil {
		t.Fatalf("owner DeleteUnlessMoved = %v, %v", kept, err)
	}
	if n, err := client.Exists(t.Context(), store.recordKeys(moved)...).Result(); err != nil || n != 0 {
		t.Fatalf("%d record keys left after the owner deleted the card (err %v)", n, err)
	}
	if kept, err := store.DeleteUnlessMoved(t.Context(), moved, newNode); kept || err != nil {
		t.Fatalf("DeleteUnlessMoved of a missing record = %v, %v", kept, err)
	}

	// Without a row id the card could be the node's own under an edited URL.
	if err := store.Put(t.Context(), urlOnly, playback.RecipeCard{SessionID: urlOnly, InputPath: "/media/b.mkv", TranscodeNodeURL: newNode.NodeURL}); err != nil {
		t.Fatal(err)
	}
	if kept, err := store.DeleteUnlessMoved(t.Context(), urlOnly, oldNode); kept || err != nil {
		t.Fatalf("URL-only DeleteUnlessMoved = %v, %v; want it deleted", kept, err)
	}
	if _, ok := store.Get(t.Context(), urlOnly); ok {
		t.Fatal("a URL-only card survived a delete")
	}
}

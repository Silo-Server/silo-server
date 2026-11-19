package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// relayStreamDeny is a deny marker store over a miniredis.
func relayStreamDeny(t *testing.T) *playback.StreamDeny {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return playback.NewStreamDeny(rdb)
}

// countingNode is a live transcode node that answers every request with
// status and counts what reaches it.
func countingNode(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("node answer"))
	}))
	t.Cleanup(node.Close)
	return node, &hits
}

// A client whose URLs carry a token keeps presenting the token minted for the
// node its transcode left. The move stores a card naming the new node, so the
// new node can rebuild the transport after its own restart once it resolves
// recipes through the core, which binds a token to the node it names.
func TestRelayMoveOfATokenSessionStoresTheCardForItsNewNode(t *testing.T) {
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")
	store := &sharedRecipeStore{cards: map[string]playback.RecipeCard{}}
	f.handler.NodeRecipeStore = store

	if rr := f.segment("seg_00003.ts"); rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00003.ts" {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	waitForStoredRecipeNode(t, store, healthy.server.URL, 2)
	card, _ := store.Get(t.Context(), rehomeTestTransport)
	if card.SessionID != f.session.ID || card.TranscodeTransportID != rehomeTestTransport || card.InputPath != "/media/movie.mkv" || card.TargetCodecVideo != "h264" {
		t.Fatalf("stored card = %+v, want the token's recipe", card)
	}
}

// The relays, not the nodes, enforce the deny marker: a denied playback
// session or transport gets 410 although its node runtime is alive (its stop
// failed), and the node is never asked.
func TestRelayRefusesADeniedSessionWithoutAskingTheNode(t *testing.T) {
	node, hits := countingNode(t, http.StatusOK)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, node.URL)}, "ffmpeg")
	deny := relayStreamDeny(t)
	f.handler.StreamDeny = deny

	if rr := f.segment("seg_00001.ts"); rr.Code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("live session = %d (node hits %d), want it relayed", rr.Code, hits.Load())
	}
	for _, denied := range []string{rehomeTestTransport, f.session.ID} {
		deny.Deny(t.Context(), denied)
		segment := f.segment("seg_00002.ts")
		manifest := httptest.NewRecorder()
		f.handler.HandleGetTranscodeManifest(manifest, playbackTestRequest(http.MethodGet,
			"/api/v2/playback/transcode/"+f.session.ID+"/master.m3u8?st="+f.token, nil, map[string]string{"session_id": f.session.ID}))
		if segment.Code != http.StatusGone || manifest.Code != http.StatusGone {
			t.Fatalf("denied %q: segment %d, manifest %d, want 410", denied, segment.Code, manifest.Code)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("node hits = %d, want 1: a relay forwarded a denied session", got)
	}
}

// A stop can land while a move starts the transport on another node. The
// move then commits nothing, stops what it started, and the client gets 410.
func TestRelayMoveRefusesASessionStoppedWhileItRan(t *testing.T) {
	gated := newFakeRehomeNode(t, true)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, gated.server.URL),
	}, "ffmpeg")
	deny := relayStreamDeny(t)
	f.handler.StreamDeny = deny

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.segment("seg_00003.ts") }()
	deadline := time.Now().Add(10 * time.Second)
	for gated.starts.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the move never started the transport on the other node")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls observable state: the gated node's start counter

	}
	deny.Deny(t.Context(), f.session.ID)
	close(gated.release)

	rr := <-done
	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d %q, want 410", rr.Code, rr.Body.String())
	}
	session, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.TranscodeNodeURL != f.deadURL {
		t.Fatalf("session moved to %q after its stop", session.TranscodeNodeURL)
	}
	if gated.stops.Load() == 0 {
		t.Fatal("the job the move started was not stopped")
	}
}

// A node's 401 is about this server's signature or the token it forwarded,
// never the client's credentials, which the relay checked: the client gets
// the relay's node-unavailable answer, not a 401 it would read as signed out.
func TestRelayAnswersANodeRefusalAsUnavailable(t *testing.T) {
	node, hits := countingNode(t, http.StatusUnauthorized)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, node.URL)}, "ffmpeg")

	rr := f.segment("seg_00001.ts")
	if rr.Code != http.StatusBadGateway || strings.Contains(rr.Body.String(), "node answer") || hits.Load() != 1 {
		t.Fatalf("node 401 = %d %q (hits %d), want the relay's 502", rr.Code, rr.Body.String(), hits.Load())
	}
}

// unsignableKeyring signs for every node but one, which has no key.
type unsignableKeyring struct {
	keyless string
	nodeauth.Keyring
}

func (k unsignableKeyring) KeyFor(target nodeauth.Target, nodeURL string) (string, error) {
	if nodeURL == k.keyless {
		return "", nodeauth.ErrNoNodeKey
	}
	return k.Keyring.KeyFor(target, nodeURL)
}

// A node this server cannot sign for (no key, or no longer registered at the
// URL) is unavailable to it: the relay answers 502, not 500, and moves
// nothing. A replica whose key registry is empty or stale signs for no node,
// so a move would restart a healthy transcode on every hop.
func TestRelayDoesNotMoveOffANodeItCannotSignFor(t *testing.T) {
	keyless, keylessHits := countingNode(t, http.StatusOK)
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, keyless.URL),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")
	f.handler.NodeKeys = unsignableKeyring{keyless: keyless.URL, Keyring: nodeauthtest.SameKey("rehome-secret")}

	if rr := f.segment("seg_00003.ts"); rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %q, want the relay's 502", rr.Code, rr.Body.String())
	}
	if keylessHits.Load() != 0 || healthy.starts.Load() != 0 {
		t.Fatalf("keyless node hits %d, starts elsewhere %d; want neither", keylessHits.Load(), healthy.starts.Load())
	}
	if session, err := f.sessions.GetSession(f.session.ID); err != nil || session.TranscodeNodeURL != keyless.URL {
		t.Fatalf("session route = %+v, %v; want it left on its node", session, err)
	}
	if !f.planner.TranscodeNodeHealthy(keyless.URL) {
		t.Fatal("a signing failure marked the node unreachable")
	}
}

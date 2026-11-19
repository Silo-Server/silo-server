package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// pointRecipeAt makes nodeURL the node the play's transcode runs on.
func (f *compatMoveFixture) pointRecipeAt(t *testing.T, nodeURL string) {
	t.Helper()
	if err := f.store.Update("play-1", func(current *PlaybackSession) error {
		current.Recipe.TranscodeNodeURL = nodeURL
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The relays, not the nodes, enforce the deny marker: a denied play gets 410
// although its node runtime is alive (its stop failed), and the node is never
// asked.
func TestCompatRelayRefusesADeniedSessionWithoutAskingTheNode(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	f.pointRecipeAt(t, f.liveURL)
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	f.handler.StreamDeny = playback.NewStreamDeny(rdb)

	if recorder := f.requestSegment(t); recorder.Code != http.StatusOK || len(f.segmentHit) != 1 {
		t.Fatalf("live play = %d (node hits %d), want it relayed", recorder.Code, len(f.segmentHit))
	}
	f.handler.StreamDeny.Deny(t.Context(), "upstream-1")
	if recorder := f.requestSegment(t); recorder.Code != http.StatusGone {
		t.Fatalf("denied play = %d %q, want 410", recorder.Code, recorder.Body.String())
	}
	if got := len(f.segmentHit); got != 1 {
		t.Fatalf("node hits = %d, want 1: the relay forwarded a denied play", got)
	}
}

// A node's 401 refuses the relay's signature, never the client's login: the
// client gets the relay's node-unavailable answer, and a live node's answer
// moves nothing.
func TestCompatSegmentRelayAnswersANodeRefusalAsUnavailable(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	var hits atomic.Int32
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "node says unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(refusing.Close)
	f.pointRecipeAt(t, refusing.URL)

	recorder := f.requestSegment(t)
	if recorder.Code != http.StatusBadGateway || hits.Load() != 1 {
		t.Fatalf("node 401 = %d %q (hits %d), want 502", recorder.Code, recorder.Body.String(), hits.Load())
	}
	if ops := f.recipes.Operations(); len(ops) != 0 {
		t.Fatalf("a refusing node moved the transcode: %v", ops)
	}
}

// keylessAt has no key for the node at one URL.
type keylessAt struct {
	url string
	nodeauth.Keyring
}

func (k keylessAt) KeyFor(target nodeauth.Target, nodeURL string) (string, error) {
	if nodeURL == k.url {
		return "", nodeauth.ErrNoNodeKey
	}
	return k.Keyring.KeyFor(target, nodeURL)
}

// A node this server cannot sign for is unavailable to it: the relay answers
// 502 and moves nothing. A replica whose key registry is empty or stale signs
// for no node, so a move would restart a healthy transcode on every hop.
func TestCompatSegmentRelayDoesNotMoveOffANodeItCannotSignFor(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	f.pointRecipeAt(t, f.liveURL)
	f.handler.NodeKeys = keylessAt{url: f.liveURL, Keyring: nodeauthtest.SameKey("test-secret")}

	recorder := f.requestSegment(t)
	if recorder.Code != http.StatusBadGateway || len(f.segmentHit) != 0 {
		t.Fatalf("segment = %d %q (node hits %d), want 502 without reaching the node", recorder.Code, recorder.Body.String(), len(f.segmentHit))
	}
	if ops := f.recipes.Operations(); len(ops) != 0 {
		t.Fatalf("a signing failure moved the transcode: %v", ops)
	}
	if playSession := f.playSession(t); playSession.Recipe.TranscodeNodeURL != f.liveURL {
		t.Fatalf("play moved to %q", playSession.Recipe.TranscodeNodeURL)
	}
}

package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// keylessKeyring has no key for any transcode node.
type keylessKeyring struct{}

func (keylessKeyring) KeyFor(nodeauth.Target, string) (string, error) {
	return "", nodeauth.ErrNoNodeKey
}

// The proxy verified the client's token before it signed anything to the
// node, so a node's 401 refuses the proxy, not the client: it reaches the
// client as 502, the proxy's node-unavailable answer, never as a 401.
func TestRelayAnswersANodeRefusalAsUnavailable(t *testing.T) {
	var hits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "node says unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(node.Close)
	token := signToken(t, streamtoken.Claims{
		SessionID: "s-1", PlayMethod: string(playback.PlayTranscode), TranscodeNode: node.URL, TranscodeTransportID: "s-1-plan",
	}, grantTestSecret, time.Minute)

	rr := grantRequest(t, newGrantProxyServer(t, nil), http.MethodGet, "/stream/transcode/"+token+"/master.m3u8", "")
	if rr.Code != http.StatusBadGateway || strings.Contains(rr.Body.String(), "node says") || hits.Load() != 1 {
		t.Fatalf("node 401 = %d %q (hits %d), want 502", rr.Code, rr.Body.String(), hits.Load())
	}
}

// A node this proxy cannot sign for is unavailable to it: 502, not 500, and
// no move request, even for a store-backed transport whose move the proxy
// follows. A proxy with an empty or stale key registry signs for no node, so
// a move would restart a healthy transcode on every hop.
func TestRelayDoesNotMoveOffANodeItCannotSignFor(t *testing.T) {
	var hits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(node.Close)
	store := &fakeMoveStore{
		cards:   map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: node.URL, SegmentDuration: 2, TargetCodecVideo: "h264"}},
		movedTo: "http://other-node.invalid",
	}
	server := newMoveTestProxy(t, store)
	server.SetNodeKeys(keylessKeyring{})

	rr := relaySegment(server, &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: node.URL})
	if rr.Code != http.StatusBadGateway || hits.Load() != 0 {
		t.Fatalf("keyless node = %d %q (hits %d), want 502 without reaching it", rr.Code, rr.Body.String(), hits.Load())
	}
	if requests := store.moveRequests(); len(requests) != 0 {
		t.Fatalf("move requests = %+v, want none for a signing failure", requests)
	}
}

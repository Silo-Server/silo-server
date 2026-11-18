package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// keylessKeyring has no key for any transcode node.
type keylessKeyring struct{}

func (keylessKeyring) KeyFor(nodeauth.Target, string) (string, error) {
	return "", nodeauth.ErrNoNodeKey
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

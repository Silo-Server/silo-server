package playback_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// recordingNode is a transcode node that records the stops it receives and
// whether the transport's stored recipe was still there when each arrived.
type recordingNode struct {
	*httptest.Server
	mu    sync.Mutex
	stops []string
}

func newRecordingNode(t *testing.T, store *noderecipe.Store, transportID string) *recordingNode {
	t.Helper()
	n := &recordingNode{}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			_, stillStored := store.Get(context.Background(), transportID)
			stop := r.URL.String()
			if stillStored {
				stop += " with recipe"
			}
			n.mu.Lock()
			n.stops = append(n.stops, stop)
			n.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(n.Close)
	return n
}

func (n *recordingNode) received() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.stops...)
}

func recipeStopFixture(t *testing.T) (*playback.TranscodeManager, *noderecipe.Store, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	store := noderecipe.NewStore(rdb, time.Hour)
	m := playback.NewTranscodeManager()
	m.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.NodeKey("snk1_1_stop-key") }
	m.NodeRecipesFn = func() playback.NodeRecipeDeleter { return store }
	return m, store, server
}

func compatCard(nodeURL string, executionNodeID int) playback.RecipeCard {
	card := playback.NewRecipeCard(7, "profile-1", 42, nodeURL, playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 6,
	})
	card.RoutingExecutionNodeID = executionNodeID
	return card
}

// jellycompat runs every start candidate under the upstream session id. A
// stop of candidate A that lands after candidate B wrote its card, as the
// background stop after a connect failure can, leaves B's card alone: B's
// card names B's URL, whatever row id it carries (none, or the previous
// executor's from a routing assignment recorded after the start). A stop of
// B itself removes the card before B is told to stop.
func TestStopOnOneCandidateKeepsTheNextCandidatesRecipe(t *testing.T) {
	m, store, _ := recipeStopFixture(t)
	const transport = "upstream-1"
	a := newRecordingNode(t, store, transport)
	b := newRecordingNode(t, store, transport)

	for name, card := range map[string]playback.RecipeCard{
		"card without a row id":    compatCard(b.URL, 0),
		"card with a stale row id": compatCard(b.URL, 3),
	} {
		if err := store.Put(t.Context(), transport, card); err != nil {
			t.Fatal(err)
		}
		m.StopRemoteTranscode(transport, a.URL)
		if _, ok := store.Get(t.Context(), transport); !ok {
			t.Fatalf("%s: the stop on candidate A deleted candidate B's card", name)
		}
	}
	if got := a.received(); len(got) != 2 || got[0] != "/transcode/upstream-1 with recipe" {
		t.Fatalf("node A stops = %v, want two DELETEs", got)
	}

	m.StopRemoteTranscode(transport, b.URL)
	if _, ok := store.Get(t.Context(), transport); ok {
		t.Fatal("the stop on node B kept its own card")
	}
	if got := b.received(); len(got) != 1 || got[0] != "/transcode/upstream-1" {
		t.Fatalf("node B stops = %v, want one DELETE after the card was gone", got)
	}
}

// A release moves the transport and deletes nothing: the transport keeps its
// id, and its card is about to name the node it moves to. A recipe store that
// cannot be written does not keep the node from being told to stop.
func TestReleaseKeepsTheRecipeAndAFailedDeleteStillStops(t *testing.T) {
	m, store, server := recipeStopFixture(t)
	const transport = "native-plan"
	node := newRecordingNode(t, store, transport)
	if err := store.Put(t.Context(), transport, compatCard(node.URL, 0)); err != nil {
		t.Fatal(err)
	}

	if err := m.ReleaseRemoteTranscode(t.Context(), transport, node.URL, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(t.Context(), transport); !ok {
		t.Fatal("a release deleted the transport's recipe")
	}

	server.Close()
	m.StopRemoteTranscode(transport, node.URL)
	if got := node.received(); len(got) != 2 || got[0] != "/transcode/native-plan?fence=0 with recipe" || got[1] != "/transcode/native-plan" {
		t.Fatalf("node stops = %v, want the release and then the stop despite the store failure", got)
	}
}

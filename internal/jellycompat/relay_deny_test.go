package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// unreachableStreamDeny returns a marker store whose Redis never answers, so
// Deny populates only the in-process cache and every other lookup fails open.
func unreachableStreamDeny(t *testing.T) *playback.StreamDeny {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return playback.NewStreamDeny(client)
}

// The transcode node checks no deny marker, so the Jellyfin relays to it must:
// a stopped or terminated session whose node runtime is still alive (its stop
// DELETE failed) gets 410 and the node is never asked.
func TestRemoteCompatRelaysRefuseADeniedSession(t *testing.T) {
	const secret = "compat-deny-secret"
	var nodeHits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeHits.Add(1)
		if strings.HasSuffix(r.URL.Path, "/master.m3u8") {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nsegment/seg_00000.ts\n"))
			return
		}
		_, _ = w.Write([]byte("segment bytes"))
	}))
	t.Cleanup(node.Close)

	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	recipe := playback.NewRecipeCard(7, "profile-1", source.FileID, node.URL, playback.TranscodeOpts{SessionID: "upstream-1"})
	store := NewPlaybackSessionStore(0, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1", UpstreamSessionID: "upstream-1",
		UpstreamPlayMethod: "transcode", MediaSources: []PlaybackMediaSource{source}, Recipe: &recipe,
		RoutingAssignment: &playback.NodeRoutingAssignment{
			Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionTranscode),
			ExecutionNodeURL: node.URL, Egress: string(noderouting.EgressAPI),
		},
	})
	deny := unreachableStreamDeny(t)
	handler := &PlaybackHandler{playbackStore: store, NodeKeys: nodeauthtest.SharedSecret(secret), StreamDeny: deny}

	relays := []struct {
		name    string
		segment bool
	}{{name: "manifest"}, {name: "segment", segment: true}}
	serve := func(segment bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/Videos/item-1/hls/play-1/stream.m3u8", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", "item-1")
		routeCtx.URLParams.Add("playlistId", "play-1")
		if segment {
			routeCtx.URLParams.Add("segmentId", "0")
			routeCtx.URLParams.Add("segmentContainer", "ts")
		}
		ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7})
		recorder := httptest.NewRecorder()
		if segment {
			handler.HandleHLSSegment(recorder, request.WithContext(ctx))
		} else {
			handler.HandleHLSManifest(recorder, request.WithContext(ctx))
		}
		return recorder
	}

	// The relays and the node runtime work: an undenied session is served.
	for _, relay := range relays {
		if rr := serve(relay.segment); rr.Code != http.StatusOK {
			t.Fatalf("%s before the deny = %d %s", relay.name, rr.Code, rr.Body.String())
		}
	}
	served := nodeHits.Load()
	if served != int32(len(relays)) {
		t.Fatalf("node hits = %d, want one per relay", served)
	}

	deny.Deny(t.Context(), "upstream-1")
	for _, relay := range relays {
		rr := serve(relay.segment)
		if rr.Code != http.StatusGone || !strings.Contains(rr.Body.String(), "PlaybackSessionEnded") {
			t.Fatalf("%s of a denied session = %d %s, want 410", relay.name, rr.Code, rr.Body.String())
		}
	}
	if got := nodeHits.Load(); got != served {
		t.Fatalf("node hits = %d after the deny, want %d: a relay forwarded a denied session", got, served)
	}
}

// The master manifest relay starts the node job it then forwards to, so its
// deny check comes first: a denied session neither reserves node capacity nor
// starts a job it would then abandon.
func TestCompatMasterManifestRefusesADeniedSessionBeforeStartingANode(t *testing.T) {
	var nodeHits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeHits.Add(1)
		if r.URL.Path != "/transcode/start" {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nsegment/seg_00000.ts\n"))
			return
		}
		var request transcodenode.TranscodeStartRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started"})
	}))
	t.Cleanup(node.Close)
	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	transcodes := nodepooltest.NewTranscodePool()
	maxJobs := 1
	transcodes.SetNodes([]*nodepool.Node{{ID: 5, URL: node.URL, Enabled: true, Healthy: true, MaxJobs: &maxJobs}})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	handler.NodePlanner = planner
	requireCompatWorkerRouting(handler)
	deny := unreachableStreamDeny(t)
	handler.StreamDeny = deny
	source := testRemoteTranscodeSource()
	playbackStore.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item",
		MediaSources: []PlaybackMediaSource{source}, UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
	})
	deny.Deny(t.Context(), "upstream-1")

	request := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}))
	recorder := httptest.NewRecorder()
	handler.HandleMasterManifest(recorder, request)

	if recorder.Code != http.StatusGone || !strings.Contains(recorder.Body.String(), "PlaybackSessionEnded") {
		t.Fatalf("master of a denied session = %d %s, want 410", recorder.Code, recorder.Body.String())
	}
	if got := nodeHits.Load(); got != 0 {
		t.Fatalf("node hits = %d: the master relay started or asked a node for a denied session", got)
	}
	// No reservation is left behind: the one-job node still admits a session.
	if plan := planner.PlanRoute(nodepool.RouteRequest{SessionID: "capacity-probe", NeedsTranscode: true}); plan.TranscodeNode == nil {
		t.Fatal("refused master left a node reservation behind")
	}
}

// A Jellyfin transcode's remote stop deletes the recipe a node would rebuild
// it from before it tells the node, as the native stop does.
func TestCompatRemoteStopDeletesTheNodeRecipe(t *testing.T) {
	var deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)
	handler := NewPlaybackHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	handler.NodeKeys = nodeauthtest.SharedSecret("compat-stop-secret")
	store := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1"}}}
	handler.RecipeNodeStore = store

	handler.tm.StopRemoteTranscode("upstream-1", node.URL)
	if ops := store.Operations(); len(ops) != 1 || ops[0] != "delete:upstream-1" {
		t.Fatalf("recipe operations = %v, want the recipe deleted", ops)
	}
	if deletes.Load() != 1 {
		t.Fatalf("node DELETEs = %d, want 1", deletes.Load())
	}
}

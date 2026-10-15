package transcodenode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// testCore is the core a test node resolves through: the real resolve
// endpoint, in process, behind real node authentication, as the node row id
// the server reports. Its recipes and deny markers are the test's.
type testCore struct {
	t      *testing.T
	server *Server

	mu      sync.Mutex
	recipes nodecontrol.RecipeSource
	deny    nodecontrol.DenySource
	clients map[int]*nodecontrol.Client
	calls   int
}

func newTestCore(t *testing.T, server *Server) *testCore {
	return &testCore{t: t, server: server, clients: make(map[int]*nodecontrol.Client)}
}

func (c *testCore) Resolve(ctx context.Context, req nodecontrol.ResolveRequest) (*nodecontrol.Resolution, error) {
	nodeID := 1
	if c.server.nodeRowID != nil {
		if id, ok := c.server.nodeRowID(); ok && id > 0 {
			nodeID = id
		}
	}
	c.mu.Lock()
	c.calls++
	client := c.clients[nodeID]
	if client == nil {
		client = nodecontroltest.Resolver(c.t, nodeID, nodecontrol.Options{
			Nodes:        c,
			StreamSecret: func() string { return testSecret },
			Recipes:      c,
			Deny:         c,
		})
		c.clients[nodeID] = client
	}
	c.mu.Unlock()
	return client.Resolve(ctx, req)
}

func (c *testCore) resolveCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// coreNodeURL is the URL every test core has the calling node registered
// under: the URL a test's HLS recipe card names when the planner's node id
// does not bind it.
const coreNodeURL = "http://node"

func (c *testCore) GetByID(ctx context.Context, id int) (*nodepool.Node, error) {
	identity, err := c.GetIdentity(ctx, id)
	if err != nil {
		return nil, err
	}
	return &nodepool.Node{ID: identity.ID, Type: identity.Type, URL: identity.URL}, nil
}

func (c *testCore) GetIdentity(_ context.Context, id int) (nodepool.Identity, error) {
	return nodepool.Identity{ID: id, Type: "transcode", URL: coreNodeURL}, nil
}

// Lookup reads the test's recipe store at call time, so a test may set it
// after the core first answered.
func (c *testCore) Lookup(ctx context.Context, transportID string) (*playback.RecipeCard, bool, error) {
	c.mu.Lock()
	recipes := c.recipes
	c.mu.Unlock()
	if recipes == nil {
		return nil, false, nil
	}
	return recipes.Lookup(ctx, transportID)
}

func (c *testCore) Denied(ctx context.Context, sessionID string) bool {
	c.mu.Lock()
	deny := c.deny
	c.mu.Unlock()
	return deny != nil && deny.Denied(ctx, sessionID)
}

// core is the test core s resolves through.
func (s *Server) core() *testCore {
	core, ok := s.resolver.(*testCore)
	if !ok {
		panic("server does not resolve through a test core")
	}
	return core
}

// useCoreRecipes gives the server's core the recipe store it resolves from.
func (s *Server) useCoreRecipes(recipes nodecontrol.RecipeSource) {
	core := s.core()
	core.mu.Lock()
	core.recipes = recipes
	core.mu.Unlock()
}

// useCoreDeny gives the server's core the deny markers it checks.
func (s *Server) useCoreDeny(deny nodecontrol.DenySource) {
	core := s.core()
	core.mu.Lock()
	core.deny = deny
	core.mu.Unlock()
}

// resolutionOf is the resolution the core answers for card, its source
// granted as nodecontroltest.PathGrants grants it.
func resolutionOf(card playback.RecipeCard) *nodecontrol.Resolution {
	playbackSessionID := card.SessionID
	if playbackSessionID == "" {
		playbackSessionID = card.TranscodeTransportID
	}
	return &nodecontrol.Resolution{Recipe: card, SourceGrant: card.InputPath, PlaybackSessionID: playbackSessionID,
		UserID: card.UserID, ProfileID: card.ProfileID, MediaFileID: card.MediaFileID}
}

// refusingResolver answers every resolve with one refusal or failure.
type refusingResolver struct{ err error }

func (r refusingResolver) Resolve(context.Context, nodecontrol.ResolveRequest) (*nodecontrol.Resolution, error) {
	return nil, r.err
}

func refusal(status int, reason string) error {
	return &nodecontrol.StatusError{Path: nodecontrol.PathResolve, Status: status, Reason: reason}
}

func routeRequest(method, target string, params map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	rctx := chi.NewRouteContext()
	for name, value := range params {
		rctx.URLParams.Add(name, value)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// Every refusal the core can give a progressive remux reaches the relay as
// the answer the node gave for the same condition when it checked the token
// itself; a core it cannot reach is a retryable 503.
func TestRemuxMapsResolveRefusalsToItsPreviousAnswers(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"invalid token", refusal(http.StatusForbidden, nodecontrol.ResolveInvalidToken), http.StatusUnauthorized, "invalid stream token"},
		{"another node's route", refusal(http.StatusForbidden, nodecontrol.ResolveWrongRoute), http.StatusServiceUnavailable, "routing policy unsatisfied"},
		{"denied", refusal(http.StatusGone, nodecontrol.ResolveDenied), http.StatusGone, "playback session ended"},
		{"authority withdrawn", refusal(http.StatusGone, nodecontrol.ResolveStopped), http.StatusGone, "session stopped"},
		{"source changed", refusal(http.StatusGone, nodecontrol.ResolveSourceChanged), http.StatusBadRequest, "remux input is no longer the approved source"},
		{"bad shape", refusal(http.StatusNotFound, nodecontrol.ResolveNotFound), http.StatusNotFound, "404 page not found"},
		{"core failing", refusal(http.StatusServiceUnavailable, ""), http.StatusServiceUnavailable, "recipe authority unavailable"},
		{"core unreachable", context.DeadlineExceeded, http.StatusServiceUnavailable, "recipe authority unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			server.SetRecipeResolver(refusingResolver{err: test.err})
			rec := httptest.NewRecorder()
			request := routeRequest(http.MethodGet, "/remux/transport-1", map[string]string{"session_id": "transport-1"})
			request.Header.Set(streamTokenHeader, "opaque-token")
			server.handleRemux(rec, request)
			if rec.Code != test.status || strings.TrimSpace(rec.Body.String()) != test.body {
				t.Fatalf("remux = %d %q, want %d %q", rec.Code, rec.Body.String(), test.status, test.body)
			}
		})
	}
}

// A lost HLS session the core reports denied is gone, and one it refuses
// otherwise is not found, as a recipe store miss was. A core the node cannot
// get an answer from is a retryable 503: the session may well exist.
func TestReconstructMapsResolveRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"denied", refusal(http.StatusGone, nodecontrol.ResolveDenied), http.StatusGone},
		{"no recipe", refusal(http.StatusNotFound, nodecontrol.ResolveNotFound), http.StatusNotFound},
		{"another node's recipe", refusal(http.StatusForbidden, nodecontrol.ResolveWrongRoute), http.StatusNotFound},
		{"core failing", refusal(http.StatusServiceUnavailable, ""), http.StatusServiceUnavailable},
		{"core unreachable", context.DeadlineExceeded, http.StatusServiceUnavailable},
	} {
		for _, path := range []string{"master.m3u8", "segment/seg_00001.m4s"} {
			t.Run(test.name+" "+path, func(t *testing.T) {
				server := newTestServer(t)
				server.SetRecipeResolver(refusingResolver{err: test.err})
				rr := nodeRequest(t, server, http.MethodGet, "/transcode/lost-transport/"+path, "")
				if rr.Code != test.status {
					t.Fatalf("%s = %d %q, want %d", path, rr.Code, rr.Body.String(), test.status)
				}
			})
		}
	}
}

// A live session is served from memory without asking the core: the relays
// check the deny marker before they forward.
func TestLiveSessionServesWithoutResolving(t *testing.T) {
	server := newTestServer(t)
	server.mu.Lock()
	server.registerSessionLocked("live-transport", playback.NewTranscodeSessionForTest(t.TempDir()), sessionIdentity{}, nil)
	server.mu.Unlock()
	rr := nodeRequest(t, server, http.MethodGet, "/transcode/live-transport/master.m3u8", "")
	if rr.Code == http.StatusGone || rr.Code == http.StatusNotFound {
		t.Fatalf("live manifest = %d %q", rr.Code, rr.Body.String())
	}
	if calls := server.core().resolveCalls(); calls != 0 {
		t.Fatalf("resolved %d times for a live session", calls)
	}
}

// A session the node rebuilds is the same playback session, so its record
// keeps the start the core resolved rather than the moment of the rebuild, with
// full precision, as a remux request's record does.
func TestRebuiltSessionRecordKeepsTheResolvedStart(t *testing.T) {
	server := newTestServer(t)
	tracker := nodesessions.NewLocalTracker(nil)
	server.tracker = tracker
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	for _, test := range []struct {
		id         string
		startedAt  time.Time
		source     streamtoken.StartedAtSource
		wantSource string
	}{
		{id: "rebuilt-claim", startedAt: time.Unix(1_700_000_000, 123_456_789).UTC(), source: streamtoken.StartedAtSourceClaim, wantSource: "claim"},
		{id: "rebuilt-issued", startedAt: time.Unix(1_700_000_100, 0).UTC(), source: streamtoken.StartedAtSourceIssuedAt, wantSource: "issued_at"},
		{id: "rebuilt-unknown", source: streamtoken.StartedAtSourceNone, wantSource: "none"},
	} {
		t.Run(test.id, func(t *testing.T) {
			resolution := resolutionOf(*nativeTransportCard("viewer", test.id))
			resolution.StartedAt, resolution.StartedAtSource = test.startedAt, string(test.source)
			before := time.Now()
			if _, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), test.id, -1, resolution); err != nil {
				t.Fatalf("spawnReconstruct: %v", err)
			}
			var record *nodesessions.SessionInfo
			for _, candidate := range tracker.Snapshot() {
				if candidate.SessionID == test.id {
					record = &candidate
				}
			}
			if record == nil {
				t.Fatal("the rebuilt session was not tracked")
			}
			want := test.startedAt
			if want.IsZero() {
				// Nothing resolved: the rebuild is the best start there is.
				if record.StartedAtUnixNano < before.UnixNano() {
					t.Fatalf("record start = %d, want the rebuild time", record.StartedAtUnixNano)
				}
				want = time.Unix(0, record.StartedAtUnixNano).UTC()
			}
			if record.StartedAtUnixNano != want.UnixNano() || record.StartedAt != want.Format(time.RFC3339) || record.StartedAtSource != test.wantSource {
				t.Fatalf("record start = %q %d %q, want %s from %q", record.StartedAt, record.StartedAtUnixNano, record.StartedAtSource, want, test.wantSource)
			}
		})
	}
}

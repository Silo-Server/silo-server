package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

// directStateV3 is a DirectState that counts its Device calls, and its other
// calls apart from them.
type directStateV3 struct {
	mu         sync.Mutex
	device     directconn.DeviceDirect
	enrolled   bool
	generation uint64
	// engineRunning is what PathObservation reports for this replica's engine.
	engineRunning bool
	calls         int
	// observations counts PathObservation calls; otherCalls counts every
	// PathGeneration and Enrolled call.
	observations, otherCalls int
	// err, when set, is what every Device call returns.
	err error
}

func (s *directStateV3) Device(context.Context, int, string) (directconn.DeviceDirect, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return directconn.DeviceDirect{}, false, s.err
	}
	return s.device, s.enrolled, nil
}

func (s *directStateV3) PathGeneration(int, string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.otherCalls++
	return s.generation
}

func (s *directStateV3) PathObservation(int, string) (bool, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations++
	return s.engineRunning, s.generation
}

func (s *directStateV3) Enrolled(context.Context, int, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.otherCalls++
	return s.enrolled, nil
}

// directCalls returns the PathObservation count and the count of every other
// direct-state call: Device, PathGeneration and Enrolled.
func (s *directStateV3) directCalls() (observations, others int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observations, s.otherCalls + s.calls
}

func (s *directStateV3) deviceCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// directRouteV3 is one route selection: how many Device calls had happened
// when it began and when it ended.
type directRouteV3 struct {
	atEntry, atExit int
	needsProxy      bool
}

// directPlannerV3 selects the first proxy a request's predicate admits, as the
// pool does, and records each selection against the direct state's call count.
// proxyLostAfter, when positive, makes every proxy selection after that many
// of them find no proxy with capacity.
type directPlannerV3 struct {
	state          *directStateV3
	proxies        []*nodepool.Node
	proxyLostAfter int
	// onProxyPlan, when set, runs at the start of each proxy selection with
	// the count of proxy selections so far.
	onProxyPlan func(n int)

	mu         sync.Mutex
	routes     []directRouteV3
	proxyPlans int
}

func (*directPlannerV3) PlanSession(string, string, bool, int) nodepool.Plan { return nodepool.Plan{} }

func (p *directPlannerV3) PlanRoute(request nodepool.RouteRequest) nodepool.Plan {
	route := directRouteV3{atEntry: p.state.deviceCalls(), needsProxy: request.NeedsProxy}
	plan := nodepool.Plan{}
	p.mu.Lock()
	lost := false
	n := 0
	if request.NeedsProxy {
		p.proxyPlans++
		n = p.proxyPlans
		lost = p.proxyLostAfter > 0 && p.proxyPlans > p.proxyLostAfter
	}
	p.mu.Unlock()
	if n > 0 && p.onProxyPlan != nil {
		p.onProxyPlan(n)
	}
	if request.NeedsProxy && !lost {
		for _, node := range p.proxies {
			if request.ProxyEligible == nil || request.ProxyEligible(node) {
				plan.ProxyNode = node
				break
			}
		}
	}
	route.atExit = p.state.deviceCalls()
	p.mu.Lock()
	p.routes = append(p.routes, route)
	p.mu.Unlock()
	return plan
}

func (*directPlannerV3) ReleaseSession(string) {}

func directProxiesV3(n int) []*nodepool.Node {
	nodes := make([]*nodepool.Node, n)
	for i := range nodes {
		nodes[i] = &nodepool.Node{ID: 100 + i, URL: fmt.Sprintf("http://proxy-%d.example", i+1)}
	}
	return nodes
}

// directStartV3 is a native start of a direct-playable file through a handler
// whose pool holds the given proxies.
type directStartV3 struct {
	handler *PlaybackHandler
	planner *directPlannerV3
	state   *directStateV3
	request playback.StartRequestV3
	caller  PlaybackCaller
}

func newDirectStartV3(t *testing.T, state *directStateV3, proxies int) *directStartV3 {
	t.Helper()
	source := v3HandlerFixtureFile(t)
	source.Bitrate, source.VideoTracks[0].Bitrate = 63_000, 63_000
	alternate := lowerAlternateFileV3(source)
	alternate.Bitrate, alternate.VideoTracks[0].Bitrate = 31_600, 31_600
	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.InstallationID = serviceInstallation
	handler.JWTSecret = "direct-start-secret"
	handler.DirectState = state
	planner := &directPlannerV3{state: state, proxies: directProxiesV3(proxies)}
	handler.NodePlanner = planner
	request := v3HandlerStartRequest()
	request.MediaTransport = playback.MediaTransportDirectV3
	request.Capabilities.VideoDecode[0].MaxBitrateKbps = 100_000
	return &directStartV3{
		handler: handler, planner: planner, state: state, request: request,
		caller: PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation, ConnectionDeviceID: "tv-1"},
	}
}

func (s *directStartV3) start(t *testing.T) *playback.PlanV3 {
	t.Helper()
	response, err := s.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), s.caller, s.request)
	if err != nil || response.PlaybackPlan == nil {
		t.Fatalf("start: err=%v response=%#v", err, response)
	}
	return response.PlaybackPlan
}

// urlOriginV3 is a stream URL without its path and token, so a failed check
// never prints a credential.
func urlOriginV3(raw string) string {
	if i := strings.Index(raw, "/stream"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// A direct_only device takes no proxy origin: the plan's media URL is the
// API's own path whatever the pool holds, whether the request asked for direct
// or not and whether the API's path is ready or not. A device whose state
// cannot be read is treated the same way. An automatic device on the same pool
// is served by the proxy, so the check can fail, and so is a device whose state
// answers not enrolled, as it does for every device while direct connections are
// off.
func TestDirectOnlyNeverGetsRegularProxyURL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    string
		apiReady  bool
		transport string
		wantProxy bool
		readErr   bool
		// off makes the state answer ok=false, as the service does while direct
		// connections are off.
		off bool
	}{
		{"automatic device uses the proxy", "automatic", false, playback.MediaTransportDirectV3, true, false, false},
		{"direct_only, API not ready, asks direct", directPolicyDirectOnlyV3, false, playback.MediaTransportDirectV3, false, false, false},
		{"direct_only, API not ready, asks regular", directPolicyDirectOnlyV3, false, playback.MediaTransportRegularV3, false, false, false},
		{"direct_only, API ready, asks direct", directPolicyDirectOnlyV3, true, playback.MediaTransportDirectV3, false, false, false},
		{"state unreadable, asks direct", "automatic", false, playback.MediaTransportDirectV3, false, true, false},
		{"direct connections off, asks direct", directPolicyDirectOnlyV3, true, playback.MediaTransportDirectV3, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &directStateV3{enrolled: !tc.off, device: directconn.DeviceDirect{Policy: tc.policy, APIReady: tc.apiReady}}
			if tc.readErr {
				state.err = errors.New("store unavailable")
			}
			if tc.apiReady {
				state.device.ConnectionKey, state.device.PathGeneration = "direct:path-1", 3
			}
			f := newDirectStartV3(t, state, 1)
			f.request.MediaTransport = tc.transport
			plan := f.start(t)
			onProxy := strings.HasPrefix(plan.Stream.URL, f.planner.proxies[0].URL)
			if onProxy != tc.wantProxy {
				t.Fatalf("stream URL origin %q: on the proxy = %v, want %v", urlOriginV3(plan.Stream.URL), onProxy, tc.wantProxy)
			}
			if !tc.wantProxy && !strings.HasPrefix(plan.Stream.URL, "/") {
				t.Fatalf("stream URL origin %q, want an API-relative path", urlOriginV3(plan.Stream.URL))
			}
		})
	}
}

// A replan reads the device's direct state again. A device that started as
// automatic on the proxy and is direct_only at a quality change, or whose state
// cannot be read then, gets the API's own path, not the proxy origin.
func TestDirectOnlyReplanNeverGetsRegularProxyURL(t *testing.T) {
	for _, tc := range []struct {
		name       string
		directOnly bool
		readErr    bool
	}{
		{"switched to direct_only", true, false},
		{"state unreadable at the replan", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}
			f := newDirectStartV3(t, state, 1)
			// The proxy answers the replan's liveness probe, so only the direct
			// rule can keep the replan off it.
			f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
			started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
			if err != nil || started.PlaybackPlan == nil {
				t.Fatalf("start: err=%v", err)
			}
			if plan := started.PlaybackPlan; !strings.HasPrefix(plan.Stream.URL, f.planner.proxies[0].URL) {
				t.Fatalf("start stream URL origin %q, want the proxy so the replan can change it", urlOriginV3(plan.Stream.URL))
			}
			state.mu.Lock()
			if tc.directOnly {
				state.device.Policy = directPolicyDirectOnlyV3
			}
			if tc.readErr {
				state.err = errors.New("store unavailable")
			}
			state.mu.Unlock()
			plan := started.PlaybackPlan
			request := playback.ReplanRequestV3{
				ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationQualityChangeV3,
				PlaybackAttemptID: f.request.PlaybackAttemptID, ReplanRequestID: "direct-replan-0001",
				FailedPlanID: plan.PlanID, PlanAttemptID: "direct-replan-attempt-0001", PlanAttemptKey: plan.PlanAttemptKey,
				AttemptCount: 1, PositionSeconds: 30, QualityPreference: f.request.QualityPreference, SelectedTracks: plan.SelectedTracks,
				MediaTransport: playback.MediaTransportDirectV3, ClientFeatures: f.request.ClientFeatures, Capabilities: f.request.Capabilities,
				ClientPlaybackContext: f.request.ClientPlaybackContext,
			}
			replanned, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: "direct-replan-digest"})
			if err != nil || replanned.PlaybackPlan == nil {
				t.Fatalf("replan: err=%v", err)
			}
			if got := replanned.PlaybackPlan.Stream.URL; !strings.HasPrefix(got, "/") {
				t.Fatalf("replan stream URL origin %q, want an API-relative path", urlOriginV3(got))
			}
		})
	}
}

// A direct start reads the device's direct state once, before any route is
// selected, and no route selection (which runs under the planner's lock in the
// real pool) calls the state again, across all of the start policy's passes.
func TestDirectStartReadsStoreOnce(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 3)
	plan := f.start(t)
	if got := state.deviceCalls(); got != 1 {
		t.Fatalf("DirectState.Device calls = %d, want 1", got)
	}
	if len(f.planner.routes) == 0 {
		t.Fatal("no route was selected")
	}
	proxyRoutes := 0
	for i, route := range f.planner.routes {
		if route.atEntry != 1 || route.atExit != 1 {
			t.Fatalf("route %d ran with %d Device calls at entry and %d at exit, want 1 and 1", i, route.atEntry, route.atExit)
		}
		if route.needsProxy {
			proxyRoutes++
		}
	}
	// The API's key differs from a proxy's, so the policy ran more than one
	// proxy-bearing route (the dry route and the pinned one) and still read once.
	if proxyRoutes < 2 {
		t.Fatalf("proxy routes = %d, want the start policy's passes to have run", proxyRoutes)
	}
	if plan.MediaTransport != playback.MediaTransportRegularV3 {
		t.Fatalf("media_transport = %q, want regular for a proxy-served plan", plan.MediaTransport)
	}
}

// An enrolled device whose API path is ready, starting over /api/v2 with
// media_transport direct on a pool with no proxy, gets a direct plan: the
// media is the API host's own path and the plan says direct.
func TestDirectStartEnrolledDeviceGetsDirectPlan(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 0)
	plan := f.start(t)
	if plan.MediaTransport != playback.MediaTransportDirectV3 {
		t.Fatalf("media_transport = %q, want direct", plan.MediaTransport)
	}
	if !strings.HasPrefix(plan.Stream.URL, "/") {
		t.Fatalf("stream URL origin %q, want an API-relative path", urlOriginV3(plan.Stream.URL))
	}
}

// directLimitsV3 stores a different learned limit for the device under its
// direct key and under its regular key, and returns the handler of a start
// that plans Auto under whichever the start policy selects.
func directLimitsV3(t *testing.T, proxies int) (f *directStartV3, regularLimit, directLimit int) {
	t.Helper()
	pool := connectionTestPool(t)
	ctx := t.Context()
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("direct-start-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })

	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f = newDirectStartV3(t, state, proxies)
	f.handler.Connections = &connectionstore.Store{Pool: pool}
	f.caller.UserID = user
	f.request.QualityPreference = playback.QualityAutoV3
	f.request.ClientFeatures = append(f.request.ClientFeatures, playback.FeatureBufferingQualityV1)

	regularLimit, directLimit = 40_000, 50_000
	remote := clientip.SetContext(context.Background(), "203.0.113.7")
	regularKey, _ := connectionstore.Key(remote, f.handler.JWTSecret, connectionstore.ClassRegular, "")
	if regularKey == "" || regularKey == state.device.ConnectionKey {
		t.Fatalf("regular key %q is not distinct from the direct key", regularKey)
	}
	for key, kbps := range map[string]int{regularKey: regularLimit, state.device.ConnectionKey: directLimit} {
		if err := f.handler.Connections.Observe(ctx, user, "tv-1", key, kbps); err != nil {
			t.Fatal(err)
		}
	}
	return f, regularLimit, directLimit
}

func (s *directStartV3) startAutoResponse() (playback.DecisionResponseV3, error) {
	ctx := apimw.SetClaims(newAuthorizedPlaybackContext(), &auth.Claims{UserID: s.caller.UserID, Role: "user", TokenType: auth.TokenTypeAccess})
	ctx = clientip.SetContext(ctx, "203.0.113.7")
	return s.handler.StartPlaybackV2(ctx, s.caller, s.request)
}

func (s *directStartV3) startAuto(t *testing.T) *playback.PlanV3 {
	t.Helper()
	response, err := s.startAutoResponse()
	if err != nil || response.PlaybackPlan == nil {
		t.Fatalf("start: err=%v response=%#v", err, response)
	}
	return response.PlaybackPlan
}

// TestDirectStartPassesUseMediaHostKeyDB: a direct start plans its first pass
// under the API host's direct key. When the route it selects is a regular
// proxy, pass 2 plans again under that proxy's regular key and the plan
// carries the regular limit. Where the pinned proxy cannot serve, the final
// plan carries no limit at all. A regular start reads the regular key in one
// pass.
func TestDirectStartPassesUseMediaHostKeyDB(t *testing.T) {
	t.Run("a proxy-served direct start applies the proxy's regular limit", func(t *testing.T) {
		f, regular, _ := directLimitsV3(t, 1)
		plan := f.startAuto(t)
		if plan.BandwidthLimit == nil || plan.BandwidthLimit.Kbps != regular {
			t.Fatalf("limit %+v, want the regular key's %d", plan.BandwidthLimit, regular)
		}
		if !strings.HasPrefix(plan.Stream.URL, f.planner.proxies[0].URL) || plan.MediaTransport != playback.MediaTransportRegularV3 {
			t.Fatalf("plan origin %q over %q, want the proxy and a regular transport", urlOriginV3(plan.Stream.URL), plan.MediaTransport)
		}
	})
	t.Run("a pinned proxy that cannot serve leaves the final plan with no limit", func(t *testing.T) {
		f, _, _ := directLimitsV3(t, 1)
		// The first proxy selection is pass 1's dry route; every later one
		// finds the proxy gone.
		f.planner.proxyLostAfter = 1
		plan := f.startAuto(t)
		if plan.BandwidthLimit != nil {
			t.Fatalf("limit %+v, want none from an empty key", plan.BandwidthLimit)
		}
		if strings.HasPrefix(plan.Stream.URL, f.planner.proxies[0].URL) || plan.MediaTransport != playback.MediaTransportDirectV3 {
			t.Fatalf("plan origin %q over %q, want the API host's direct path", urlOriginV3(plan.Stream.URL), plan.MediaTransport)
		}
	})
	t.Run("a store failure in pass 2 fails the start", func(t *testing.T) {
		f, _, _ := directLimitsV3(t, 1)
		// Pass 1 and its dry route finish before the store goes away, so only
		// pass 2's reading fails.
		f.planner.onProxyPlan = func(n int) {
			if n == 1 {
				f.handler.Connections.Pool.Close()
			}
		}
		response, err := f.startAutoResponse()
		if operationErr, ok := errors.AsType[*PlaybackOperationError](err); !ok || operationErr.Status != http.StatusServiceUnavailable {
			t.Fatalf("start = %#v, %v; want a 503", response, err)
		}
	})
	t.Run("a start served by the API host applies the direct limit", func(t *testing.T) {
		f, _, direct := directLimitsV3(t, 0)
		plan := f.startAuto(t)
		if plan.BandwidthLimit == nil || plan.BandwidthLimit.Kbps != direct || plan.MediaTransport != playback.MediaTransportDirectV3 {
			t.Fatalf("limit %+v over %q, want the direct key's %d on a direct plan", plan.BandwidthLimit, plan.MediaTransport, direct)
		}
	})
	t.Run("a regular start never reads the direct key", func(t *testing.T) {
		f, regular, _ := directLimitsV3(t, 1)
		f.request.MediaTransport = playback.MediaTransportRegularV3
		plan := f.startAuto(t)
		if plan.BandwidthLimit == nil || plan.BandwidthLimit.Kbps != regular || plan.MediaTransport != playback.MediaTransportRegularV3 {
			t.Fatalf("limit %+v over %q, want the regular key's %d", plan.BandwidthLimit, plan.MediaTransport, regular)
		}
		if got := len(f.planner.routes); got != 1 {
			t.Fatalf("route selections = %d, want the one pass of a regular start", got)
		}
	})
}

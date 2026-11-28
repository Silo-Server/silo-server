package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// directOnlyContextV3 is ctx carrying the direct state of an enrolled
// direct_only device that asked for the direct transport.
func directOnlyContextV3(ctx context.Context) context.Context {
	return context.WithValue(ctx, directRequestKeyV3{}, &directRequestV3{
		requested: playback.MediaTransportDirectV3, enrolled: true,
		device: directconn.DeviceDirect{Policy: directPolicyDirectOnlyV3},
	})
}

// proxyOnlyRoutingV3 sets every workload's egress to proxy_only.
func proxyOnlyRoutingV3(handler *PlaybackHandler) {
	previous := handler.PlaybackConfig
	handler.PlaybackConfig = func() config.PlaybackConfig {
		var cfg config.PlaybackConfig
		if previous != nil {
			cfg = previous()
		}
		cfg.Routing = config.DefaultPlaybackRoutingPolicy()
		cfg.Routing.DirectPlayEgress = config.PlaybackEgressProxyOnly
		cfg.Routing.RemuxEgress = config.PlaybackEgressProxyOnly
		cfg.Routing.VideoTranscodeEgress = config.PlaybackEgressProxyOnly
		return cfg
	}
}

// directReplanRequestV3 is a replan of plan in the attempt f started.
func directReplanRequestV3(f *directStartV3, plan *playback.PlanV3, operation playback.ReplanOperationV3, id, transport string) playback.ReplanRequestV3 {
	return playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: operation,
		PlaybackAttemptID: f.request.PlaybackAttemptID, ReplanRequestID: id + "-request",
		FailedPlanID: plan.PlanID, PlanAttemptID: id + "-attempt", PlanAttemptKey: plan.PlanAttemptKey,
		AttemptCount: 1, PositionSeconds: 321, QualityPreference: f.request.QualityPreference, SelectedTracks: plan.SelectedTracks,
		MediaTransport: transport, ClientFeatures: f.request.ClientFeatures, Capabilities: f.request.Capabilities,
		ClientPlaybackContext: f.request.ClientPlaybackContext,
	}
}

// A seek reanchor of a direct_only session started on the API host's direct
// path must not move the media to a proxy. The pool's proxy is healthy and
// the policy is the default prefer_proxy, so only the direct_only rule keeps
// the seek on the API host, and the seek is served from it: a refusal would
// pass a test that only asks for no proxy.
func TestDirectOnlySeekReanchorNeverMovesToAProxy(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: directPolicyDirectOnlyV3, APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 1)
	// The proxy answers the liveness probe, so only the direct rule can keep
	// the seek off it.
	f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v", err)
	}
	plan := started.PlaybackPlan
	if !strings.HasPrefix(plan.Stream.URL, "/") || plan.MediaTransport != playback.MediaTransportDirectV3 {
		t.Fatalf("fixture: start plan origin %q over %q, want the API host's direct path", urlOriginV3(plan.Stream.URL), plan.MediaTransport)
	}
	request := directReplanRequestV3(f, plan, playback.ReplanOperationSeekReanchorV3, "direct-only-seek", playback.MediaTransportDirectV3)
	request.ClientFeatures = nil
	replanned, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: "direct-only-seek-digest"})
	if err != nil {
		t.Fatalf("seek reanchor: %v", err)
	}
	if replanned.Terminal != nil {
		t.Fatalf("seek reanchor refused with %q, want the API host's path", replanned.Terminal.Reason)
	}
	got := replanned.PlaybackPlan
	if got == nil {
		t.Fatal("seek reanchor returned neither a plan nor a refusal")
	}
	if !strings.HasPrefix(got.Stream.URL, "/") {
		t.Fatalf("seek reanchor of a direct_only session moved its media to origin %q, labeled %q; want the API host's own path", urlOriginV3(got.Stream.URL), got.MediaTransport)
	}
}

// seekAfterStartV3 replays the frozen recipe of the started plan at a new
// position, on the transport the plan used.
func seekAfterStartV3(t *testing.T, f *directStartV3, started playback.DecisionResponseV3, id string) playback.DecisionResponseV3 {
	t.Helper()
	request := directReplanRequestV3(f, started.PlaybackPlan, playback.ReplanOperationSeekReanchorV3, id, started.PlaybackPlan.MediaTransport)
	request.ClientFeatures = nil
	replanned, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: id + "-digest"})
	if err != nil {
		t.Fatalf("seek reanchor: %v", err)
	}
	return replanned
}

// A seek of a session on the direct path replays a stream whose connection
// the session's attribution describes, so it never goes through a proxy: the
// route it plans leaves proxies out, and the API host serves it. The proxy that
// joins the pool after the start, under the default prefer_proxy policy, is
// what would take a seek planned like a regular session.
func TestSeekReanchorOfADirectSessionStaysOnTheAPIHost(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 0)
	f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v", err)
	}
	if plan := started.PlaybackPlan; !strings.HasPrefix(plan.Stream.URL, "/") || plan.MediaTransport != playback.MediaTransportDirectV3 {
		t.Fatalf("fixture: start origin %q over %q, want the API host's direct path", urlOriginV3(plan.Stream.URL), plan.MediaTransport)
	}
	f.planner.proxies = directProxiesV3(1)

	replanned := seekAfterStartV3(t, f, started, "direct-session-seek")

	if replanned.Terminal != nil || replanned.PlaybackPlan == nil {
		t.Fatalf("seek of a direct session: plan %v, terminal %+v; want a plan from the API host", replanned.PlaybackPlan != nil, replanned.Terminal)
	}
	if got := replanned.PlaybackPlan; !strings.HasPrefix(got.Stream.URL, "/") || got.MediaTransport != playback.MediaTransportDirectV3 {
		t.Fatalf("seek of a direct session moved its media to origin %q over %q; want the API host's direct path", urlOriginV3(got.Stream.URL), got.MediaTransport)
	}
}

// When the operator's policy leaves a direct session's seek no route but a
// proxy, the seek ends in the same final refusal a start gets, and the session
// keeps the plan it has.
func TestSeekReanchorOfADirectSessionWithNoLegalAPIRouteIsRefusedFinally(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 0)
	f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v", err)
	}
	f.planner.proxies = directProxiesV3(1)
	proxyOnlyRoutingV3(f.handler)

	replanned := seekAfterStartV3(t, f, started, "direct-session-seek-proxy-only")

	if replanned.Terminal == nil || replanned.Terminal.Reason != "routing_policy_unsatisfied" || replanned.Terminal.Retryable {
		t.Fatalf("seek with only a proxy route: plan %v, terminal %+v; want the final routing_policy_unsatisfied", replanned.PlaybackPlan != nil, replanned.Terminal)
	}
	record, err := f.handler.PlanStoreV3.GetAttempt(newAuthorizedPlaybackContext(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.CurrentPlan.Stream.URL; got != started.PlaybackPlan.Stream.URL {
		t.Fatalf("refused seek changed the stored plan's origin to %q, want %q kept", urlOriginV3(got), urlOriginV3(started.PlaybackPlan.Stream.URL))
	}
}

// A regular session seeks as it always did: a proxy that has left the pool
// does not stop the seek, whether another proxy or the API host takes it.
func TestSeekReanchorOfARegularSessionStillSeeksOffADeadProxy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		proxies   int
		remaining int
	}{
		{name: "another proxy takes it", proxies: 2, remaining: 1},
		{name: "the API host takes it", proxies: 1, remaining: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}
			f := newDirectStartV3(t, state, tc.proxies)
			f.request.MediaTransport = playback.MediaTransportRegularV3
			f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
			started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
			if err != nil || started.PlaybackPlan == nil {
				t.Fatalf("start: err=%v", err)
			}
			if !strings.HasPrefix(started.PlaybackPlan.Stream.URL, f.planner.proxies[0].URL) {
				t.Fatalf("fixture: start origin %q, want the first proxy", urlOriginV3(started.PlaybackPlan.Stream.URL))
			}
			f.planner.proxies = f.planner.proxies[len(f.planner.proxies)-tc.remaining:]

			replanned := seekAfterStartV3(t, f, started, "regular-session-seek")

			if replanned.Terminal != nil || replanned.PlaybackPlan == nil {
				t.Fatalf("seek of a regular session off a dead proxy: plan %v, terminal %+v; want a plan", replanned.PlaybackPlan != nil, replanned.Terminal)
			}
		})
	}
}

// A device enrolled direct_only has no legal route when the operator pins
// every egress to proxies: no proxy can serve it and the API host may not. That
// is a final policy refusal, not retryable capacity loss, so the client stops
// and shows why. A device that takes a proxy still gets one.
func TestKnownDirectOnlyDeviceUnderProxyOnlyIsRefusedFinally(t *testing.T) {
	control := newDirectStartV3(t, &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}, 1)
	control.request.MediaTransport = playback.MediaTransportRegularV3
	proxyOnlyRoutingV3(control.handler)
	if plan := control.start(t); !strings.HasPrefix(plan.Stream.URL, control.planner.proxies[0].URL) {
		t.Fatalf("fixture: automatic device origin %q, want the proxy", urlOriginV3(plan.Stream.URL))
	}

	f := newDirectStartV3(t, &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: directPolicyDirectOnlyV3}}, 1)
	f.request.MediaTransport = playback.MediaTransportRegularV3
	proxyOnlyRoutingV3(f.handler)
	response, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || response.Terminal == nil {
		t.Fatalf("start: err=%v terminal=%+v; want a terminal refusal", err, response.Terminal)
	}
	if response.Terminal.Reason != "routing_policy_unsatisfied" || response.Terminal.Retryable {
		t.Fatalf("terminal %q retryable=%t; want routing_policy_unsatisfied, not retryable", response.Terminal.Reason, response.Terminal.Retryable)
	}
}

// When the proxy goes away after the first preview, a direct start falls back
// to the API host (TestDirectStartPassesUseMediaHostKeyDB). It must do the
// same when the proxy goes away after the second, pinned preview: the
// configured prefer_proxy fallback still holds at commit.
func TestDirectStartKeepsTheFallbackWhenTheProxyGoesAfterThePinnedPreview(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 1)
	// Proxy selections 1 and 2 are the two previews; every later one, the
	// commit's included, finds the proxy gone.
	f.planner.proxyLostAfter = 2
	response, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	f.planner.mu.Lock()
	proxyPlans := f.planner.proxyPlans
	f.planner.mu.Unlock()
	if proxyPlans < 3 {
		t.Fatalf("fixture: %d proxy selections, want the two previews and the commit", proxyPlans)
	}
	// The commit stays pinned to the proxy class the second pass planned for.
	// When it loses its proxy, the start plans once more under no connection
	// key and commits that plan under the operator's own policy: a fourth
	// selection. A commit left unpinned would run once and serve the second
	// pass's plan, made under the proxy's regular key, from the API host.
	if proxyPlans != 4 {
		t.Fatalf("%d proxy selections, want 4: two previews, the pinned commit, and the retry under no key", proxyPlans)
	}
	if err != nil || response.PlaybackPlan == nil {
		reason := ""
		if response.Terminal != nil {
			reason = response.Terminal.Reason
		}
		t.Fatalf("start with the proxy gone after the pinned preview: err=%v terminal=%q; want a plan on the API host, as when the proxy goes after the first preview", err, reason)
	}
	if got := response.PlaybackPlan.Stream.URL; !strings.HasPrefix(got, "/") {
		t.Fatalf("stream URL origin %q, want the API host's path", urlOriginV3(got))
	}
}

// A failure on a direct plan replanned with media_transport=regular, as
// client rule 9 in docs/direct-connections-api.md tells an Automatic client to
// do, retries the same recipe on the regular path. The direct path failed, not
// the recipe, so the recipe the fresh regular start would also pick stays
// eligible, even when the client lists its key as attempted.
func TestDirectPathFailureRecoveryOnRegularKeepsTheRecipe(t *testing.T) {
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:path-1", PathGeneration: 3}}
	f := newDirectStartV3(t, state, 0)
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v", err)
	}
	plan := started.PlaybackPlan
	if plan.MediaTransport != playback.MediaTransportDirectV3 {
		t.Fatalf("fixture: start media_transport %q, want direct", plan.MediaTransport)
	}
	output := f.request.ClientPlaybackContext.Output.OutputContextID
	key := playback.PlanAttemptKeyV3(*plan, output, nil)
	request := directReplanRequestV3(f, plan, playback.ReplanOperationFailureRecoveryV3, "direct-path-recovery", playback.MediaTransportRegularV3)
	request.PositionSeconds = 30
	request.PlanAttemptKey = key
	request.AttemptedPlanKeys = []string{key}
	request.Failure = playback.FailureV3{Classification: "network_error"}
	replanned, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: "direct-path-recovery-digest"})
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if replanned.PlaybackPlan == nil {
		reason := ""
		if replanned.Terminal != nil {
			reason = replanned.Terminal.Reason
		}
		t.Fatalf("recovery after a direct-path failure ended with %q; want the %s recipe again on the regular path", reason, plan.Delivery)
	}
	got := replanned.PlaybackPlan
	if playback.PlanAttemptKeyV3(*got, output, nil) != key || got.EffectiveMediaFileID != plan.EffectiveMediaFileID {
		t.Fatalf("recovery after a direct-path failure planned %s of file %d, want the failed direct plan's %s of file %d on the regular path",
			got.Delivery, got.EffectiveMediaFileID, plan.Delivery, plan.EffectiveMediaFileID)
	}
	if got.MediaTransport != playback.MediaTransportRegularV3 {
		t.Fatalf("recovery media_transport %q, want regular", got.MediaTransport)
	}
}

// Under proxy_only, a device whose enrollment cannot be read gets no proxy, so
// its start cannot be served. That refusal is a temporary dependency failure,
// not exhausted proxy capacity: docs/architecture/api-contract.md maps a
// failed store read to 503, and an operator must be able to tell it from a
// full proxy pool. The same start with a readable store reaches the proxy.
func TestUnreadableDeviceUnderProxyOnlyIsADependencyFailure(t *testing.T) {
	healthy := newDirectStartV3(t, &directStateV3{}, 1)
	healthy.request.MediaTransport = playback.MediaTransportRegularV3
	proxyOnlyRoutingV3(healthy.handler)
	if plan := healthy.start(t); !strings.HasPrefix(plan.Stream.URL, healthy.planner.proxies[0].URL) {
		t.Fatalf("fixture: readable never-enrolled device origin %q, want the proxy", urlOriginV3(plan.Stream.URL))
	}

	f := newDirectStartV3(t, &directStateV3{err: errors.New("store unavailable")}, 1)
	f.request.MediaTransport = playback.MediaTransportRegularV3
	proxyOnlyRoutingV3(f.handler)
	response, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if response.Terminal != nil {
		t.Fatalf("start with an unreadable enrollment store under proxy_only: terminal %q; want a 503 dependency_unavailable and no stored decision", response.Terminal.Reason)
	}
	operationErr, ok := errors.AsType[*PlaybackOperationError](err)
	if !ok || operationErr.Status != http.StatusServiceUnavailable || operationErr.Code != "dependency_unavailable" {
		t.Fatalf("start with an unreadable enrollment store under proxy_only: err=%v; want a 503 dependency_unavailable", err)
	}
}

// A direct_only device reaches no proxy, so on a worker_only deployment a
// progressive remux has no route it can take, and the remux must escalate to
// HLS exactly as it does when the pool holds no proxy
// (TestEscalateRefusedProgressiveRemuxV3StillEscalatesWithoutAnyProxyOrigin).
// The same fixture without the direct_only state keeps the progressive remux
// (TestEscalateRefusedProgressiveRemuxV3SkipsEscalationWhenOriginsHaveAProxy).
func TestEscalateRefusedProgressiveRemuxV3IgnoresProxiesADirectOnlyDeviceCannotUse(t *testing.T) {
	handler, input, result := escalationFixtureV3(t, true)
	handler.NodePlanner.(*recordingNodePlannerV3).plan.ProxyNode = &nodepool.Node{URL: "http://proxy-1"}
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

	escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(directOnlyContextV3(context.Background()), authorizedOriginsModeV3(), func() playback.PlannerInputV3 { return input }, result)
	if transportErr != nil {
		t.Fatalf("escalation error = %#v", transportErr)
	}
	if escalated.Plan == nil || escalated.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		delivery := playback.DeliveryV3("")
		if escalated.Plan != nil {
			delivery = escalated.Plan.Delivery
		}
		t.Fatalf("direct_only device kept delivery %q on a worker_only deployment whose only proxy it cannot use; want %q", delivery, playback.DeliveryRemuxHLSV3)
	}
}

// The pin of a second planning pass keeps the committed route on the proxy
// class, but a workload the operator set to api_only stays api_only: the pin
// may narrow the operator's policy, never widen it.
func TestPinProxyEgressKeepsAnOperatorAPIOnlyWorkload(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(withPlaybackRoutingPolicySnapshotV3(context.Background(), policy))
	pinned := handler.playbackRoutingPolicyForContextV3(pinProxyEgressV3(handler, r).Context())
	if pinned.DirectPlayEgress != config.PlaybackEgressProxyOnly {
		t.Fatalf("fixture: pinned direct play egress %q, want proxy_only", pinned.DirectPlayEgress)
	}
	if pinned.VideoTranscodeEgress != config.PlaybackEgressAPIOnly {
		t.Fatalf("pinned video transcode egress %q, want the operator's api_only kept", pinned.VideoTranscodeEgress)
	}
}

// A probe receipt names the path the probe request arrived on. When the
// device's path was replaced between the request's arrival (generation 5 on
// the request) and the receipt (generation 6 in the direct state), the receipt
// must not certify the replacement path.
func TestProbeConnectionReceiptRefusesAPathReplacedSinceTheRequestArrived(t *testing.T) {
	const user, device = 7, "tv-1"
	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: "direct:api-path-2", PathGeneration: 6}}
	h := &PlaybackHandler{JWTSecret: receiptSecretV3, DirectState: state}
	arrived := &netaccess.DirectPath{HostID: "api", PeerKey: "nodekey:peer", UserID: user, DeviceID: device, Class: "direct", PathGeneration: 5}
	receipt := h.ProbeConnectionReceipt(netaccess.WithPath(context.Background(), netaccess.Path{Direct: arrived}), user, device)
	if receipt == "" {
		return
	}
	claims, err := streamtoken.Verify(receipt, receiptSecretV3)
	if err != nil {
		t.Fatalf("receipt does not verify: %v", err)
	}
	t.Fatalf("a request that arrived on path generation 5 got a %s receipt for generation %d", claims.ConnectionClass, claims.PathGeneration)
}

// Progressive-remux planning counts only executors the request can reach. A
// direct_only device reaches no proxy, so a remux transformation that only a
// proxy advertises is not available to plan for it; otherwise the planner picks
// a progressive remux whose every route the direct_only rule then removes.
func TestProgressiveRemuxPlanningLeavesOutProxyExecutorsForDirectOnly(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{
			Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
			RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
		}}})
	}))
	t.Cleanup(proxy.Close)
	// The API host defines the conversion but cannot run it.
	local := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}})
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodePlanner = enumeratingNodePlannerV3{proxyURLs: []string{proxy.URL}}
	useDefaultRoutingV3(handler)

	if !handler.progressiveRemuxPlanningRegistryWithInputsV3(t.Context(), local, true).Available(playback.TransformationAudioToAACV3) {
		t.Fatal("fixture: the proxy's audio conversion is not available to a regular request")
	}
	if handler.progressiveRemuxPlanningRegistryWithInputsV3(directOnlyContextV3(t.Context()), local, true).Available(playback.TransformationAudioToAACV3) {
		t.Fatal("a direct_only request plans with an audio conversion only a proxy it cannot use advertises")
	}
}

// learned_limit=clear deletes the limit of the connection that serves the
// start. A direct start that a proxy serves plans under the regular key, so
// the regular connection's limit is the one cleared; the next Auto start on any
// regular route must not apply it again.
func TestDirectStartClearRemovesTheServingConnectionsLimitDB(t *testing.T) {
	f, _, _ := directLimitsV3(t, 1)
	f.request.LearnedLimit = playback.LearnedLimitClearV3
	plan := f.startAuto(t)
	if !strings.HasPrefix(plan.Stream.URL, f.planner.proxies[0].URL) || plan.MediaTransport != playback.MediaTransportRegularV3 {
		t.Fatalf("fixture: plan origin %q over %q, want the proxy and a regular transport", urlOriginV3(plan.Stream.URL), plan.MediaTransport)
	}
	remote := clientip.SetContext(context.Background(), "203.0.113.7")
	regularKey, _ := connectionstore.Key(remote, f.handler.JWTSecret, connectionstore.ClassRegular, "")
	limit, err := f.handler.Connections.Get(t.Context(), f.caller.UserID, "tv-1", regularKey)
	if err != nil {
		t.Fatal(err)
	}
	if limit != nil {
		t.Fatalf("learned_limit=clear on a start the proxy served left the regular connection's limit of %d kbps", limit.Kbps)
	}
}

// A proxy sourceRefused recovery retries the same plan through the API origin.
// The replan is then served by the API host over the direct path, so it plans
// under the direct key's limit, not the limit of the proxy it left.
func TestSourceRefusedRecoveryPlansUnderTheServingHostsKeyDB(t *testing.T) {
	f, _, directLimit := directLimitsV3(t, 1)
	// The refusing proxy still answers, so the preview can select it again.
	f.handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }
	started, err := f.startAutoResponse()
	if err != nil || started.PlaybackPlan == nil || !strings.HasPrefix(started.PlaybackPlan.Stream.URL, f.planner.proxies[0].URL) {
		t.Fatalf("fixture: start err=%v, want a plan on the proxy", err)
	}
	plan := started.PlaybackPlan
	request := directReplanRequestV3(f, plan, playback.ReplanOperationFailureRecoveryV3, "source-refused", playback.MediaTransportDirectV3)
	request.PositionSeconds = 30
	request.Failure = playback.FailureV3{Classification: "sourceRefused"}
	ctx := apimw.SetClaims(newAuthorizedPlaybackContext(), &auth.Claims{UserID: f.caller.UserID, Role: "user", TokenType: auth.TokenTypeAccess})
	ctx = clientip.SetContext(ctx, "203.0.113.7")
	replanned, err := f.handler.ReplanPlaybackV2(ctx, f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: "source-refused-digest"})
	if err != nil || replanned.PlaybackPlan == nil {
		t.Fatalf("recovery: err=%v", err)
	}
	got := replanned.PlaybackPlan
	if !strings.HasPrefix(got.Stream.URL, "/") || got.MediaTransport != playback.MediaTransportDirectV3 {
		t.Fatalf("fixture: recovery origin %q over %q, want the API host's direct path", urlOriginV3(got.Stream.URL), got.MediaTransport)
	}
	if got.BandwidthLimit == nil || got.BandwidthLimit.Kbps != directLimit {
		t.Fatalf("a recovery served over the direct path carries limit %+v, want the direct key's %d kbps", got.BandwidthLimit, directLimit)
	}
}

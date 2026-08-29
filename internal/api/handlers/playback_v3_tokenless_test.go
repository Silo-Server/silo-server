package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// The negotiated mode's whole promise is that nothing the client can see
// carries a playback credential, so this asserts on the entire response body
// rather than on the individual URLs a reader remembered to check.
func TestHandleStartPlaybackV3HeaderAuthenticatedResponseCarriesNoStreamToken(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: writePlaybackTestMediaFile(t, "movie.eng.srt"), Language: "eng", Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "fra"}}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-stream-signing-secret"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	subtitleIndex := 0
	start.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
	start.SubtitleTrackIndex = &subtitleIndex

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, streamTokenParam+"=") {
		t.Fatalf("header-authenticated response carries a stream token: %s", body)
	}

	var response playback.DecisionResponseV3
	if err := json.Unmarshal([]byte(body), &response); err != nil || response.PlaybackPlan == nil {
		t.Fatalf("response: err=%v body=%s", err, body)
	}
	urls := []string{response.PlaybackPlan.Stream.URL}
	if artifact := response.PlaybackPlan.Subtitle.Artifact; artifact != nil {
		urls = append(urls, artifact.URL)
	}
	for _, entry := range response.PlaybackPlan.Subtitle.Inventory {
		if entry.URL != "" {
			urls = append(urls, entry.URL)
		}
	}
	if len(urls) < 3 {
		t.Fatalf("response published %d URLs, want a stream plus subtitle artifact and inventory routes", len(urls))
	}
	for _, raw := range urls {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.IsAbs() || parsed.Query().Get(streamTokenParam) != "" {
			t.Fatalf("URL %q is not a tokenless API-local route (parse error %v)", raw, err)
		}
	}
	if len(response.PlaybackPlan.Stream.Headers) != 0 {
		t.Fatalf("plan persisted credential material in stream headers: %#v", response.PlaybackPlan.Stream.Headers)
	}
}

// The call-site checks are defense in depth; the builders themselves must
// refuse, so a future caller cannot mint a credential by forgetting to ask.
func TestPlaybackURLBuildersRefuseTokensForMediaAuthorizedSessions(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-stream-signing-secret"
	file := v3HandlerFixtureFile(t)
	proxy := &nodepool.Node{URL: "http://proxy.example"}

	secure := &playback.Session{ID: "session-secure", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID, PlayMethod: playback.PlayDirect, RequireMediaAuthorization: true}
	legacy := *secure
	legacy.ID = "session-legacy"
	legacy.RequireMediaAuthorization = false

	if got := handler.playbackStreamURL(secure); got != "/stream/session-secure" {
		t.Fatalf("secure stream URL = %q, want the bare API-local route", got)
	}
	if got := handler.playbackStreamURL(&legacy); !strings.Contains(got, streamTokenParam+"=") {
		t.Fatalf("legacy stream URL = %q, want a restart token", got)
	}

	if got, servedByProxy := handler.identityStreamURLV3(secure, file, proxy); servedByProxy || got != "/stream/session-secure" {
		t.Fatalf("secure identity URL = %q (proxy %v), want the API-local route", got, servedByProxy)
	}
	if got, servedByProxy := handler.identityStreamURLV3(&legacy, file, proxy); !servedByProxy || !strings.HasPrefix(got, proxy.URL) {
		t.Fatalf("legacy identity URL = %q (proxy %v), want the signed proxy route", got, servedByProxy)
	}

	card := playback.NewRecipeCard(secure.UserID, secure.ProfileID, file.ID, "", playback.TranscodeOpts{SessionID: secure.ID, InputPath: file.FilePath})
	if got := handler.buildProxyManifestURL(card, proxy, true); got != "/playback/transcode/session-secure/master.m3u8" {
		t.Fatalf("secure manifest URL = %q, want the tokenless API-local manifest", got)
	}
	if got := handler.buildProxyManifestURL(card, proxy, false); !strings.HasPrefix(got, proxy.URL+"/stream/transcode/") {
		t.Fatalf("legacy manifest URL = %q, want the signed proxy manifest", got)
	}
	if token := handler.signSessionToken(card, true); token != "" {
		t.Fatalf("signer minted a token for a media-authorized session: %q", token)
	}
	if token := handler.signSessionToken(card, false); token == "" {
		t.Fatal("signer refused a legacy session with a configured secret")
	}

	// The authorized-origins builders publish the same proxy origin the legacy
	// ones do, but address it by session id against a stored grant — so they
	// must never fall back to minting the credential the mode removed.
	grants := &recordingRecipeCardStoreV3{}
	handler.ProxyGrantStore = grants
	got, servedByProxy, _ := handler.identityGrantStreamURLV3(context.Background(), secure, file, proxy)
	if !servedByProxy || got != proxy.URL+"/stream/v3/session-secure" {
		t.Fatalf("origins identity URL = %q (proxy %v), want the credential-free proxy route", got, servedByProxy)
	}
	assertNoPlaybackCredentialV3(t, got)
	if _, ok := grants.cards["session-secure"]; !ok {
		t.Fatal("origins identity URL was published without a grant behind it")
	}

	got, servedByProxy, _ = handler.grantManifestURLV3(context.Background(), card, proxy)
	if !servedByProxy || got != proxy.URL+"/stream/v3/session-secure/master.m3u8" {
		t.Fatalf("origins manifest URL = %q (proxy %v), want the credential-free proxy manifest", got, servedByProxy)
	}
	assertNoPlaybackCredentialV3(t, got)
}

// escalationFixtureV3 plans a progressive remux that must convert audio, which
// is exactly the route the header-authenticated transport cannot execute once
// local fallback is disabled.
func escalationFixtureV3(t *testing.T, hlsCapable bool) (*PlaybackHandler, playback.PlannerInputV3, playback.PlannerResultV3) {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	file.CodecAudio = "eac3"
	file.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Layout: "5.1", Default: true}}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		"allow_4k_transcode":                "true",
		"playback.local_transcode_fallback": "false",
	}}
	registry := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
	})
	presetLocalRegistryV3(handler, registry)

	request := v3HandlerStartRequest()
	request.QualityPreference = "auto"
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	if hlsCapable {
		request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	}
	input := playback.PlannerInputV3{
		Request:       request,
		RequestedFile: file,
		EffectiveFile: file,
		Settings:      playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:      registry,
		HLSRegistry:   func() *playback.TransformationRegistryV3 { return registry },
	}
	result := playback.PlanPlaybackV3(input)
	if result.Terminal != nil || result.Plan == nil || result.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 {
		t.Fatalf("fixture planned %#v, want a progressive remux", result)
	}
	if !planRequiresServerTransformationsV3(result.Plan) {
		t.Fatalf("fixture plan carries no server transformation: %#v", result.Plan.Transformations)
	}
	return handler, input, result
}

// A progressive remux the header-authenticated transport would refuse is
// escalated onto HLS up front, rather than handed to the client as a retryable
// capacity error it can only recover from with a replan round trip.
func TestEscalateRefusedProgressiveRemuxV3PlansHLSForCapableClients(t *testing.T) {
	handler, input, result := escalationFixtureV3(t, true)
	escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), mediaAuthModeV3{headerAuth: true}, func() playback.PlannerInputV3 { return input }, result)
	if transportErr != nil {
		t.Fatalf("escalation error = %#v", transportErr)
	}
	if escalated.Plan == nil || escalated.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("escalated delivery = %#v, want %q", escalated.Plan, playback.DeliveryRemuxHLSV3)
	}
}

// A progressive-only client has no alternative delivery, so the refusal is
// final: a retryable error would make it retry a route no retry can satisfy.
func TestEscalateRefusedProgressiveRemuxV3IsTerminalForProgressiveOnlyClients(t *testing.T) {
	handler, input, result := escalationFixtureV3(t, false)
	_, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), mediaAuthModeV3{headerAuth: true}, func() playback.PlannerInputV3 { return input }, result)
	if transportErr == nil || transportErr.reason != "local_transcode_disabled" || transportErr.retryable {
		t.Fatalf("transport error = %#v, want a non-retryable local_transcode_disabled", transportErr)
	}
}

func TestEscalateRefusedProgressiveRemuxV3LeavesExecutableRoutesAlone(t *testing.T) {
	handler, input, result := escalationFixtureV3(t, true)
	planned := 0
	plannerInput := func() playback.PlannerInputV3 {
		planned++
		return input
	}
	if escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), mediaAuthModeV3{}, plannerInput, result); transportErr != nil || escalated.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 {
		t.Fatalf("legacy attempt was escalated: %#v %#v", escalated.Plan, transportErr)
	}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.local_transcode_fallback": "true"}}
	if escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), mediaAuthModeV3{headerAuth: true}, plannerInput, result); transportErr != nil || escalated.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 {
		t.Fatalf("locally executable remux was escalated: %#v %#v", escalated.Plan, transportErr)
	}
	if planned != 0 {
		t.Fatalf("planner input was rebuilt %d times on the non-escalating path", planned)
	}
}

// The capability fan-out costs a per-node HTTP round trip, so it must not run
// for a planner that cannot consume the eligibility predicate it produces.
func TestPlanNodeSessionV3SkipsCapabilityFanOutWithoutConsumer(t *testing.T) {
	fetches := 0
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		}})
	}))
	defer node.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	// Enumerates pooled nodes, but implements neither PlanSessionWith nor the
	// local-egress selector.
	handler.NodePlanner = enumeratingNodePlannerV3{
		staticNodePlannerV3: staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}}},
		urls:                []string{node.URL},
	}
	plan := &playback.PlanV3{
		PlanID:          "plan:no-consumer",
		Delivery:        playback.DeliveryRemuxHLSV3,
		Transformations: []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}},
	}
	for _, localEgress := range []bool{false, true} {
		selected := handler.planNodeSessionV3(context.Background(), &playback.Session{ID: "session-no-consumer"}, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux}, localEgress)
		if selected.TranscodeNode == nil || selected.TranscodeNode.URL != node.URL {
			t.Fatalf("planner selection = %+v, want the static node", selected.TranscodeNode)
		}
	}
	if fetches != 0 {
		t.Fatalf("capability fan-out ran %d times for a planner that discards the predicate", fetches)
	}
}

// software_video_decode_v1 is attempt-sticky like header-authenticated media: a
// replan that sends an explicit feature list cannot drop it and silently
// convert a direct route into a transcode.
func TestHandleReplanPlaybackV3PinsAttemptStickyFeatures(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-stream-signing-secret"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureSoftwareVideoDecodeV3)
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}

	nextContext := start.ClientPlaybackContext
	nextContext.Output.OutputContextID = "route-2"
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		ClientFeatures:        []string{playback.FeaturePlaybackPlanV3}, // explicit list dropping the opt-in
		Operation:             playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "sticky-feature-replan-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "sticky-feature-attempt-0001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       12,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: nextContext,
	})
	if replanned.PlaybackPlan == nil {
		t.Fatalf("replan = %#v", replanned)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !playback.HasFeatureV3(record.NormalizedRequest.ClientFeatures, playback.FeatureSoftwareVideoDecodeV3) {
		t.Fatalf("durable client features = %v, the software-decode opt-in was dropped", record.NormalizedRequest.ClientFeatures)
	}
}

// Every client-facing proxy URL builder joins onto the proxy's ClientURL —
// the public URL when one is set — while the backend URL stays what the
// server and the stream-token tnode claim dial. A split-network proxy
// registered by its private address must never leak that address to a player.
func TestProxyURLBuildersUseThePublicURLWhenSet(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-stream-signing-secret"
	file := v3HandlerFixtureFile(t)
	public := "https://cdn.example.com"
	proxy := &nodepool.Node{URL: "http://10.0.0.9:8083", PublicURL: &public}

	session := &playback.Session{ID: "session-public", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID, PlayMethod: playback.PlayDirect}
	if got, servedByProxy := handler.identityStreamURLV3(session, file, proxy); !servedByProxy || !strings.HasPrefix(got, public+"/stream/direct/") {
		t.Fatalf("identity URL = %q (proxy %v), want the public origin", got, servedByProxy)
	}

	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, "", playback.TranscodeOpts{SessionID: session.ID, InputPath: file.FilePath})
	if got := handler.buildProxyManifestURL(card, proxy, false); !strings.HasPrefix(got, public+"/stream/transcode/") {
		t.Fatalf("manifest URL = %q, want the public origin", got)
	}
	if strings.Contains(handler.buildProxyManifestURL(card, proxy, false), "10.0.0.9") {
		t.Fatalf("manifest URL leaked the backend address")
	}
}

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

// TestSeekReplayKeepsLearnedLimitDB: only a request carrying observed_rebuffer
// records a limit. A client replan that repeats its kept step-down estimate has
// no source and records nothing. A seek-scoped replan replays the stored
// start request, which applyLearnedLimit has already rewritten to the limit or
// marked observed_rebuffer over a lower probe. Only the client's own request
// may record or clear the limit, so a replay neither renews it nor stores the
// probe estimate as a new limit.
func TestSeekReplayKeepsLearnedLimitDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("learned-limit-replay-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })

	h := &PlaybackHandler{Connections: &connectionstore.Store{Pool: pool}}
	requestCtx := apimw.SetClaims(WithNativeAPIV2(ctx), &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess})
	requestCtx = context.WithValue(clientip.SetContext(requestCtx, "192.168.1.10"), playbackConnectionDeviceContextKey{}, "tv-1")
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(requestCtx)
	request := func(source string, estimate *int, control string) playback.StartRequestV3 {
		return playback.StartRequestV3{QualityPreference: playback.QualityAutoV3, ClientFeatures: []string{playback.FeatureBufferingQualityV1},
			BandwidthEstimateKbps: estimate, BandwidthEstimateSource: source, LearnedLimit: control}
	}
	stored := func() (int, time.Time, bool) {
		var kbps *int
		var observed *time.Time
		if err := pool.QueryRow(ctx, `SELECT limit_kbps, observed_at FROM playback_connections WHERE user_id = $1`, user).Scan(&kbps, &observed); err != nil {
			t.Fatal(err)
		}
		if kbps == nil || observed == nil {
			return 0, time.Time{}, false
		}
		return *kbps, *observed, true
	}

	lowered := request(playback.BandwidthEstimateObservedRebufferV3, new(44100), "")
	if limit, err := h.applyLearnedLimit(r, &lowered, true); err != nil || limit == nil || limit.Kbps != 44100 {
		t.Fatalf("client lowering: limit=%+v err=%v", limit, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET observed_at = now() - interval '1 day' WHERE user_id = $1`, user); err != nil {
		t.Fatal(err)
	}
	_, before, _ := stored()
	// A client replan that repeats the kept step-down estimate has no source:
	// it plans with min(estimate, limit) and records nothing.
	for _, tc := range []struct{ sent, planned int }{{50000, 44100}, {44100, 44100}, {30000, 30000}} {
		kept := request("", new(tc.sent), "")
		if _, err := h.applyLearnedLimit(r, &kept, true); err != nil || kept.BandwidthEstimateKbps == nil || *kept.BandwidthEstimateKbps != tc.planned {
			t.Fatalf("kept estimate %d planned %v, want %d (err %v)", tc.sent, kept.BandwidthEstimateKbps, tc.planned, err)
		}
	}
	if kbps, observed, ok := stored(); !ok || kbps != 44100 || !observed.Equal(before) {
		t.Fatalf("a kept estimate changed the limit: kbps=%d observed=%v", kbps, observed)
	}
	probed := request(playback.BandwidthEstimateProbeV3, new(20000), "")
	if limit, err := h.applyLearnedLimit(r, &probed, true); err != nil || limit != nil || probed.BandwidthEstimateSource != playback.BandwidthEstimateObservedRebufferV3 {
		t.Fatalf("lower probe: limit=%+v source=%q err=%v", limit, probed.BandwidthEstimateSource, err)
	}
	for _, replay := range []playback.StartRequestV3{lowered, probed} {
		if _, err := h.applyLearnedLimit(r, &replay, false); err != nil {
			t.Fatal(err)
		}
	}
	if kbps, observed, ok := stored(); !ok || kbps != 44100 || !observed.Equal(before) {
		t.Fatalf("replay changed the limit: kbps=%d observed=%v want 44100 at %v", kbps, observed, before)
	}

	cleared := request("", nil, playback.LearnedLimitClearV3)
	replayedClear := cleared
	if _, err := h.applyLearnedLimit(r, &replayedClear, false); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := stored(); !ok {
		t.Fatal("a replayed clear removed the limit")
	}
	if _, err := h.applyLearnedLimit(r, &cleared, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := stored(); ok {
		t.Fatal("the client's clear kept the limit")
	}
}

// TestConnectionBudgetAppliesAdminCapDB: the connection endpoint reports
// Auto's budget under the administrator's bitrate cap for the caller's
// network, the cap a start would plan under.
func TestConnectionBudgetAppliesAdminCapDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("connection-budget-cap-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })

	h := &PlaybackHandler{Connections: &connectionstore.Store{Pool: pool}, JWTSecret: "connection-budget-secret"}
	remote := clientip.SetContext(apimw.SetClaims(ctx, &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "203.0.113.7")
	if err := h.ReportPlaybackProbe(remote, "tv-1", connectionstore.ProbeResult{Kbps: 48000, Bytes: 12_000_000, DurationMS: 2000, Transport: "wifi"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		scope *access.Scope
		want  int
	}{
		{"no cap", nil, 38400},
		{"remote cap", &access.Scope{MaxRemoteStreamBitrateKbps: 12000, MaxLocalStreamBitrateKbps: 100000}, 12000},
		{"local cap only", &access.Scope{MaxLocalStreamBitrateKbps: 8000}, 38400},
	} {
		request := remote
		if tc.scope != nil {
			request = access.SetScope(remote, *tc.scope)
		}
		state, err := h.PlaybackConnection(request, "tv-1", false)
		if err != nil || state.AutoBudgetKbps == nil || *state.AutoBudgetKbps != tc.want {
			t.Fatalf("%s: budget=%v err=%v, want %d", tc.name, state.AutoBudgetKbps, err, tc.want)
		}
	}
	// A device ID Postgres cannot store is no identity, not a failed request.
	if err := h.ReportPlaybackProbe(remote, "tv-\xff", connectionstore.ProbeResult{Kbps: 1000, Bytes: 250_000, DurationMS: 2000, Transport: "wifi"}); err != nil {
		t.Fatalf("invalid device ID report: %v", err)
	}
	if state, err := h.PlaybackConnection(remote, "tv-\xff", false); err != nil || state.Probe != nil {
		t.Fatalf("invalid device ID read: probe=%+v err=%v", state.Probe, err)
	}
}

func TestBufferedAutoPrefersCopyVersion(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Bitrate = 63000
	source.VideoTracks[0].Bitrate = 63000
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.Bitrate = 31600
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Bitrate = 31600
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}})
	h.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	req := v3HandlerStartRequest()
	req.Capabilities.VideoDecode[0].MaxBitrateKbps = 100_000
	req.QualityPreference = "auto"
	req.BandwidthEstimateKbps = new(44100)
	req.BandwidthEstimateSource = "observed_rebuffer"
	req.ClientFeatures = append(req.ClientFeatures, playback.FeatureBufferingQualityV1)
	r := httptest.NewRequest("POST", "/", nil).WithContext(newAuthorizedPlaybackContext())
	chosen, _, _ := h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != alternate.ID {
		t.Fatalf("selected %d, want lower original %d", chosen.ID, alternate.ID)
	}
	req.AllowAlternateVersions = new(false)
	chosen, _, _ = h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != source.ID {
		t.Fatal("fixed-file request changed version")
	}
	req.AllowAlternateVersions = nil
	req.BandwidthEstimateSource = "probe"
	chosen, _, _ = h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != source.ID {
		t.Fatal("legacy probe behavior changed")
	}
}

func TestBufferedHDRChoosesSDRVersionWithoutToneMap(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Bitrate = 63000
	source.Resolution = "4k"
	source.CodecVideo = "hevc"
	source.VideoTracks[0] = models.VideoTrack{Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10, Bitrate: 63000, VideoRange: "HDR", VideoRangeType: "HDR10"}
	alternate := v3HandlerFixtureFile(t)
	alternate.ID = 84
	alternate.Bitrate = 31600
	alternate.VideoTracks[0].Bitrate = 31600
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}})
	h.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	req := v3HandlerStartRequest()
	req.Capabilities.VideoDecode[0].MaxBitrateKbps = 100000
	req.QualityPreference = playback.QualityAutoV3
	req.BandwidthEstimateKbps = new(44100)
	req.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
	r := httptest.NewRequest("POST", "/", nil).WithContext(newAuthorizedPlaybackContext())
	chosen, _, _ := h.lowerBufferedVersion(r, req, source, 0, playback.PlannerSettingsV3{})
	if chosen.ID != alternate.ID {
		t.Fatalf("HDR without tone-map policy chose %d, want SDR original %d", chosen.ID, alternate.ID)
	}
}

func TestPlaybackCallerKeepsConnectionDeviceSeparate(t *testing.T) {
	for _, device := range []string{"declared-device", ""} {
		r := playbackCallerRequest(newAuthorizedPlaybackContext(), PlaybackCaller{DeviceID: "playback-device", ConnectionDeviceID: device})
		got, _ := r.Context().Value(playbackConnectionDeviceContextKey{}).(string)
		if got != device {
			t.Fatalf("connection device = %q, want %q", got, device)
		}
		if r.Header.Get(deviceIDHeader) != "playback-device" {
			t.Fatal("changed existing playback device identity")
		}
	}
}

func TestLearnedLimitAnnotationRequiresAdditionalReduction(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Bitrate = 63000
	source.VideoTracks[0].Bitrate = 63000
	request := v3HandlerStartRequest()
	request.QualityPreference = playback.QualityAutoV3
	request.BandwidthEstimateKbps = new(44100)
	limit := &playback.BandwidthLimitV3{Kbps: 44100, Source: playback.BandwidthEstimateObservedRebufferV3}
	for _, tc := range []struct {
		name           string
		delivered, cap int
		annotated      bool
	}{
		{"lower original", 31600, 0, true},
		{"unchanged source", 63000, 0, false},
		{"admin cap already lower", 8000, 8000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := playback.PlannerResultV3{Plan: &playback.PlanV3{}}
			result.Plan.Source.BitrateKbps = tc.delivered
			markLearnedLimit(&result, limit, source, request, tc.cap)
			if (result.Plan.BandwidthLimit != nil) != tc.annotated {
				t.Fatalf("annotation=%+v", result.Plan.BandwidthLimit)
			}
		})
	}
}

// TestV1StartIgnoresBufferingQualityFields: /api/v1 is frozen and ignored
// these fields as unknown before buffering_quality_v1 existed. A v1 start that
// sends them without the feature still plays rather than failing with 400.
func TestV1StartIgnoresBufferingQualityFields(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	start := v3HandlerStartRequest()
	start.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
	start.LearnedLimit = playback.LearnedLimitClearV3
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

// TestBufferedReplansGoLowerToAVersion: an HDR source without a tone-map route
// cannot be transcoded, so Auto under a buffering budget must play a lower
// version instead of returning the source. An output change, in either form,
// returns to the requested edition first, and must not carry the stalled
// source back while the learned limit (here sent as the observed estimate it
// becomes) applies.
func TestBufferedReplansGoLowerToAVersion(t *testing.T) {
	handler, source, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
	handler.InstallationID = serviceInstallation
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
	// The device decodes the 1080p H.264 version as it is.
	startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, v3HandlerStartRequest().Capabilities.VideoDecode[0])
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}
	ctx := newAuthorizedPlaybackContext()
	started, err := handler.StartPlaybackV2(ctx, caller, startRequest)
	if err != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("start: err=%v response=%#v", err, started)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	plan := started.PlaybackPlan
	// Android reports an output move as a failure recovery classified
	// output_route_changed, with the new output context.
	movedOutput := startRequest.ClientPlaybackContext
	movedOutput.Output.OutputContextID = "output-after-route-change"
	for i, step := range []struct {
		operation playback.ReplanOperationV3
		failure   playback.FailureV3
		context   playback.ClientPlaybackContextV3
	}{
		{playback.ReplanOperationQualityChangeV3, playback.FailureV3{}, startRequest.ClientPlaybackContext},
		{playback.ReplanOperationOutputChangeV3, playback.FailureV3{}, startRequest.ClientPlaybackContext},
		{playback.ReplanOperationFailureRecoveryV3, playback.FailureV3{Classification: "output_route_changed"}, movedOutput},
	} {
		operation := step.operation
		replan := playback.ReplanRequestV3{
			ProtocolVersion: playback.ProtocolV3, Operation: operation, Failure: step.failure,
			PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: fmt.Sprintf("buffered-replan-%04d", i),
			FailedPlanID: plan.PlanID, PlanAttemptID: fmt.Sprintf("buffered-attempt-%04d", i),
			PlanAttemptKey: plan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
			QualityPreference: playback.QualityAutoV3, SelectedTracks: plan.SelectedTracks,
			BandwidthEstimateKbps: new(44100), BandwidthEstimateSource: playback.BandwidthEstimateObservedRebufferV3,
			ClientFeatures: startRequest.ClientFeatures, Capabilities: startRequest.Capabilities, ClientPlaybackContext: step.context,
		}
		response, err := handler.ReplanPlaybackV2(ctx, caller, started.SessionID, PlaybackReplanCommand{Request: replan, Digest: fmt.Sprintf("buffered-digest-%d", i)})
		if err != nil || response.PlaybackPlan == nil {
			t.Fatalf("%s: err=%v response=%#v", operation, err, response)
		}
		plan = response.PlaybackPlan
		if plan.EffectiveMediaFileID != alternate.ID || plan.Delivery == playback.DeliveryTranscodeHLSV3 {
			t.Fatalf("%s played file %d by %s with warnings %v, want the lower version %d without encoding", operation, plan.EffectiveMediaFileID, plan.Delivery, plan.DegradationWarnings, alternate.ID)
		}
	}
}

// TestLearnedLimitCarriesThroughReplansDB: a lowering records the limit, and a
// later output change that sends no estimate still plans under it from the
// store, stays on the lower version and reports bandwidth_limit.
func TestLearnedLimitCarriesThroughReplansDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("learned-limit-replans-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, user) })

	handler, _, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
	handler.InstallationID = serviceInstallation
	handler.JWTSecret = "learned-limit-replans-secret"
	handler.Connections = &connectionstore.Store{Pool: pool}
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
	startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, v3HandlerStartRequest().Capabilities.VideoDecode[0])
	caller := PlaybackCaller{UserID: user, ProfileID: "profile-1", InstallationID: serviceInstallation, ConnectionDeviceID: "tv-1"}
	ctx := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "profile-1")
	ctx = clientip.SetContext(ctx, "203.0.113.7")
	started, err := handler.StartPlaybackV2(ctx, caller, startRequest)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v response=%#v", err, started)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	plan := started.PlaybackPlan
	for i, step := range []struct {
		operation playback.ReplanOperationV3
		estimate  *int
		source    string
	}{
		{playback.ReplanOperationQualityChangeV3, new(44100), playback.BandwidthEstimateObservedRebufferV3},
		{playback.ReplanOperationOutputChangeV3, nil, ""},
	} {
		replan := playback.ReplanRequestV3{
			ProtocolVersion: playback.ProtocolV3, Operation: step.operation,
			PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: fmt.Sprintf("limit-replan-%04d", i),
			FailedPlanID: plan.PlanID, PlanAttemptID: fmt.Sprintf("limit-attempt-%04d", i),
			PlanAttemptKey: plan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
			QualityPreference: playback.QualityAutoV3, SelectedTracks: plan.SelectedTracks,
			BandwidthEstimateKbps: step.estimate, BandwidthEstimateSource: step.source,
			ClientFeatures: startRequest.ClientFeatures, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
		}
		response, err := handler.ReplanPlaybackV2(ctx, caller, started.SessionID, PlaybackReplanCommand{Request: replan, Digest: fmt.Sprintf("limit-digest-%d", i)})
		if err != nil || response.PlaybackPlan == nil {
			t.Fatalf("%s: err=%v response=%#v", step.operation, err, response)
		}
		plan = response.PlaybackPlan
		if plan.EffectiveMediaFileID != alternate.ID || plan.BandwidthLimit == nil || plan.BandwidthLimit.Kbps != 44100 {
			t.Fatalf("%s played file %d with bandwidth_limit %+v, want the lower version %d limited to 44100", step.operation, plan.EffectiveMediaFileID, plan.BandwidthLimit, alternate.ID)
		}
	}
}

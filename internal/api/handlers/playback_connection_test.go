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

// connectionTestPool opens the migrated database the DB contract tests run
// against, or skips when there is none.
func connectionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSeekReplayKeepsLearnedLimitDB: only a request carrying observed_rebuffer
// records a limit. A client replan that repeats its kept step-down estimate has
// no source and records nothing. A seek-scoped replan replays the stored
// start request, which applyLearnedLimit may have rewritten to the limit. Only
// the client's own request may record or clear the limit, so a replay neither
// renews it nor stores a probe estimate as a new limit. A lowering that reports a lower measured
// throughput records that instead. The last case sends a seek_failure_recovery
// through the native v2 replan dispatch, which must pass the replay as one.
func TestSeekReplayKeepsLearnedLimitDB(t *testing.T) {
	pool := connectionTestPool(t)
	ctx := t.Context()
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
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET observed_at = now() - interval '1 hour' WHERE user_id = $1`, user); err != nil {
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
	if limit, err := h.applyLearnedLimit(r, &probed, true); err != nil || limit != nil {
		t.Fatalf("lower probe: limit=%+v err=%v", limit, err)
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
	// A lowering that measured its plan's throughput below its step-down
	// estimate plans with, and records, the throughput.
	measured := request(playback.BandwidthEstimateObservedRebufferV3, new(44100), "")
	measured.ObservedThroughputKbps = new(31000)
	if _, err := h.applyLearnedLimit(r, &measured, true); err != nil {
		t.Fatal(err)
	}
	if kbps, _, _ := stored(); kbps != 31000 || *measured.BandwidthEstimateKbps != 31000 {
		t.Fatalf("measured lowering recorded %d and planned %d, want 31000", kbps, *measured.BandwidthEstimateKbps)
	}

	requireSeekRecoveryKeepsLimitV3(t, pool, user, stored)
}

// requireSeekRecoveryKeepsLimitV3 sends a seek_failure_recovery through the
// native v2 replan dispatch and fails unless it leaves the learned limit as it
// was. A stall on the same device and connection records the limit first, and
// the stored start request the recovery replays then carries it as an
// observed_rebuffer estimate.
func requireSeekRecoveryKeepsLimitV3(t *testing.T, pool *pgxpool.Pool, user int, stored func() (int, time.Time, bool)) {
	t.Helper()
	ctx := t.Context()
	handler, _, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
	handler.InstallationID = serviceInstallation
	handler.Connections = &connectionstore.Store{Pool: pool}
	stubCopySeekAnchorV3(handler)
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
	startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, v3HandlerStartRequest().Capabilities.VideoDecode[0])
	caller := PlaybackCaller{UserID: user, ProfileID: "profile-1", InstallationID: serviceInstallation, ConnectionDeviceID: "tv-1"}
	serviceCtx := apimw.SetProfileID(apimw.SetClaims(ctx, &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "profile-1")
	serviceCtx = clientip.SetContext(serviceCtx, "192.168.1.10")
	started, err := handler.StartPlaybackV2(serviceCtx, caller, startRequest)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v response=%#v", err, started)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	replan := func(i int, operation playback.ReplanOperationV3, plan *playback.PlanV3, estimate *int, source string, failure playback.FailureV3) *playback.PlanV3 {
		t.Helper()
		request := playback.ReplanRequestV3{
			ProtocolVersion: playback.ProtocolV3, Operation: operation, Failure: failure,
			PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: fmt.Sprintf("replay-replan-%04d", i),
			FailedPlanID: plan.PlanID, PlanAttemptID: fmt.Sprintf("replay-attempt-%04d", i),
			PlanAttemptKey: plan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
			QualityPreference: playback.QualityAutoV3, SelectedTracks: plan.SelectedTracks,
			BandwidthEstimateKbps: estimate, BandwidthEstimateSource: source,
			ClientFeatures: startRequest.ClientFeatures, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
		}
		response, err := handler.ReplanPlaybackV2(serviceCtx, caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: fmt.Sprintf("replay-digest-%d", i)})
		if err != nil || response.PlaybackPlan == nil {
			t.Fatalf("%s: err=%v response=%#v", operation, err, response)
		}
		return response.PlaybackPlan
	}
	stalled := replan(0, playback.ReplanOperationQualityChangeV3, started.PlaybackPlan, new(44100), playback.BandwidthEstimateObservedRebufferV3, playback.FailureV3{})
	if stalled.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("the stall played file %d, want the lower version %d", stalled.EffectiveMediaFileID, alternate.ID)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET observed_at = now() - interval '1 hour' WHERE user_id = $1`, user); err != nil {
		t.Fatal(err)
	}
	_, before, _ := stored()
	recovered := replan(1, playback.ReplanOperationSeekFailureRecoveryV3, stalled, nil, "", playback.FailureV3{Classification: "decoder_failure", Message: "the seek's route failed"})
	if recovered.EffectiveMediaFileID != alternate.ID || recovered.PlanAttemptKey == stalled.PlanAttemptKey {
		t.Fatalf("seek recovery played file %d on route %s, want another route for %d", recovered.EffectiveMediaFileID, recovered.PlanAttemptKey, alternate.ID)
	}
	if kbps, observed, ok := stored(); !ok || kbps != 44100 || !observed.Equal(before) {
		t.Fatalf("seek recovery changed the limit: kbps=%d observed=%v want 44100 at %v", kbps, observed, before)
	}
}

// TestConnectionBudgetAppliesAdminCapDB: the connection endpoint reports
// Auto's budget under the administrator's bitrate cap for the caller's
// network, the cap a start would plan under.
func TestConnectionBudgetAppliesAdminCapDB(t *testing.T) {
	pool := connectionTestPool(t)
	ctx := t.Context()
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
	req.AudioTrackID = playback.TrackIDV3(source.ID, "audio", 0)
	r := httptest.NewRequest("POST", "/", nil).WithContext(newAuthorizedPlaybackContext())
	chosen, next, _ := h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != alternate.ID {
		t.Fatalf("selected %d, want lower original %d", chosen.ID, alternate.ID)
	}
	// A later replan resolves the selection against the file that plays.
	if want := playback.TrackIDV3(alternate.ID, "audio", 0); next.AudioTrackID != want {
		t.Fatalf("audio identity %q, want %q on the lower version", next.AudioTrackID, want)
	}
	req.AudioTrackID = ""
	req.AllowAlternateVersions = new(false)
	chosen, _, _ = h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != source.ID {
		t.Fatal("fixed-file request changed version")
	}
	// Any Auto budget tries the version first: a speed test's, and a kept
	// estimate a client repeats without a source.
	req.AllowAlternateVersions = nil
	for _, estimateSource := range []string{playback.BandwidthEstimateProbeV3, ""} {
		req.BandwidthEstimateSource = estimateSource
		if chosen, _, _ = h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context())); chosen.ID != alternate.ID {
			t.Fatalf("source %q selected %d, want lower original %d", estimateSource, chosen.ID, alternate.ID)
		}
	}
}

// TestBufferedAutoPicksTheBestFittingVersion: of the versions within an Auto
// budget, one that keeps the source's dynamic range plays first, then the
// highest bitrate, whatever order the catalog lists them in (SDR and non-4K
// first). A version the client would have to encode is passed over for the
// 1080p SDR one.
func TestBufferedAutoPicksTheBestFittingVersion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		codec     string
		hdr       bool
		wantLower bool
	}{
		{"a lower HDR version keeps the picture HDR", "hevc", true, true},
		{"a 4K SDR version beats a lower 1080p one", "h264", false, true},
		{"a version the client cannot copy is passed over", "av1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, source, sdr, req := subtitleBlockedHDRFixtureV3(t)
			lowerValue := *source
			lower := &lowerValue
			lower.ID, lower.FilePath, lower.CodecVideo, lower.Bitrate, lower.SubtitleTracks = 85, writePlaybackTestMediaFile(t, "movie-4k-lower.mkv"), tc.codec, 30_000, nil
			track := source.VideoTracks[0]
			track.Codec, track.Bitrate = tc.codec, 30_000
			if !tc.hdr {
				track.Profile, track.Level, track.BitDepth, track.VideoRange, track.VideoRangeType = "high", 51, 8, "SDR", "SDR"
			}
			lower.VideoTracks = []models.VideoTrack{track}
			handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, sdr, lower}}}
			req.ClientFeatures = append(req.ClientFeatures, playback.FeatureBufferingQualityV1)
			req.Capabilities.VideoDecode = append(req.Capabilities.VideoDecode, playback.VideoDecodeCapabilityV3{
				Codec: "h264", Profiles: []string{"high"}, Levels: []int{41, 51}, BitDepths: []int{8},
				MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
			})
			req.BandwidthEstimateKbps, req.BandwidthEstimateSource = new(44100), playback.BandwidthEstimateProbeV3
			r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(newAuthorizedPlaybackContext())
			chosen, _, _ := handler.lowerBufferedVersion(r, req, source, 0, handler.plannerSettingsV3(r.Context()))
			want := sdr.ID
			if tc.wantLower {
				want = lower.ID
			}
			if chosen.ID != want {
				t.Fatalf("selected %d, want %d", chosen.ID, want)
			}
		})
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
	pool := connectionTestPool(t)
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

// TestLearnedLimitMarksOnlyAStartItLoweredDB: a start under a stored limit
// reports bandwidth_limit only when it plays something lower than the same
// start would without the limit. A client that cannot decode the 4K HDR HEVC
// source plays its 1080p H.264 version either way, so the limit lowered
// nothing. A client that plays the source moves to that version only under
// the limit, unless the connection's speed-test reading already moves it.
func TestLearnedLimitMarksOnlyAStartItLoweredDB(t *testing.T) {
	pool := connectionTestPool(t)
	var user int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("learned-limit-starts-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, user) })

	for _, tc := range []struct {
		name          string
		decodesSource bool
		readingKbps   int
		limited       bool
	}{
		{"compatibility picks the version", false, 0, false},
		{"the limit picks the version", true, 0, true},
		{"the reading picks the version", true, 50000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, source, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
			handler.InstallationID = serviceInstallation
			handler.JWTSecret = "learned-limit-starts-secret"
			handler.Connections = &connectionstore.Store{Pool: pool}
			startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
			h264 := v3HandlerStartRequest().Capabilities.VideoDecode[0]
			if tc.decodesSource {
				startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, h264)
			} else {
				startRequest.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{h264}
			}
			device := "tv-" + strings.ReplaceAll(tc.name, " ", "-")
			caller := PlaybackCaller{UserID: user, ProfileID: "profile-1", InstallationID: serviceInstallation, ConnectionDeviceID: device}
			ctx := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "profile-1")
			ctx = clientip.SetContext(ctx, "203.0.113.7")
			reportReadingV3(t, handler, ctx, device, tc.readingKbps)
			start := func(attempt string) *playback.PlanV3 {
				request := startRequest
				request.PlaybackAttemptID = attempt
				started, err := handler.StartPlaybackV2(ctx, caller, request)
				if err != nil || started.PlaybackPlan == nil {
					t.Fatalf("start %s: err=%v response=%#v", attempt, err, started)
				}
				t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
				return started.PlaybackPlan
			}

			unlimited := start("attempt-limit-start-0001")
			want := alternate.ID
			if tc.decodesSource && tc.readingKbps == 0 {
				want = source.ID
			}
			if unlimited.EffectiveMediaFileID != want || unlimited.BandwidthLimit != nil {
				t.Fatalf("without a limit played file %d with bandwidth_limit %+v, want %d unlimited", unlimited.EffectiveMediaFileID, unlimited.BandwidthLimit, want)
			}
			key, _ := connectionstore.Key(ctx, handler.JWTSecret)
			if err := handler.Connections.Observe(ctx, user, device, key, 44100); err != nil {
				t.Fatal(err)
			}
			limited := start("attempt-limit-start-0002")
			if limited.EffectiveMediaFileID != alternate.ID || (limited.BandwidthLimit != nil) != tc.limited {
				t.Fatalf("under the limit played file %d with bandwidth_limit %+v, want the lower version %d, limited=%v", limited.EffectiveMediaFileID, limited.BandwidthLimit, alternate.ID, tc.limited)
			}
		})
	}
}

// TestLearnedLimitMarksOnlyAReplanItLoweredDB: a replan reports bandwidth_limit
// only when it plays something lower than the same replan would without the
// limit. After a stall lowers Auto, a client that cannot decode the 4K HDR HEVC
// source plays its 1080p H.264 version either way, so no replan is limited. A
// client that plays the source moves to that version under the limit, and a
// later track change keeps that version, and so the label, even though the
// same track change without the limit would stay on it too. When the
// connection's speed-test reading already moves Auto to that version, the
// stall's limit lowered nothing either.
func TestLearnedLimitMarksOnlyAReplanItLoweredDB(t *testing.T) {
	pool := connectionTestPool(t)
	var user int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("learned-limit-replan-marks-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, user) })

	for _, tc := range []struct {
		name          string
		decodesSource bool
		readingKbps   int
		limited       bool
	}{
		{"compatibility picks the version", false, 0, false},
		{"the limit picks the version", true, 0, true},
		{"the reading picks the version", true, 50000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, source, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
			handler.InstallationID = serviceInstallation
			handler.JWTSecret = "learned-limit-replan-marks-secret"
			handler.Connections = &connectionstore.Store{Pool: pool}
			startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
			h264 := v3HandlerStartRequest().Capabilities.VideoDecode[0]
			want := alternate.ID
			if tc.decodesSource {
				startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, h264)
			} else {
				startRequest.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{h264}
			}
			if tc.decodesSource && tc.readingKbps == 0 {
				want = source.ID
			}
			caller := PlaybackCaller{UserID: user, ProfileID: "profile-1", InstallationID: serviceInstallation, ConnectionDeviceID: "tv-" + strings.ReplaceAll(tc.name, " ", "-")}
			ctx := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "profile-1")
			ctx = clientip.SetContext(ctx, "203.0.113.7")
			reportReadingV3(t, handler, ctx, caller.ConnectionDeviceID, tc.readingKbps)
			started, err := handler.StartPlaybackV2(ctx, caller, startRequest)
			if err != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != want {
				t.Fatalf("start: err=%v response=%#v, want file %d", err, started, want)
			}
			t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

			plan := started.PlaybackPlan
			for i, step := range []struct {
				operation playback.ReplanOperationV3
				estimate  *int
				source    string
			}{
				{playback.ReplanOperationQualityChangeV3, new(44100), playback.BandwidthEstimateObservedRebufferV3},
				{playback.ReplanOperationTrackChangeV3, nil, ""},
				{playback.ReplanOperationOutputChangeV3, nil, ""},
			} {
				replan := playback.ReplanRequestV3{
					ProtocolVersion: playback.ProtocolV3, Operation: step.operation,
					PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: fmt.Sprintf("marks-replan-%04d", i),
					FailedPlanID: plan.PlanID, PlanAttemptID: fmt.Sprintf("marks-attempt-%04d", i),
					PlanAttemptKey: plan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
					QualityPreference: playback.QualityAutoV3, SelectedTracks: plan.SelectedTracks,
					BandwidthEstimateKbps: step.estimate, BandwidthEstimateSource: step.source,
					ClientFeatures: startRequest.ClientFeatures, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
				}
				response, err := handler.ReplanPlaybackV2(ctx, caller, started.SessionID, PlaybackReplanCommand{Request: replan, Digest: fmt.Sprintf("marks-digest-%d", i)})
				if err != nil || response.PlaybackPlan == nil {
					t.Fatalf("%s: err=%v response=%#v", step.operation, err, response)
				}
				plan = response.PlaybackPlan
				if plan.EffectiveMediaFileID != alternate.ID || (plan.BandwidthLimit != nil) != tc.limited {
					t.Fatalf("%s played file %d with bandwidth_limit %+v, want the lower version %d, limited=%v", step.operation, plan.EffectiveMediaFileID, plan.BandwidthLimit, alternate.ID, tc.limited)
				}
			}
		})
	}
}

// reportReadingV3 stores a speed test for device, or nothing for zero.
func reportReadingV3(t *testing.T, h *PlaybackHandler, ctx context.Context, device string, kbps int) {
	t.Helper()
	if kbps == 0 {
		return
	}
	if err := h.ReportPlaybackProbe(ctx, device, connectionstore.ProbeResult{Kbps: kbps, Bytes: 3000000, DurationMS: 500, Transport: "ethernet"}); err != nil {
		t.Fatal(err)
	}
}

// TestConnectionReadingsDB: a plan summary's throughput becomes the
// connection's playback sample, a nonsense figure does not, a probe keeps its
// lower bound, and only readings within their lifetime count toward the
// budget, though both are still returned. Only current readings stand in for
// a missing Auto estimate.
func TestConnectionReadingsDB(t *testing.T) {
	pool := connectionTestPool(t)
	ctx := t.Context()
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("connection-readings-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })

	connections := &connectionstore.Store{Pool: pool}
	h := &PlaybackHandler{Connections: connections, JWTSecret: "connection-readings-secret"}
	remote := clientip.SetContext(apimw.SetClaims(ctx, &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "203.0.113.7")
	key, _ := connectionstore.Key(remote, h.JWTSecret)
	if err := h.ReportPlaybackProbe(remote, "tv-1", connectionstore.ProbeResult{Kbps: 48000, Bytes: 3000000, DurationMS: 500, Transport: "ethernet", LowerBound: true}); err != nil {
		t.Fatal(err)
	}
	summary := func(throughput string) {
		event := playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{Event: playback.RouteEventPlanSummaryV3, PlanID: "plan-1", Diagnostics: map[string]string{"throughput_kbps": throughput}},
			EventID: fmt.Sprintf("summary-%s-%d", throughput, time.Now().UnixNano()), UserID: user, ConnectionDeviceID: "tv-1", ConnectionKey: key, ReceivedAt: time.Now(), SampleLifetime: 7 * 24 * time.Hour}
		recordRouteEventV3(playback.NewMemoryPlanStoreV3(), event, connections)
	}
	for _, nonsense := range []string{"fast", "0", "99", "2000000"} {
		summary(nonsense)
	}
	state, err := h.PlaybackConnection(remote, "tv-1", false)
	if err != nil || state.PlaybackSample != nil || state.Probe == nil || !state.Probe.LowerBound || state.AutoBudgetKbps == nil || *state.AutoBudgetKbps != 38400 {
		t.Fatalf("before a sample: %+v (%v)", state, err)
	}
	summary("60000")
	state, err = h.PlaybackConnection(remote, "tv-1", false)
	if err != nil || state.PlaybackSample == nil || state.PlaybackSample.Kbps != 60000 || state.AutoBudgetKbps == nil || *state.AutoBudgetKbps != 48000 {
		t.Fatalf("with a 60000 sample: %+v (%v)", state, err)
	}
	// An Auto start or replan without an estimate plans under that reading, as
	// a probe estimate; one that carries an estimate or ignores the learned
	// limit (as a Cast start does) keeps what it sent.
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(context.WithValue(WithNativeAPIV2(remote), playbackConnectionDeviceContextKey{}, "tv-1"))
	estimated := func(estimate *int, control string) *int {
		t.Helper()
		req := playback.StartRequestV3{QualityPreference: playback.QualityAutoV3, ClientFeatures: []string{playback.FeatureBufferingQualityV1}, BandwidthEstimateKbps: estimate, LearnedLimit: control}
		if err := h.applyConnectionReading(r, &req); err != nil {
			t.Fatal(err)
		}
		if req.BandwidthEstimateKbps != nil && estimate == nil && req.BandwidthEstimateSource != playback.BandwidthEstimateProbeV3 {
			t.Fatalf("reading sent as %q, want a probe estimate", req.BandwidthEstimateSource)
		}
		return req.BandwidthEstimateKbps
	}
	if got := estimated(nil, ""); got == nil || *got != 60000 {
		t.Fatalf("missing estimate planned under %v, want the 60000 reading", got)
	}
	if got := estimated(new(20000), ""); *got != 20000 {
		t.Fatalf("sent estimate replaced by %d", *got)
	}
	if got := estimated(nil, playback.LearnedLimitIgnoreV3); got != nil {
		t.Fatalf("ignore planned under %d, want no estimate", *got)
	}
	// Past the 7-day lifetime the probe is still shown but no longer counts.
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET measured_at = now() - interval '8 days', sample_at = now() - interval '8 days' WHERE user_id = $1`, user); err != nil {
		t.Fatal(err)
	}
	state, err = h.PlaybackConnection(remote, "tv-1", false)
	if err != nil || state.Probe == nil || state.PlaybackSample == nil || state.AutoBudgetKbps != nil {
		t.Fatalf("expired readings: %+v (%v)", state, err)
	}
	if got := estimated(nil, ""); got != nil {
		t.Fatalf("expired readings planned under %d, want no estimate", *got)
	}
}

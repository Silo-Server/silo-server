package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type transportCapabilityBearerValidator struct{}

func (transportCapabilityBearerValidator) ValidateToken(string) (*auth.Claims, error) {
	return nil, errors.New("expired access token")
}

type transportCapabilitySessionValidator struct{}

func (transportCapabilitySessionValidator) IsValid(context.Context, string) (bool, error) {
	return false, nil
}

func TestVerifiedStreamCardFromRequestUsesHeaderCapabilityForReconstruction(t *testing.T) {
	const secret = "transport-capability-reconstruction-secret"
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:       "playback-1",
		MediaPath:       "/media/movie.mkv",
		PlayMethod:      "transcode",
		TargetCodec:     "h264",
		OutputSubdir:    "playback-1-plan-1",
		UserID:          7,
		ProfileID:       "profile-1",
		MediaFileID:     42,
		TargetRes:       "1920x1080",
		SegmentDuration: 6,
	}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	authMiddleware := apimw.NewAuthMiddleware(
		transportCapabilityBearerValidator{},
		transportCapabilitySessionValidator{},
		nil,
		nil,
	)
	router := chi.NewRouter()
	router.With(authMiddleware.RequireTransportAuth(secret)).Get("/stream/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		card, claims := verifiedStreamCardFromRequest(r, "playback-1", secret)
		if card == nil || claims == nil {
			t.Fatal("header capability did not provide a reconstruction recipe")
		}
		if card.SessionID != "playback-1" || card.UserID != 7 || card.MediaFileID != 42 || card.InputPath != "/media/movie.mkv" || card.TargetCodecVideo != "h264" {
			t.Fatalf("reconstruction card = %#v", card)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
	req.Header.Set(streamtoken.Header, token)
	req.Header.Set("Authorization", "Bearer expired-access-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestApplyPreparedTransportToPlanV3DoesNotRewritePreviousPlanCapability(t *testing.T) {
	audioIndex := 1
	previous := playback.PlanV3{
		ProtocolVersion: playback.ProtocolV3, PlanID: "plan:previous",
		Delivery: playback.DeliveryRemuxHLSV3,
		Stream: playback.StreamV3{
			Protocol: playback.StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", HeaderRefresh: playback.HeaderRefreshSessionV3,
			URL:     "/playback/transcode/playback-1/master.m3u8",
			Headers: map[string]string{streamtoken.Header: "previous-generation-capability"},
		},
		SelectedTracks:  playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "audio", audioIndex), Index: &audioIndex}},
		EffectiveRecipe: playback.EffectiveRecipeV3{VideoCodec: "h264", AudioCodec: "aac", DynamicRange: "sdr"},
		Subtitle:        playback.SubtitleDecisionV3{Mode: playback.SubtitleOffV3},
		Transformations: []playback.TransformationV3{}, AppliedQuirks: []playback.AppliedQuirkV3{}, RuntimeCorrections: []string{},
		RequestedMediaFileID: 42, EffectiveMediaFileID: 42,
	}
	operational := playback.PlannerResultV3{Plan: &previous, PlayMethod: playback.PlayRemux, TargetVideoCodec: "copy", SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	record := &playback.AttemptRecordV3{
		PlaybackAttemptID: "attempt-capability-0001", RequestedMediaFileID: 42, EffectiveMediaFileID: 42,
		CurrentPlanID: previous.PlanID, CurrentPlan: previous, FrozenRecipe: playback.FreezeExecutableRecipeV3(operational),
	}

	// A seek reanchor starts from a by-value copy of the current plan. Installing
	// the seek generation's capability must not reach the stored plan before the
	// transport commits, or a rolled-back seek would leave its recipe behind.
	result, err := frozenSeekReanchorResultV3(record, 600, time.Unix(1_786_000_000, 0))
	if err != nil || result.Plan == nil {
		t.Fatalf("frozen reanchor: result=%#v err=%v", result, err)
	}
	applyPreparedTransportToPlanV3(result.Plan, preparedTransportV3{
		url:     "/playback/transcode/playback-1/master.m3u8",
		headers: map[string]string{streamtoken.Header: "discarded-seek-capability"},
	})

	if got := result.Plan.Stream.Headers[streamtoken.Header]; got != "discarded-seek-capability" {
		t.Fatalf("candidate capability = %q", got)
	}
	if got := record.CurrentPlan.Stream.Headers[streamtoken.Header]; got != "previous-generation-capability" {
		t.Fatalf("previous plan capability = %q after an uncommitted seek", got)
	}
}

func TestPlannerMediaAuthModeMatchesSessionCapabilityTransport(t *testing.T) {
	req := playback.StartRequestV3{ClientFeatures: []string{
		playback.FeatureHeaderAuthenticatedMediaV3,
		playback.FeatureAuthorizedMediaOriginsV3,
		playback.FeatureDeviceQuirksV3,
	}}
	req.ClientPlaybackContext.Device.Platform = "tvos"
	if featureOnly := headerAuthenticatedMediaV3(req.ClientFeatures); !featureOnly.proxyEgress {
		t.Fatalf("feature-only mode = %#v, want proxy egress", featureOnly)
	}
	start := mediaAuthModeForStartV3(req, "31")
	if !start.sessionHeaderCapability || start.proxyEgress {
		t.Fatalf("build 31 start mode = %#v", start)
	}

	// The planner must see the transport's mode, not the feature-only default,
	// or it can choose a proxy route the pinned transport will refuse.
	if _, ok := plannerMediaAuthModeFromContextV3(context.Background()); ok {
		t.Fatal("planner mode present without being set")
	}
	planned, ok := plannerMediaAuthModeFromContextV3(withPlannerMediaAuthModeV3(context.Background(), start))
	if !ok || planned != start {
		t.Fatalf("planner mode = %#v, ok = %v, want %#v", planned, ok, start)
	}
}

func TestApplyPreparedTransportToPlanV3LeavesHeadersUntouchedWithoutCapability(t *testing.T) {
	plan := &playback.PlanV3{}
	applyPreparedTransportToPlanV3(plan, preparedTransportV3{url: "/stream/playback-1"})
	if plan.Stream.URL != "/stream/playback-1" || plan.Stream.Headers != nil {
		t.Fatalf("stream = %#v", plan.Stream)
	}
}

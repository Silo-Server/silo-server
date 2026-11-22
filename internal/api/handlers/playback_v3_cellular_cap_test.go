package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func cellularMultiVersionFixtureV3(t *testing.T) (*PlaybackHandler, *models.MediaFile, *models.MediaFile, playback.StartRequestV3) {
	t.Helper()
	handler, source, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
	handler.InstallationID = serviceInstallation
	startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, v3HandlerStartRequest().Capabilities.VideoDecode[0])
	return handler, source, alternate, startRequest
}

func TestStartPlaybackV2AutoWithBandwidthCapSelectsFittingSiblingInsteadOfTranscoding(t *testing.T) {
	handler, source, alternate, startRequest := cellularMultiVersionFixtureV3(t)
	startRequest.QualityPreference = playback.QualityAutoV3
	startRequest.BandwidthCapKbps = new(10_000)

	started, err := handler.startPlaybackV2ForTest(t, startRequest)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start under Auto / 10 Mbps: err=%v response=%#v", err, started)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	if started.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("effective file = %d (requested %d), want 8 Mbps sibling %d", started.PlaybackPlan.EffectiveMediaFileID, source.ID, alternate.ID)
	}
	if started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("delivery = %s, want direct play of sibling", started.PlaybackPlan.Delivery)
	}
}

func TestLowerBufferedVersionForIntentUnknownSourceBitrateAndServerCapBoundary(t *testing.T) {
	t.Run("unknown source video bitrate under explicit cap searches siblings", func(t *testing.T) {
		handler, source, alternate, startRequest := cellularMultiVersionFixtureV3(t)
		source.Bitrate = 0
		source.VideoTracks[0].Bitrate = 0
		startRequest.QualityPreference = playback.QualityAutoV3
		startRequest.BandwidthCapKbps = new(10_000)

		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(newAuthorizedPlaybackContext())
		chosen, _, _ := handler.lowerBufferedVersionForIntent(
			req, startRequest, source, source, 0,
			playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, nil,
		)
		if chosen.ID != alternate.ID {
			t.Fatalf("unknown bitrate base with 10 Mbps cap chose file %d, want sibling %d", chosen.ID, alternate.ID)
		}
	})

	t.Run("server cap alone without client cap or estimate does not trigger sibling swap", func(t *testing.T) {
		handler, source, _, startRequest := cellularMultiVersionFixtureV3(t)
		startRequest.QualityPreference = playback.QualityAutoV3
		startRequest.BandwidthCapKbps = nil
		startRequest.BandwidthEstimateKbps = nil

		ctx := access.SetScope(clientip.SetContext(newAuthorizedPlaybackContext(), "203.0.113.7"), access.Scope{
			MaxRemoteStreamBitrateKbps: 10_000,
		})
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
		chosen, _, _ := handler.lowerBufferedVersionForIntent(
			req, startRequest, source, source, 0,
			playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, nil,
		)
		if chosen.ID != source.ID {
			t.Fatalf("server cap alone chose file %d, want original %d", chosen.ID, source.ID)
		}
	})
}

func TestReplanQualityChangeWithChangedBandwidthCapReconsidersRequestedVersion(t *testing.T) {
	handler, source, alternate, startRequest := cellularMultiVersionFixtureV3(t)
	startRequest.QualityPreference = playback.QualityAutoV3
	startRequest.BandwidthCapKbps = new(2_000)

	ctx := newAuthorizedPlaybackContext()
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}
	started, err := handler.StartPlaybackV2(ctx, caller, startRequest)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start at Auto / 2 Mbps: err=%v response=%#v", err, started)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	if started.PlaybackPlan.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("start at Auto / 2 Mbps delivery = %s, want HLS transcode", started.PlaybackPlan.Delivery)
	}

	runQualityChange := func(step int, prev *playback.PlanV3, capKbps *int) *playback.PlanV3 {
		t.Helper()
		req := playback.ReplanRequestV3{
			ProtocolVersion:       playback.ProtocolV3,
			Operation:             playback.ReplanOperationQualityChangeV3,
			PlaybackAttemptID:     startRequest.PlaybackAttemptID,
			ReplanRequestID:       fmt.Sprintf("cap-replan-%04d", step),
			FailedPlanID:          prev.PlanID,
			PlanAttemptID:         fmt.Sprintf("cap-attempt-%04d", step),
			PlanAttemptKey:        prev.PlanAttemptKey,
			AttemptCount:          1,
			PositionSeconds:       30,
			QualityPreference:     playback.QualityAutoV3,
			BandwidthCapKbps:      capKbps,
			SelectedTracks:        prev.SelectedTracks,
			ClientFeatures:        startRequest.ClientFeatures,
			Capabilities:          startRequest.Capabilities,
			ClientPlaybackContext: startRequest.ClientPlaybackContext,
		}
		resp, err := handler.ReplanPlaybackV2(ctx, caller, started.SessionID, PlaybackReplanCommand{
			Request: req,
			Digest:  fmt.Sprintf("cap-digest-%d", step),
		})
		if err != nil || resp.PlaybackPlan == nil {
			t.Fatalf("replan step %d (cap=%v): err=%v response=%#v", step, capKbps, err, resp)
		}
		return resp.PlaybackPlan
	}

	// Low Data Mode turned off: Auto / 2 Mbps -> Auto / 10 Mbps upgrades to the 8 Mbps 1080p sibling.
	at10Mbps := runQualityChange(1, started.PlaybackPlan, new(10_000))
	if at10Mbps.EffectiveMediaFileID != alternate.ID || at10Mbps.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("Auto / 2 Mbps -> Auto / 10 Mbps played file %d via %s, want 8 Mbps sibling %d via direct play",
			at10Mbps.EffectiveMediaFileID, at10Mbps.Delivery, alternate.ID)
	}

	// Switched to Wi-Fi: Auto / 10 Mbps -> Auto / uncapped upgrades back to the 64 Mbps 4K requested file.
	uncapped := runQualityChange(2, at10Mbps, nil)
	if uncapped.EffectiveMediaFileID != source.ID || uncapped.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("Auto / 10 Mbps -> Auto / uncapped played file %d via %s, want 4K source %d via direct play",
			uncapped.EffectiveMediaFileID, uncapped.Delivery, source.ID)
	}
}

func TestReplanQualityChangeWithEqualCapAndClearedEstimateReselectsRequestedVersion(t *testing.T) {
	handler, source, alternate, startRequest := cellularMultiVersionFixtureV3(t)
	startRequest.QualityPreference = playback.QualityAutoV3
	// Both cellular and Wi-Fi use the same 80 Mbps cap, so the 64 Mbps 4K
	// source fits the cap, while a 12 Mbps cellular estimate drops start onto
	// the 8 Mbps 1080p sibling.
	startRequest.BandwidthCapKbps = new(80_000)
	startRequest.BandwidthEstimateKbps = new(12_000)

	ctx := newAuthorizedPlaybackContext()
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}
	started, err := handler.StartPlaybackV2(ctx, caller, startRequest)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start with cellular estimate: err=%v response=%#v", err, started)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	if started.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("start effective file = %d, want 8 Mbps sibling %d", started.PlaybackPlan.EffectiveMediaFileID, alternate.ID)
	}

	// Switching to Wi-Fi clears the cellular estimate while keeping the same
	// 80 Mbps cap; quality_change must reconsider the 64 Mbps requested source.
	replanReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationQualityChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "equal-cap-clear-estimate-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "equal-cap-attempt-0001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       30,
		QualityPreference:     playback.QualityAutoV3,
		BandwidthCapKbps:      new(80_000),
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		ClientFeatures:        startRequest.ClientFeatures,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	resp, err := handler.ReplanPlaybackV2(ctx, caller, started.SessionID, PlaybackReplanCommand{
		Request: replanReq,
		Digest:  "equal-cap-clear-estimate-digest",
	})
	if err != nil || resp.PlaybackPlan == nil {
		t.Fatalf("replan with equal cap and cleared estimate: err=%v response=%#v", err, resp)
	}
	if resp.PlaybackPlan.EffectiveMediaFileID != source.ID || resp.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("equal-cap Wi-Fi replan played file %d via %s, want 4K source %d via direct play",
			resp.PlaybackPlan.EffectiveMediaFileID, resp.PlaybackPlan.Delivery, source.ID)
	}
}

func TestMarkLearnedLimitUnderAutoWithBandwidthCap(t *testing.T) {
	source := &models.MediaFile{ID: 42, Bitrate: 25_000}
	limit := &playback.BandwidthLimitV3{Kbps: 5_000}
	req := playback.StartRequestV3{
		QualityPreference: playback.QualityAutoV3,
		BandwidthCapKbps:  new(10_000),
	}

	// A 6 Mbps sibling chosen when the learned limit is 5 Mbps (lower than the 10 Mbps cap):
	// markLearnedLimit marks it, leaving the caller's unlimited-plan comparison to clear it
	// only if the 10 Mbps cap alone would have made the same choice.
	res := playback.PlannerResultV3{
		Plan: &playback.PlanV3{
			EffectiveMediaFileID: 84,
			Delivery:             playback.DeliveryOriginalHTTPV3,
			EffectiveRecipe:      playback.EffectiveRecipeV3{BitrateKbps: new(6_000)},
		},
	}
	markLearnedLimit(&res, limit, source, req, 0)
	if res.Plan.BandwidthLimit == nil || res.Plan.BandwidthLimit.Kbps != 5_000 {
		t.Fatalf("Auto + 10 Mbps cap with 6 Mbps sibling got bandwidth_limit=%+v, want 5000", res.Plan.BandwidthLimit)
	}

	// If the chosen plan is a direct play of the requested source itself, no limit lowered it.
	directSource := playback.PlannerResultV3{
		Plan: &playback.PlanV3{
			EffectiveMediaFileID: 42,
			Delivery:             playback.DeliveryOriginalHTTPV3,
			EffectiveRecipe:      playback.EffectiveRecipeV3{BitrateKbps: new(25_000)},
		},
	}
	markLearnedLimit(&directSource, limit, source, req, 0)
	if directSource.Plan.BandwidthLimit != nil {
		t.Fatalf("direct play of requested source got bandwidth_limit=%+v, want nil", directSource.Plan.BandwidthLimit)
	}
}

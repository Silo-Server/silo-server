package main

import "github.com/Silo-Server/silo-server/internal/playback"

// goldenBufferingStartRequest is a buffering_quality_v1 start: Auto with the
// stored speed test reading, from a client whose setting is Never, so the
// server neither applies nor records a learned limit.
func goldenBufferingStartRequest() playback.StartRequestV3 {
	request := goldenStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureBufferingQualityV1)
	request.QualityPreference = playback.QualityAutoV3
	request.BandwidthEstimateKbps = new(48_000)
	request.BandwidthEstimateSource = playback.BandwidthEstimateProbeV3
	request.LearnedLimit = playback.LearnedLimitIgnoreV3
	return request
}

// goldenBufferingReplanRequest is the lowering a client sends after repeated
// buffering on Auto: 70% of the delivered bitrate, observed while rebuffering,
// with the lower throughput the stalled plan measured.
func goldenBufferingReplanRequest() playback.ReplanRequestV3 {
	request := goldenReplanRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureBufferingQualityV1)
	request.Operation = playback.ReplanOperationQualityChangeV3
	request.Failure = playback.FailureV3{}
	request.Metered = false
	request.BandwidthCapKbps = nil
	request.BandwidthEstimateKbps = new(5_600)
	request.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
	request.ObservedThroughputKbps = new(4_200)
	return request
}

// goldenLearnedLimitDecisionResponse is an /api/v2 Auto start under a learned
// limit, as the handler plans it: the limit replaces the estimate, the planner
// encodes below the source, and the plan reports the limit that lowered it.
func goldenLearnedLimitDecisionResponse() playback.DecisionResponseV3 {
	const limitKbps = 5_000
	request := goldenStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureBufferingQualityV1)
	request.QualityPreference = playback.QualityAutoV3
	request.BandwidthEstimateKbps = new(limitKbps)
	request.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = goldenHLSDelivery()
	decision := goldenDecisionForSource(conformanceFallbackFile(), request, nil)
	plan := decision.PlaybackPlan
	delivered := plan.Source.BitrateKbps
	if plan.EffectiveRecipe.BitrateKbps != nil {
		delivered = *plan.EffectiveRecipe.BitrateKbps
	}
	// The server reports a limit only on a plan it lowered.
	if delivered <= 0 || delivered >= plan.Source.BitrateKbps {
		failf("golden learned limit did not lower the plan: delivered %d of %d (%s)", delivered, plan.Source.BitrateKbps, plan.Delivery)
	}
	plan.Stream.URL = "/playback/transcode/" + goldenSessionID + "/master.m3u8"
	plan.BandwidthLimit = &playback.BandwidthLimitV3{Kbps: limitKbps, Source: playback.BandwidthEstimateObservedRebufferV3, ExpiresAt: "2030-01-07T23:55:00Z"}
	decision.ServerFeatures = playback.NativeServerFeaturesV3()
	return decision
}

// goldenPlanSummaryRouteEvent reports a plan that buffered and was lowered.
func goldenPlanSummaryRouteEvent() playback.RouteEventV3 {
	event := goldenRouteEvent()
	event.Event = playback.RouteEventPlanSummaryV3
	event.Diagnostics = map[string]string{
		"played_ms":        "600000",
		"stall_count":      "2",
		"stall_total_ms":   "8000",
		"stall_longest_ms": "6000",
		"delivered_kbps":   "8000",
		"throughput_kbps":  "6200",
		"buffering_mode":   "always",
		"buffering_action": "lowered",
	}
	return event
}

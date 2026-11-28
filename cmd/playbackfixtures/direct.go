package main

import "github.com/Silo-Server/silo-server/internal/playback"

// goldenDirectStartRequest is an /api/v2 start that asks for the direct media
// transport.
func goldenDirectStartRequest() playback.StartRequestV3 {
	request := goldenStartRequest()
	request.MediaTransport = playback.MediaTransportDirectV3
	return request
}

// goldenDirectDecisionResponse is an /api/v2 start the API host serves over the
// direct path: the plan names the transport its media travels on.
func goldenDirectDecisionResponse() playback.DecisionResponseV3 {
	decision := goldenNativeDecisionResponse()
	decision.PlaybackPlan.MediaTransport = playback.MediaTransportDirectV3
	decision.ServerFeatures = playback.NativeServerFeaturesV3()
	return decision
}

package apiv2

import (
	"net/http"
)

const playbackTestEvent = "55555555-5555-4555-8555-555555555555"

func playbackRouteEventFixtureCases() []fixtureCase {
	event := map[string]any{"installation_id": playbackTestInstallation, "event_id": playbackTestEvent, "protocol_version": 3, "playback_attempt_id": "attempt-0123456789", "session_id": "11111111-1111-4111-8111-111111111111", "event": "first_frame", "diagnostics": map[string]string{"decoder_name": "synthetic"}}
	return []fixtureCase{
		{name: "playback_route_event_accepted", operationID: "reportPlaybackRouteEvent", method: http.MethodPost, path: Prefix + "/playback/route-events", body: fixturePlaybackJSON(event), schema: "#/components/schemas/PlaybackRouteEventReceipt", scenario: "A diagnostic route event with a client-minted id is queued and acknowledged by that id.", status: 202, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}

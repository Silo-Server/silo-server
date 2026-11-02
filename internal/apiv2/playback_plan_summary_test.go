package apiv2

import (
	"net/http"
	"testing"
)

func TestNativePlanSummaryAccepted(t *testing.T) {
	deps, _ := catalogDeps(t)
	service := &fakePlaybackService{}
	deps.Playback = service
	body := `{"installation_id":"11111111-1111-4111-8111-111111111111","event_id":"22222222-2222-4222-8222-222222222222","protocol_version":3,"playback_attempt_id":"attempt-summary-1","event":"plan_summary","diagnostics":{"played_ms":"600000","stall_count":"2","stall_total_ms":"8000","buffering_mode":"always","buffering_action":"lowered"}}`
	response := do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/playback/route-events", body, viewerHeaders())
	if response.Code != http.StatusAccepted || service.event.Event.Event != "plan_summary" {
		t.Fatalf("summary: %d %s", response.Code, response.Body.String())
	}
}

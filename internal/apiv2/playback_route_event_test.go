package apiv2

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const playbackTestEvent = "55555555-5555-4555-8555-555555555555"

func playbackRouteEventFixture() map[string]any {
	return map[string]any{
		"installation_id":     playbackTestInstallation,
		"event_id":            playbackTestEvent,
		"protocol_version":    3,
		"playback_attempt_id": "attempt-0123456789",
		"session_id":          "11111111-1111-4111-8111-111111111111",
		"plan_id":             "plan-0123456789",
		"event":               "first_frame",
		"diagnostics":         map[string]string{"decoder_name": "synthetic", "unlisted": "dropped later by the service"},
	}
}

func TestPlaybackV2RouteEventIsAcceptedAndIdentified(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakePlaybackService{}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, Prefix+"/playback/route-events", playbackJSON(t, playbackRouteEventFixture()), viewerHeaders())
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"event_id":"`+playbackTestEvent+`"`) || !strings.Contains(rec.Body.String(), `"outcome":"accepted"`) {
		t.Fatalf("route event: %d %q", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 || fake.event.EventID != playbackTestEvent || fake.event.Event.Event != "first_frame" || fake.event.Event.SessionID != "11111111-1111-4111-8111-111111111111" || fake.event.Event.Diagnostics["decoder_name"] != "synthetic" || fake.caller.InstallationID != playbackTestInstallation {
		t.Fatalf("service call: %+v %+v", fake.caller, fake.event)
	}
	for name, mutate := range map[string]func(map[string]any){
		"event name":      func(b map[string]any) { b["event"] = "guessed" },
		"event id":        func(b map[string]any) { b["event_id"] = "not-a-uuid" },
		"missing id":      func(b map[string]any) { delete(b, "event_id") },
		"short attempt":   func(b map[string]any) { b["playback_attempt_id"] = "x" },
		"unknown field":   func(b map[string]any) { b["unexpected"] = 1 },
		"long class":      func(b map[string]any) { b["failure_classification"] = strings.Repeat("x", 65) },
		"too many quirks": func(b map[string]any) { b["applied_quirk_ids"] = make([]string, 17) },
	} {
		t.Run(name, func(t *testing.T) {
			calls := fake.calls
			body := playbackRouteEventFixture()
			mutate(body)
			rec := do(t, h, http.MethodPost, Prefix+"/playback/route-events", playbackJSON(t, body), viewerHeaders())
			if rec.Code != 422 || fake.calls != calls {
				t.Fatalf("validation: %d %s calls=%d", rec.Code, rec.Body.String(), fake.calls-calls)
			}
		})
	}
	fake.err = &handlers.PlaybackOperationError{Status: 429, Code: "event_rate_limited", Message: "drop"}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/playback/route-events", playbackJSON(t, playbackRouteEventFixture()), viewerHeaders()), TypeRateLimited)
	fake.err = &handlers.PlaybackOperationError{Status: 403, Code: "forbidden", Message: "not yours"}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/playback/route-events", playbackJSON(t, playbackRouteEventFixture()), viewerHeaders()), TypePermissionDenied)
	fake.err = nil
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/playback/route-events", playbackJSON(t, playbackRouteEventFixture()), nil), TypeAuthenticationRequired)
}

// TestPlaybackV2CallerResolvesClientIdentity pins where a v2 playback
// caller's app identity comes from. The first-party apps and the web player
// name themselves with X-Silo-Client* rather than the declared X-Client-*
// headers; the session label on the admin Activity page and the first-frame
// histogram's client label both depend on that identity reaching the service.
func TestPlaybackV2CallerResolvesClientIdentity(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakePlaybackService{}
	data, err := os.ReadFile("../playback/testdata/protocol_v3/decision_response.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fake.response); err != nil {
		t.Fatal(err)
	}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	siloApp := map[string]string{"X-Silo-Client": "Silo Android TV", "X-Silo-Client-Version": "1.0.0", "X-Silo-Client-Build": "5", "X-Silo-Client-Channel": "beta"}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    playback.ClientInfo
	}{
		{name: "first-party app", headers: siloApp, want: playback.ClientInfo{Name: "Silo Android TV", Version: "1.0.0", Build: "5", Channel: "beta"}},
		{name: "declared headers", headers: map[string]string{"X-Client-Name": "Third Party", "X-Client-Version": "2.1", "X-Client-Build": "77", "X-Client-Channel": "release"}, want: playback.ClientInfo{Name: "Third Party", Version: "2.1", Build: "77", Channel: "release"}},
		{name: "declared name wins as a set", headers: with(siloApp, "X-Client-Name", "Third Party"), want: playback.ClientInfo{Name: "Third Party"}},
		{name: "blank declared name", headers: with(with(siloApp, "X-Client-Name", " "), "X-Client-Version", "9"), want: playback.ClientInfo{Name: "Silo Android TV", Version: "1.0.0", Build: "5", Channel: "beta"}},
		{name: "clamped", headers: map[string]string{"X-Silo-Client": " " + strings.Repeat("n", 200) + " ", "X-Silo-Client-Channel": strings.Repeat("c", 40)}, want: playback.ClientInfo{Name: strings.Repeat("n", 128), Channel: strings.Repeat("c", 32)}},
		{name: "nameless", headers: map[string]string{}},
	} {
		for _, op := range []struct {
			name, path, body string
			status           int
		}{
			{name: "start", path: Prefix + "/playback/start", body: playbackJSON(t, playbackStartFixture(t)), status: 201},
			{name: "route event", path: Prefix + "/playback/route-events", body: playbackJSON(t, playbackRouteEventFixture()), status: 202},
		} {
			t.Run(tc.name+"/"+op.name, func(t *testing.T) {
				headers := viewerHeaders()
				for k, v := range tc.headers {
					headers[k] = v
				}
				fake.caller = handlers.PlaybackCaller{}
				rec := do(t, h, http.MethodPost, op.path, op.body, headers)
				if rec.Code != op.status {
					t.Fatalf("%s: %d %s", op.name, rec.Code, rec.Body.String())
				}
				got := playback.ClientInfo{Name: fake.caller.ClientName, Version: fake.caller.ClientVersion, Build: fake.caller.ClientBuild, Channel: fake.caller.ClientChannel}
				if got != tc.want {
					t.Fatalf("caller client = %+v; want %+v", got, tc.want)
				}
			})
		}
	}
}

func playbackRouteEventFixtureCases() []fixtureCase {
	event := map[string]any{"installation_id": playbackTestInstallation, "event_id": playbackTestEvent, "protocol_version": 3, "playback_attempt_id": "attempt-0123456789", "session_id": "11111111-1111-4111-8111-111111111111", "event": "first_frame", "diagnostics": map[string]string{"decoder_name": "synthetic"}}
	return []fixtureCase{
		{name: "playback_route_event_accepted", operationID: "reportPlaybackRouteEvent", method: http.MethodPost, path: Prefix + "/playback/route-events", body: fixturePlaybackJSON(event), schema: "#/components/schemas/PlaybackRouteEventReceipt", scenario: "A diagnostic route event with a client-minted id is queued and acknowledged by that id.", status: 202, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}

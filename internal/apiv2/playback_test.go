package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const playbackTestInstallation = "11111111-1111-4111-8111-111111111111"
const playbackTestStop = "22222222-2222-4222-8222-222222222222"

type fakePlaybackService struct {
	calls    int
	caller   handlers.PlaybackCaller
	request  playback.StartRequestV3
	progress handlers.PlaybackProgressCommand
	stop     handlers.PlaybackStopCommand
	replan   handlers.PlaybackReplanCommand
	event    handlers.PlaybackRouteEventCommand
	session  string
	response playback.DecisionResponseV3
	mutation handlers.PlaybackMutationView
	err      error
}

func (f *fakePlaybackService) PlaybackCapabilities(context.Context, int, string) (handlers.PlaybackCapabilitiesView, error) {
	return handlers.PlaybackCapabilitiesView{InstallationID: playbackTestInstallation, Revision: "cap-1", State: "available", Allowed: true, ProtocolVersions: []int{3}, Features: []string{"sequenced_progress_v1"}, Deliveries: []playback.DeliveryV3{playback.DeliveryOriginalHTTPV3, playback.DeliveryTranscodeHLSV3}}, f.err
}
func (f *fakePlaybackService) StartPlaybackV2(_ context.Context, caller handlers.PlaybackCaller, request playback.StartRequestV3) (playback.DecisionResponseV3, error) {
	f.calls++
	f.caller = caller
	f.request = request
	return f.response, f.err
}
func (f *fakePlaybackService) ApplyProgressV2(_ context.Context, caller handlers.PlaybackCaller, session string, command handlers.PlaybackProgressCommand) (handlers.PlaybackMutationView, error) {
	f.calls++
	f.caller = caller
	f.session = session
	f.progress = command
	return f.mutation, f.err
}
func (f *fakePlaybackService) ReplanPlaybackV2(_ context.Context, caller handlers.PlaybackCaller, session string, command handlers.PlaybackReplanCommand) (playback.DecisionResponseV3, error) {
	f.calls++
	f.caller = caller
	f.session = session
	f.replan = command
	return f.response, f.err
}
func (f *fakePlaybackService) ReportRouteEventV2(_ context.Context, caller handlers.PlaybackCaller, command handlers.PlaybackRouteEventCommand) error {
	f.calls++
	f.caller = caller
	f.event = command
	return f.err
}
func (f *fakePlaybackService) StopPlaybackV2(_ context.Context, caller handlers.PlaybackCaller, session string, command handlers.PlaybackStopCommand) (handlers.PlaybackMutationView, error) {
	f.calls++
	f.caller = caller
	f.session = session
	f.stop = command
	return f.mutation, f.err
}

func playbackStartFixture(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../playback/testdata/protocol_v3/start_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	body["file_id"] = "42"
	body["profile_id"] = "p-owner"
	body["installation_id"] = playbackTestInstallation
	return body
}
func playbackJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPlaybackV2CapabilitiesAndMissingConfiguration(t *testing.T) {
	deps, _ := catalogDeps(t)
	h := newTestHandler(t, deps)
	response := do(t, h, http.MethodGet, Prefix+"/playback/capabilities", "", viewerHeaders())
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"state":"not_configured"`) || !strings.Contains(response.Body.String(), `"allowed":false`) || strings.Contains(response.Body.String(), ":null") {
		t.Fatalf("unconfigured capability: %d %s", response.Code, response.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/playback/start", playbackJSON(t, playbackStartFixture(t)), viewerHeaders()), TypeCapabilityNotConfigured)
	deps.Playback = &fakePlaybackService{}
	response = do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/playback/capabilities", "", viewerHeaders())
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"installation_id":"`+playbackTestInstallation+`"`) || !strings.Contains(response.Body.String(), `"allowed":true`) {
		t.Fatalf("capability: %d %s", response.Code, response.Body.String())
	}
}

func TestPlaybackV2ProgressAndStop(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakePlaybackService{mutation: handlers.PlaybackMutationView{Outcome: "applied"}}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	response := do(t, h, http.MethodPost, Prefix+"/playback/session-1/progress", playbackJSON(t, map[string]any{"installation_id": playbackTestInstallation, "sequence": 42, "position": 0, "is_paused": true}), viewerHeaders())
	if response.Code != 200 || fake.progress.Sequence != 42 || fake.progress.Position != 0 || !fake.progress.IsPaused {
		t.Fatalf("progress: %d %s %+v", response.Code, response.Body.String(), fake.progress)
	}
	fake.mutation = handlers.PlaybackMutationView{Outcome: "stopped", StopID: playbackTestStop}
	stopBody := map[string]any{"installation_id": playbackTestInstallation, "stop_id": playbackTestStop}
	response = do(t, h, http.MethodDelete, Prefix+"/playback/session-1", playbackJSON(t, stopBody), viewerHeaders())
	if response.Code != 200 || fake.stop.Position != nil || fake.stop.Sequence != 0 || !strings.Contains(response.Body.String(), `"stop_id":"`+playbackTestStop+`"`) {
		t.Fatalf("stop: %d %s %+v", response.Code, response.Body.String(), fake.stop)
	}
	stopBody["sequence"] = 43
	stopBody["position"] = 0
	fake.mutation.Outcome = "replayed"
	response = do(t, h, http.MethodDelete, Prefix+"/playback/session-1", playbackJSON(t, stopBody), viewerHeaders())
	if response.Code != 200 || fake.stop.Position == nil || *fake.stop.Position != 0 || fake.stop.Sequence != 43 || !strings.Contains(response.Body.String(), `"outcome":"replayed"`) {
		t.Fatalf("final zero: %d %s %+v", response.Code, response.Body.String(), fake.stop)
	}
	// A final sample needs both members; sequence alone or position alone is refused before the service.
	calls := fake.calls
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/playback/session-1", playbackJSON(t, map[string]any{"installation_id": playbackTestInstallation, "stop_id": playbackTestStop, "sequence": 44}), viewerHeaders()), TypeValidationFailed)
	if fake.calls != calls {
		t.Fatal("half sample reached the service")
	}
	fake.err = &handlers.PlaybackOperationError{Status: 400, Code: "bad_request", Message: "Invalid playback sample"}
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/playback/session-1", playbackJSON(t, stopBody), viewerHeaders()), TypeValidationFailed)
}

func TestPlaybackV2ProblemMapping(t *testing.T) {
	for _, tc := range []struct {
		err  *handlers.PlaybackOperationError
		want ProblemType
	}{
		{&handlers.PlaybackOperationError{Status: http.StatusConflict, Code: "installation_changed", Message: "refresh capabilities"}, TypePlaybackInstallationChanged},
		{&handlers.PlaybackOperationError{Status: http.StatusConflict, Code: "capability_not_configured", Message: "no installation"}, TypeCapabilityNotConfigured},
		{&handlers.PlaybackOperationError{Status: http.StatusConflict, Code: "playback_attempt_reused", Message: "different request"}, TypeIdempotencyConflict},
		{&handlers.PlaybackOperationError{Status: http.StatusConflict, Code: "progress_conflict", Message: "same sequence, different sample"}, TypePlaybackProgressConflict},
		{&handlers.PlaybackOperationError{Status: http.StatusNotFound, Code: "session_not_found", Message: "gone"}, TypeNotFound},
		{&handlers.PlaybackOperationError{Status: http.StatusTooManyRequests, Code: "event_rate_limited", Message: "drop"}, TypeRateLimited},
		{&handlers.PlaybackOperationError{Status: http.StatusBadRequest, Code: "bad_request", Message: "invalid"}, TypeValidationFailed},
		{&handlers.PlaybackOperationError{Status: http.StatusUpgradeRequired, Code: "protocol_version_unsupported", Message: "v3 only"}, TypeValidationFailed},
		{&handlers.PlaybackOperationError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "store"}, TypeDependencyUnavailable},
	} {
		problem := playbackProblem(tc.err)
		if problem.Status != tc.want.Status || problem.Type != tc.want.URI() {
			t.Fatalf("%s: %+v", tc.err.Code, problem)
		}
	}
	if problem := playbackProblem(errors.New("opaque")); problem.Type != TypeDependencyUnavailable.URI() {
		t.Fatalf("opaque error: %+v", problem)
	}
}

// v2 drops the source entry, video or audio-only, without touching other
// entries or the shared plan that v1 serializes.
func TestPlaybackQualitiesDropsTheSourceEntry(t *testing.T) {
	plan := []playback.AvailableQualityV3{
		{Label: playback.QualityOriginalV3, Height: 2160, BitrateKbps: 40_000, PreservesSource: true},
		{Label: "1080p", DisplayName: "1080p", Height: 1080, BitrateKbps: 8_000},
		{Label: "1080p-medium", DisplayName: "1080p Medium", Height: 1080, BitrateKbps: 6_000},
	}
	got := playbackQualities(plan)
	if len(got) != 2 || got[0].Label != "1080p" || got[1].Label != "1080p-medium" {
		t.Fatalf("v2 qualities = %#v", got)
	}
	if len(plan) != 3 || plan[0].Label != playback.QualityOriginalV3 {
		t.Fatalf("shared plan changed: %#v", plan)
	}
	audio := playbackQualities([]playback.AvailableQualityV3{{Label: playback.QualityOriginalV3, BitrateKbps: 128, PreservesSource: true}})
	if len(audio) != 0 {
		t.Fatalf("audio-only menu = %#v, want empty", audio)
	}
	if playbackQualities(nil) != nil {
		t.Fatal("an absent menu stays absent")
	}
}

// TestPlaybackV2ProbeURLOrigin: the advertised probe URL names the origin the
// client reached. Behind a TLS-terminating reverse proxy that is the forwarded
// origin, not the internal host the proxy dialed; a client on the LAN keeps
// the address it used; and a proxy that rewrites Host to a bare IP without
// naming the client's host falls back to the configured public URL, except
// on an overlay provider's path, which keeps Host and may name an IP.
func TestPlaybackV2ProbeURLOrigin(t *testing.T) {
	const probe = Prefix + "/playback/bandwidth-probe?media_path=true"
	cases := []struct {
		name, host, publicURL string
		headers               map[string]string
		path                  netaccess.Path
		want                  string
	}{
		{"forwarded host", "10.0.0.5:8090", "https://public.example.test",
			map[string]string{"X-Forwarded-Host": "media.example.test", "X-Forwarded-Proto": "https", "X-Forwarded-For": "198.51.100.7"}, netaccess.Path{}, "https://media.example.test"},
		{"lan client on its address", "192.168.1.20:8090", "https://public.example.test", nil, netaccess.Path{}, "http://192.168.1.20:8090"},
		{"proxy rewrote host to an IP", "203.0.113.5:8090", "https://public.example.test/silo/",
			map[string]string{"X-Forwarded-For": "198.51.100.7"}, netaccess.Path{}, "https://public.example.test/silo"},
		{"proxy rewrote host, no public URL", "203.0.113.5:8090", "",
			map[string]string{"X-Forwarded-For": "198.51.100.7"}, netaccess.Path{}, "http://203.0.113.5:8090"},
		{"proxy kept a named host", "media.example.test", "https://public.example.test",
			map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Forwarded-Proto": "https"}, netaccess.Path{}, "https://media.example.test"},
		{"overlay provider on an IP", "100.64.0.9", "https://public.example.test",
			map[string]string{"X-Forwarded-For": "100.64.0.20", "X-Forwarded-Proto": "https"}, netaccess.Path{Provider: "tailscale", InstallationID: 1}, "https://100.64.0.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps, _ := catalogDeps(t)
			deps.Playback = &scheduleService{}
			deps.ServerConnections.PublicURL = func() string { return c.publicURL }
			r := httptest.NewRequest(http.MethodGet, "http://"+c.host+Prefix+"/playback/capabilities", nil)
			r = r.WithContext(netaccess.WithPath(r.Context(), c.path))
			for k, v := range viewerHeaders() {
				r.Header.Set(k, v)
			}
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			response := httptest.NewRecorder()
			newTestHandler(t, deps).ServeHTTP(response, r)
			var body PlaybackCapabilities
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
				t.Fatalf("capabilities: %d %s", response.Code, response.Body.String())
			}
			if want := c.want + probe; body.BandwidthProbeURL != want {
				t.Fatalf("bandwidth_probe_url = %q, want %q", body.BandwidthProbeURL, want)
			}
		})
	}
}

// TestPlaybackV2SpeedTestSchedule: with connection_speed_v1 the capability
// carries the speed test's schedule; automatic tests switched off still read
// false rather than vanishing, and a server with no target sends none. Without
// the feature none of it appears.
func TestPlaybackV2SpeedTestSchedule(t *testing.T) {
	read := func(service PlaybackService) map[string]any {
		t.Helper()
		deps, _ := catalogDeps(t)
		deps.Playback = service
		response := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/playback/capabilities", "", viewerHeaders())
		var body map[string]any
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("capabilities: %d %s", response.Code, response.Body.String())
		}
		return body
	}
	body := read(&scheduleService{view: handlers.PlaybackCapabilitiesView{BandwidthProbeMaxAgeSeconds: 86400}})
	if body["bandwidth_probe_automatic"] != false || body["bandwidth_probe_max_age_seconds"] != float64(86400) {
		t.Fatalf("schedule = %v", body)
	}
	if _, ok := body["bandwidth_probe_target_kbps"]; ok {
		t.Fatal("a zero target was sent")
	}
	body = read(&fakePlaybackService{})
	for _, field := range []string{"bandwidth_probe_automatic", "bandwidth_probe_max_age_seconds", "bandwidth_probe_target_kbps", "bandwidth_probe_url"} {
		if _, ok := body[field]; ok {
			t.Fatalf("%s sent without connection_speed_v1", field)
		}
	}
}

type scheduleService struct {
	fakePlaybackService
	view handlers.PlaybackCapabilitiesView
}

func (f *scheduleService) PlaybackCapabilities(ctx context.Context, userID int, profileID string) (handlers.PlaybackCapabilitiesView, error) {
	view, err := f.fakePlaybackService.PlaybackCapabilities(ctx, userID, profileID)
	view.Features = append(view.Features, playback.FeatureConnectionSpeedV1)
	view.BandwidthProbeAutomatic, view.BandwidthProbeMaxAgeSeconds, view.BandwidthProbeTargetKbps = f.view.BandwidthProbeAutomatic, f.view.BandwidthProbeMaxAgeSeconds, f.view.BandwidthProbeTargetKbps
	return view, err
}

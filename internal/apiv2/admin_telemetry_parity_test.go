package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeTelemetryParity struct {
	calls int
	view  handlers.StreamTelemetryParityView
}

func (f *fakeTelemetryParity) ReadStreamTelemetryParity(context.Context) handlers.StreamTelemetryParityView {
	f.calls++
	return f.view
}
func TestAdminTelemetryParityRead(t *testing.T) {
	f := new(fakeTelemetryParity)
	deps := pilotDeps(nil, nil)
	deps.AdminTelemetryParity = f
	h := NewHandler(deps)
	path := Prefix + "/admin/stream-telemetry/parity"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("refusal compared sources")
	}
	f.view = (&handlers.StreamTelemetryParityHandler{}).ReadStreamTelemetryParity(t.Context())
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	for _, want := range []string{`"enabled":false`, `"sources":[]`, `"incomplete_reasons":[]`, `"missing_publishers":[]`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	err := json.Unmarshal([]byte(`{"enabled":true,"view":{"available":true,"built_at":"2026-09-01T00:00:00.123456789Z","stale":true,"complete":false,"last_error":"private cache secret","missing_publishers":["missing"],"incomplete_reasons":["publisher missing"]},"sources":[{"source":"playback_sessions_sync","available":true,"report":{"source":"playback_sessions_sync","agrees":true,"legacy_count":60000,"fields_absent":{"node":1},"legacy_only_truncated":2}},{"source":"node_sessions_redis","available":false,"error":"private Redis secret"}]}`), &f.view)
	if err != nil {
		t.Fatal(err)
	}
	f.view.Sources[0].LegacyMayBeTruncated = true
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	body := rec.Body.String()
	for _, want := range []string{`"built_at":"2026-09-01T00:00:00.123Z"`, `"stale":true`, `"complete":false`, `"legacy_may_be_truncated":true`, `"legacy_scan_limit":60000`, `"fields_absent":{"node":1}`, `"legacy_only_truncated":2`, `Legacy comparison source unavailable.`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatal(want, rec.Code, body)
		}
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "private") {
		t.Fatal(body)
	}
	deps.AdminTelemetryParity = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

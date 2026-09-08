package apiv2

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// newTestHandler builds the real listener with the probe operations.
func newTestHandler(t *testing.T, deps Dependencies) http.Handler {
	t.Helper()
	deps.testRegister = registerProbes
	return NewHandler(deps)
}

type problemDoc struct {
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Status   int            `json:"status"`
	Detail   string         `json:"detail"`
	Instance string         `json:"instance"`
	Errors   []ProblemError `json:"errors"`
	Extra    map[string]any `json:"-"`
}

// requestIDHeader reads the X-Request-ID header in the contract's exact
// spelling; Header.Get would canonicalize the name to X-Request-Id.
func requestIDHeader(rec *httptest.ResponseRecorder) string {
	vs := rec.Header()[RequestIDHeader] //nolint:staticcheck // contract spelling, set through the map
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

func do(t *testing.T, h http.Handler, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	if body != "" && headers["Content-Type"] == "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// requireProblem decodes a problem response and checks the envelope rules
// every problem must satisfy.
func requireProblem(t *testing.T, rec *httptest.ResponseRecorder, want ProblemType) problemDoc {
	t.Helper()
	if rec.Code != want.Status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want.Status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	if _, has := raw["$schema"]; has {
		t.Fatalf("problem carries $schema: %s", rec.Body.String())
	}
	var p problemDoc
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Type != want.URI() || p.Title != want.Title || p.Status != want.Status || p.Detail == "" {
		t.Fatalf("envelope = %+v, want type %s", p, want.URI())
	}
	id := requestIDHeader(rec)
	if id == "" || p.Instance != "urn:silo:request:"+id {
		t.Fatalf("instance %q does not match X-Request-ID %q", p.Instance, id)
	}
	for _, e := range p.Errors {
		if e.Location == "" || e.Code == "" || e.Detail == "" {
			t.Fatalf("incomplete error entry: %+v", e)
		}
		if !strings.HasPrefix(e.Location, "body") && !strings.HasPrefix(e.Location, "query.") &&
			!strings.HasPrefix(e.Location, "path.") && !strings.HasPrefix(e.Location, "header.") {
			t.Fatalf("location grammar: %q", e.Location)
		}
	}
	if strings.Contains(rec.Body.String(), `"value"`) {
		t.Fatalf("problem echoes a rejected value: %s", rec.Body.String())
	}
	return p
}

// --- Mount and registration -------------------------------------------------

// TestRuntimeReconcile walks the real assembled router and asserts the routes
// it serves are exactly the set the registry and plugin-content extension declare.
func TestRuntimeReconcile(t *testing.T) {
	var declared []Declared
	deps := Dependencies{testRegister: func(reg *Registry) {
		registerProbes(reg)
		declared = reg.Declared()
	}}
	router := newChiRouter(deps)
	observed, err := routeinventory.Observed(router)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, d := range declared {
		want[d.Method+" "+d.Path] = true
	}
	// Dynamic plugin mounts are declared by the closed extension inventory,
	// not by Huma operations. Keep expectations independent of the router walk.
	for _, mount := range describePluginContent().Mounts {
		for _, method := range mount.Methods {
			key := method + " " + mount.Path
			if want[key] {
				t.Fatalf("duplicate extension declaration: %s", key)
			}
			want[key] = true
		}
	}
	got := map[string]bool{}
	for _, o := range observed {
		got[o] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("declared but not served: %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("served but not declared: %s", k)
		}
	}
	if !want["GET /api/v2/system/info"] {
		t.Error("getSystemInfo not declared")
	}
	for _, o := range observed {
		if !strings.HasPrefix(o[strings.Index(o, " ")+1:], Prefix+"/") {
			t.Errorf("route outside %s: %s", Prefix, o)
		}
	}
}

// TestCommittedArtifactMatchesRouter is the route/spec reconciliation over
// the production wiring: every route the real assembled router serves is an
// operation in the COMMITTED contracts/api/v2/openapi.json or a manual
// registry entry, and vice versa. A stale artifact fails here as well as in
// make verify-apiv2-openapi.
func TestCommittedArtifactMatchesRouter(t *testing.T) {
	observed, err := routeinventory.Observed(newChiRouter(Dependencies{}))
	if err != nil {
		t.Fatal(err)
	}

	unaccounted, unserved, err := reconcileSpec(observed, contracts.OpenAPI, RawHandshakes())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range unaccounted {
		t.Errorf("served but in neither openapi.json nor the manual registry: %s", r)
	}
	for _, r := range unserved {
		t.Errorf("documented but not served: %s", r)
	}
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, contracts.OpenAPI) {
		t.Fatal("contracts/api/v2/openapi.json is stale; run make apiv2-openapi")
	}
	if len(RawHandshakes()) != 0 {
		t.Fatalf("the manual registry is expected to be empty until a raw handshake is ratified: %+v", RawHandshakes())
	}
}

// TestReconcileSpecSeeded proves the reconciliation fires in each direction
// and that a manual-registry entry accounts for a raw route: the one
// test-only entry the placeholder registry carries.
func TestReconcileSpecSeeded(t *testing.T) {
	ws := RawHandshake{Method: http.MethodGet, Path: Prefix + "/probe/ws", Protocol: "websocket", Reason: "test-only raw handshake"}
	observed := []string{
		"POST /api/v2/admin/nodes/{id}/check",
		"POST /api/v2/admin/nodes/{id}/reprobe",
		"POST /api/v2/admin/nodes/force-reload",
		"POST /api/v2/admin/nodes/{id}/force-reload",
		"POST /api/v2/admin/nodes",
		"PUT /api/v2/admin/nodes/{id}",
		"DELETE /api/v2/admin/nodes/{id}",
		"POST /api/v2/admin/branding/assets/{kind}",
		"DELETE /api/v2/admin/branding/assets/{kind}",
		"POST /api/v2/admin/jellyfin-compat/web/install",
		"POST /api/v2/admin/jellyfin-compat/web/remove",
		"POST /api/v2/admin/server/restart",
		"GET /api/v2/admin/playback-history",
		"PUT /api/v2/notifications/email-preferences/address",
		"GET /api/v2/notifications/email-preferences/address/capabilities",
		"GET /api/v2/direct-download",
		"HEAD /api/v2/direct-download",
		"GET /api/v2/direct-download-proxy",
		"HEAD /api/v2/direct-download-proxy",
		"POST /api/v2/admin/autoscan/trigger",
		"POST /api/v2/admin/autoscan/sources/{id}/webhook",
		"DELETE /api/v2/admin/autoscan/sources/{id}/webhook",
		"POST /api/v2/admin/autoscan/sources/{id}/webhook/rotate",
		"POST /api/v2/devices/push/apple",
		"GET /api/v2/devices/push/apple/capabilities",
		"DELETE /api/v2/subtitles/stored/{id}",
		"GET /api/v2/subtitles/stored/{id}/metadata",
		"PUT /api/v2/admin/notifications/server-channels/{id}",
		"GET /api/v2/admin/dashboard/layout",
		"PUT /api/v2/admin/dashboard/layout",
		"DELETE /api/v2/admin/dashboard/layout",
		"POST /api/v2/admin/notifications/server-channels/{id}/rotate-secret",
		"GET /api/v2/admin/autoscan/scan-source-plugins",
		"GET /api/v2/admin/autoscan/sources/{id}/rewrite-suggestions",
		"POST /api/v2/auth/plugin-launch",
		"GET /api/v2/admin/plugins/catalog",
		"GET /api/v2/admin/plugins/installations",
		"PUT /api/v2/admin/plugins/installations/{id}/config",
		"POST /api/v2/admin/plugins/installations/{id}/config/test",
		"PUT /api/v2/admin/plugins/installations/{id}/auth-binding",
		"PUT /api/v2/admin/plugins/installations/{id}/task-bindings/{capability_id}",
		"POST /api/v2/admin/plugins/installations",
		"PUT /api/v2/admin/plugins/installations/{id}",
		"DELETE /api/v2/admin/plugins/installations/{id}",
		"POST /api/v2/admin/plugins/installations/{id}/update",
		"POST /api/v2/admin/plugins/uploads",
		"POST /api/v2/admin/plugins/uploads/chunked",
		"PUT /api/v2/admin/plugins/uploads/chunked/{upload_id}/chunks/{chunk_index}",
		"POST /api/v2/admin/plugins/uploads/chunked/{upload_id}/complete",
		"DELETE /api/v2/admin/plugins/uploads/chunked/{upload_id}",
		"POST /api/v2/subtitles/ai/translate",
		"POST /api/v2/subtitles/ai/jobs/{job_id}/cancel",
		"CONNECT /api/v2/plugin-content/plugins/{installation_id}",
		"CONNECT /api/v2/plugin-content/plugins/{installation_id}/*",
		"DELETE /api/v2/plugin-content/plugins/{installation_id}",
		"DELETE /api/v2/plugin-content/plugins/{installation_id}/*",
		"GET /api/v2/plugin-content/plugins/{installation_id}",
		"GET /api/v2/plugin-content/plugins/{installation_id}/*",
		"HEAD /api/v2/plugin-content/plugins/{installation_id}",
		"HEAD /api/v2/plugin-content/plugins/{installation_id}/*",
		"OPTIONS /api/v2/plugin-content/plugins/{installation_id}",
		"OPTIONS /api/v2/plugin-content/plugins/{installation_id}/*",
		"PATCH /api/v2/plugin-content/plugins/{installation_id}",
		"PATCH /api/v2/plugin-content/plugins/{installation_id}/*",
		"POST /api/v2/plugin-content/plugins/{installation_id}",
		"POST /api/v2/plugin-content/plugins/{installation_id}/*",
		"PUT /api/v2/plugin-content/plugins/{installation_id}",
		"PUT /api/v2/plugin-content/plugins/{installation_id}/*",
		"TRACE /api/v2/plugin-content/plugins/{installation_id}",
		"TRACE /api/v2/plugin-content/plugins/{installation_id}/*",
		"GET /api/v2/plugin-content/plugin-assets/{installation_id}",
		"GET /api/v2/plugin-content/plugin-assets/{installation_id}/*",

		"GET " + Prefix + "/admin/subtitles",
		"GET /api/v2/admin/subtitles/{id}",
		"DELETE /api/v2/admin/subtitles/{id}",
		"PATCH /api/v2/admin/subtitles/{id}",
		"GET /api/v2/admin/subtitles/{id}/download",
		"DELETE " + Prefix + "/watch-together/rooms/{room_id}/suggestions/{suggestion_id}",
		"GET " + Prefix + "/plugin-content/capabilities",
		"GET " + Prefix + "/onboarding/capabilities",
		"GET " + Prefix + "/onboarding/flow",
		"GET " + Prefix + "/onboarding/state",
		"PUT " + Prefix + "/onboarding/progress",
		"POST " + Prefix + "/downloads",
		"GET " + Prefix + "/admin/rate-limits/config",
		"GET " + Prefix + "/admin/rate-limits/status",
		"PATCH " + Prefix + "/admin/rate-limits/config",
		"GET " + Prefix + "/admin/settings/{key}",
		"GET " + Prefix + "/admin/settings/sections",
		"GET " + Prefix + "/admin/playback-routing/capabilities",
		"GET " + Prefix + "/admin/jellyfin-compat/status",
		"PATCH /api/v2/admin/jellyfin-compat/settings",
		"GET /api/v2/admin/dashboard/capabilities",
		"GET /api/v2/admin/logs/app",
		"GET /api/v2/admin/logs/audit",
		"POST " + Prefix + "/downloads/subscriptions",
		"PATCH " + Prefix + "/downloads/subscriptions/{id}",
		"DELETE " + Prefix + "/downloads/subscriptions/{id}",
		"POST " + Prefix + "/downloads/subscriptions/sync",
		"GET " + Prefix + "/diagnostics/capabilities",
		"POST " + Prefix + "/diagnostics/reports",
		"POST " + Prefix + "/diagnostics/reports/uploads",
		"PUT " + Prefix + "/diagnostics/reports/uploads/{upload_id}/chunks/{chunk_index}",
		"POST " + Prefix + "/diagnostics/reports/uploads/{upload_id}/complete",
		"DELETE " + Prefix + "/diagnostics/reports/uploads/{upload_id}",
		"POST " + Prefix + "/notifications/discord/link/init",
		"GET " + Prefix + "/notifications/discord/link/callback",
		"POST " + Prefix + "/notifications/webhooks",
		"POST " + Prefix + "/admin/notifications/server-channels",
		"GET " + Prefix + "/events/capabilities",
		"GET " + Prefix + "/watch-together/rooms/{room_id}/suggestions",
		"POST " + Prefix + "/watch-together/rooms/{room_id}/suggestions/{suggestion_id}/vote",
		"DELETE " + Prefix + "/watch-together/rooms/{room_id}/suggestions/{suggestion_id}/vote",
		"GET " + Prefix + "/scan/capabilities",
		"POST " + Prefix + "/scan",
		"POST " + Prefix + "/scan/cancel",
		"GET " + Prefix + "/admin/stats",
		"PUT " + Prefix + "/admin/settings/sections",
		"PUT " + Prefix + "/admin/settings",
		"PUT " + Prefix + "/admin/settings/{key}",
		"GET " + Prefix + "/admin/sessions",
		"GET " + Prefix + "/admin/sessions/capabilities",
		"GET " + Prefix + "/admin/sessions/command-capabilities",
		"POST " + Prefix + "/admin/sessions/{session_id}/pause",
		"POST " + Prefix + "/admin/sessions/{session_id}/resume",
		"POST " + Prefix + "/admin/sessions/{session_id}/stop",
		"POST " + Prefix + "/admin/sessions/{session_id}/message",
		"POST " + Prefix + "/admin/sessions/{session_id}/terminate",
		"GET " + Prefix + "/admin/node-sessions",
		"GET " + Prefix + "/autoscan/capabilities",
		"POST " + Prefix + "/autoscan/webhooks/{token}",
		"GET " + Prefix + "/admin/autoscan/sources",
		"GET " + Prefix + "/admin/autoscan/settings",
		"GET " + Prefix + "/admin/autoscan/status",
		"GET " + Prefix + "/admin/autoscan/connections",
		"GET " + Prefix + "/events/ws",
		"GET " + Prefix + "/playback/sessions/control/capabilities",
		"POST " + Prefix + "/playback/sessions/{session_id}/control/ws-ticket",
		"GET " + Prefix + "/playback/sessions/{session_id}/control/ws",
		"POST " + Prefix + "/events/ws-ticket",
		"GET " + Prefix + "/admin/logs/ws",
		"POST " + Prefix + "/admin/logs/ws-ticket",
		"GET " + Prefix + "/admin/logs/ws/capabilities",
		"GET " + Prefix + "/admin/items/{id}/files",
		"POST " + Prefix + "/admin/items/{id}/merge",
		"POST " + Prefix + "/admin/items/{id}/split",
		"POST " + Prefix + "/admin/items/{id}/match/search",
		"POST " + Prefix + "/admin/items/{id}/match/apply",
		"GET " + Prefix + "/admin/items/{id}/images",
		"POST " + Prefix + "/admin/items/{id}/images/apply",
		"GET " + Prefix + "/admin/unmatched",
		"GET " + Prefix + "/admin/subtitle-providers",
		"GET /api/v2/admin/subtitle-providers/{provider}",
		"PUT /api/v2/admin/subtitle-providers/{provider}",
		"DELETE /api/v2/watch-together/rooms/{room_id}",
		"GET /api/v2/watch-together/rooms/{room_id}",
		"POST /api/v2/watch-together/join",
		"POST /api/v2/watch-together/rooms",
		"POST /api/v2/watch-together/rooms/{room_id}/ws-ticket",
		"GET /api/v2/watch-together/rooms/{room_id}/ws",
		"POST /api/v2/watch-together/rooms/{room_id}/suggestions",
		"POST /api/v2/watch-together/rooms/{room_id}/suggestions/promote",
		"PUT /api/v2/watch-together/rooms/{room_id}/selection",
		"PATCH /api/v2/watch-together/rooms/{room_id}/policy",
		"POST " + Prefix + "/admin/subtitle-providers/{provider}/test",

		"GET " + Prefix + "/stream/{session_id}",
		"HEAD " + Prefix + "/stream/{session_id}",
		"GET " + Prefix + "/playback/transcode/{session_id}/master.m3u8",
		"GET " + Prefix + "/playback/transcode/{session_id}/segment/{name}",
		"GET " + Prefix + "/stream/{session_id}/subtitles/{track}",
		"HEAD " + Prefix + "/stream/{session_id}/subtitles/{track}",
		"GET " + Prefix + "/stream/{session_id}/subtitles/{track}/fonts",
		"GET " + Prefix + "/subtitles/providers/status",
		"GET " + Prefix + "/subtitles/ai/status",
		"GET " + Prefix + "/subtitles/{media_file_id}",
		"POST " + Prefix + "/subtitles/search",
		"POST " + Prefix + "/subtitles/download",
		"POST " + Prefix + "/subtitles/upload",
		"POST " + Prefix + "/subtitles/detect-language",
		"GET " + Prefix + "/account/me",
		"GET " + Prefix + "/admin/users",
		"GET " + Prefix + "/openapi.json",
		"GET " + Prefix + "/profile/sections",
		"PUT " + Prefix + "/profile/sections",
		"DELETE " + Prefix + "/profile/sections",
		"GET " + Prefix + "/profile/sections/flags",
		"GET " + Prefix + "/profile/sections/settings",
		"GET " + Prefix + "/profiles",
		"POST " + Prefix + "/profiles",
		"PATCH " + Prefix + "/profiles/{id}",
		"DELETE " + Prefix + "/profiles/{id}",
		"GET " + Prefix + "/profiles/household/sessions",
		"POST " + Prefix + "/profiles/{id}/verify-pin",
		"PUT " + Prefix + "/profiles/{id}/avatar",
		"DELETE " + Prefix + "/profiles/{id}/avatar",
		"GET " + Prefix + "/progress",
		"GET " + Prefix + "/webhook-sync/connections",
		"POST " + Prefix + "/webhook-sync/connections",
		"PUT " + Prefix + "/webhook-sync/connections/{id}",
		"DELETE " + Prefix + "/webhook-sync/connections/{id}",
		"POST " + Prefix + "/webhook-sync/connections/{id}/webhook/rotate",
		"GET " + Prefix + "/webhook-sync/connections/{id}/profile-mappings",
		"PUT " + Prefix + "/webhook-sync/connections/{id}/profile-mappings",
		"GET " + Prefix + "/webhook-sync/connections/{id}/events",
		"GET " + Prefix + "/history-imports/sources", "GET " + Prefix + "/history-imports/runs", "POST " + Prefix + "/history-imports/runs",
		"GET " + Prefix + "/history-imports/runs/{id}", "POST " + Prefix + "/history-imports/plex/auth/pin", "POST " + Prefix + "/history-imports/plex/auth/check", "POST " + Prefix + "/history-imports/emby-connect/login",
		"GET " + Prefix + "/system/info",
		"GET " + Prefix + "/system/setup",
		"GET " + Prefix + "/audio-prefs/{series_id}",
		"PUT " + Prefix + "/audio-prefs/{series_id}",
		"DELETE " + Prefix + "/audio-prefs/{series_id}",
		"GET " + Prefix + "/subtitle-prefs/{series_id}",
		"PUT " + Prefix + "/subtitle-prefs/{series_id}",
		"DELETE " + Prefix + "/subtitle-prefs/{series_id}",
		"GET " + Prefix + "/library-playback-prefs",
		"PATCH " + Prefix + "/library-playback-prefs/{library_id}",
		"DELETE " + Prefix + "/library-playback-prefs/{library_id}",
		"GET " + Prefix + "/libraries",
		"POST " + Prefix + "/libraries",
		"PATCH " + Prefix + "/libraries/{id}",
		"DELETE " + Prefix + "/libraries/{id}",
		"POST " + Prefix + "/libraries/{id}/check-mount",
		"GET " + Prefix + "/libraries/metadata-match-queue",
		"GET " + Prefix + "/libraries/provider-defaults",
		"POST " + Prefix + "/libraries/reorder",
		"GET " + Prefix + "/libraries/roots",
		"PUT " + Prefix + "/libraries/roots/override",
		"DELETE " + Prefix + "/libraries/roots/override",
		"GET " + Prefix + "/libraries/skipped-roots",
		"GET " + Prefix + "/libraries/stale-ids",
		"POST " + Prefix + "/libraries/stale-ids/{content_id}/rematch",
		"GET " + Prefix + "/libraries/unmatched-items",
		"POST " + Prefix + "/libraries/{id}/confirm-empty-root-cleanup",
		"GET " + Prefix + "/libraries/{id}/metadata-match-queue",
		"POST " + Prefix + "/libraries/{id}/metadata-match-queue/retry",
		"POST " + Prefix + "/libraries/{id}/metadata-match-queue/cancel",
		"POST " + Prefix + "/libraries/{id}/refresh-metadata",
		"GET " + Prefix + "/libraries/{id}/providers",
		"PUT " + Prefix + "/libraries/{id}/providers",
		"PUT " + Prefix + "/libraries/{id}/poster",
		"DELETE " + Prefix + "/libraries/{id}/poster",
		"GET " + Prefix + "/library/{id}/layout",
		"GET " + Prefix + "/library/{id}/sections",
		"GET " + Prefix + "/library/{id}/sections/{section_id}/items",
		"GET " + Prefix + "/library/{id}/collections",
		"GET " + Prefix + "/library/{id}/user-collections",
		"GET " + Prefix + "/settings/contract",
		"GET " + Prefix + "/settings/contract/capabilities",
		"GET " + Prefix + "/settings/overlay-config",
		"GET " + Prefix + "/settings/subtitle-appearance/effective",
		"PUT " + Prefix + "/settings/device/subtitle-appearance",
		"DELETE " + Prefix + "/settings/device/subtitle-appearance",
		"GET " + Prefix + "/settings/plugins",
		"GET " + Prefix + "/settings/plugins/{installation_id}",
		"PUT " + Prefix + "/settings/plugins/{installation_id}",
		"GET " + Prefix + "/settings/values",
		"GET " + Prefix + "/settings/values/effective",
		"POST " + Prefix + "/settings/values/effective",
		"PUT " + Prefix + "/settings/values/nav.shortcuts/item",
		"GET " + Prefix + "/settings/values/{key}",
		"PUT " + Prefix + "/settings/values/{key}",
		"DELETE " + Prefix + "/settings/values/{key}",
		"GET " + Prefix + "/calendar",
		"PUT " + Prefix + "/home/dismissals/{surface}/{item_id}",
		"DELETE " + Prefix + "/home/dismissals/{surface}/{item_id}",
		"GET " + Prefix + "/home/layout",
		"GET " + Prefix + "/home/sections",
		"GET " + Prefix + "/home/sections/{id}/items",
		"GET " + Prefix + "/sections/recipes",
		"GET " + Prefix + "/sections/recipes/{type}/candidates",
		"GET " + Prefix + "/favorites",
		"GET " + Prefix + "/favorites/{item_id}",
		"PUT " + Prefix + "/favorites/{item_id}",
		"DELETE " + Prefix + "/favorites/{item_id}",
		"GET " + Prefix + "/ratings",
		"GET " + Prefix + "/ratings/{item_id}",
		"PUT " + Prefix + "/ratings/{item_id}",
		"DELETE " + Prefix + "/ratings/{item_id}",
		"GET " + Prefix + "/watchlist",
		"GET " + Prefix + "/watchlist/{item_id}",
		"PUT " + Prefix + "/watchlist/{item_id}",
		"DELETE " + Prefix + "/watchlist/{item_id}",
		"GET " + Prefix + "/history",
		"POST " + Prefix + "/history/remove",
		"POST " + Prefix + "/sync/progress",
		"GET " + Prefix + "/watch/{id}",
		"POST " + Prefix + "/watched/{id}",
		"DELETE " + Prefix + "/watched/{id}",
		"GET " + Prefix + "/recommendations/because-watched/{item_id}",
		"GET " + Prefix + "/recommendations/discover",
		"GET " + Prefix + "/recommendations/for-you/main",
		"GET " + Prefix + "/recommendations/for-you/rows",
		"GET " + Prefix + "/recommendations/popular",
		"GET " + Prefix + "/recommendations/recently-added",
		"GET " + Prefix + "/recommendations/section/{kind}",
		"GET " + Prefix + "/recommendations/similar/{item_id}",
		"GET " + Prefix + "/recommendations/similar-users",
		"GET " + Prefix + "/library-jobs/{job_id}",
		"POST " + Prefix + "/library-jobs/{job_id}/cancel",
		"GET " + Prefix + "/recommendations/taste-profile",
		"GET " + Prefix + "/recommendations/taste-seed/items",
		"POST " + Prefix + "/recommendations/taste-seed",
		"GET " + Prefix + "/recommendations/watch-tonight",
		"GET " + Prefix + "/recommendations/watch-tonight/cards",
		"POST " + Prefix + "/requests",
		"GET " + Prefix + "/requests/mine",
		"GET " + Prefix + "/requests/status",
		"POST " + Prefix + "/requests/{id}/cancel",
		"GET " + Prefix + "/admin/requests/capabilities",
		"GET " + Prefix + "/admin/requests",
		"GET " + Prefix + "/admin/request-settings",
		"PUT " + Prefix + "/admin/request-settings",
		"GET " + Prefix + "/admin/request-users/{user_id}/limit",
		"PUT " + Prefix + "/admin/request-users/{user_id}/limit",
		"GET " + Prefix + "/admin/request-integrations",
		"POST " + Prefix + "/admin/request-integrations",
		"GET " + Prefix + "/admin/request-integrations/{id}",
		"PUT " + Prefix + "/admin/request-integrations/{id}",
		"DELETE " + Prefix + "/admin/request-integrations/{id}",
		"POST " + Prefix + "/admin/request-integrations/{id}/options",
		"POST " + Prefix + "/admin/requests/{id}/approve",
		"POST " + Prefix + "/admin/requests/{id}/decline",
		"POST " + Prefix + "/admin/requests/{id}/cancel",
		"POST " + Prefix + "/admin/requests/{id}/retry",
		"GET " + Prefix + "/watch-providers",
		"GET " + Prefix + "/watch-providers/{provider}/connection",
		"GET " + Prefix + "/watch-providers/{provider}/connection/settings",
		"PATCH " + Prefix + "/watch-providers/{provider}/connection",
		"DELETE " + Prefix + "/watch-providers/{provider}/connection",
		"POST " + Prefix + "/watch-providers/{provider}/auth/device-code",
		"POST " + Prefix + "/watch-providers/{provider}/auth/poll",
		"POST " + Prefix + "/watch-providers/{provider}/auth/api-key",
		"POST " + Prefix + "/watch-providers/{provider}/sync",
		"GET " + Prefix + "/watch-providers/{provider}/sync-runs",
		"GET " + Prefix + "/requests/{id}",
		"GET " + Prefix + "/requests/search",
		"GET " + Prefix + "/requests/detail/{media_type}/{tmdb_id}",
		"GET " + Prefix + "/requests/discover",
		"GET " + Prefix + "/requests/discover/{section}",
		"GET " + Prefix + "/requests/discover/genres",
		"GET " + Prefix + "/requests/discover/networks",
		"GET " + Prefix + "/requests/discover/studios",
		"GET " + Prefix + "/requests/discover/browse/genre/{slug}",
		"GET " + Prefix + "/requests/discover/browse/network/{slug}",
		"GET " + Prefix + "/requests/discover/browse/studio/{slug}",
		"GET " + Prefix + "/library/{id}/collections/{collection_id}/items",
		"GET " + Prefix + "/collections",
		"POST " + Prefix + "/collections",
		"GET " + Prefix + "/devices",
		"DELETE " + Prefix + "/devices/{device_id}",
		"DELETE " + Prefix + "/devices/{device_id}/settings",
		"GET " + Prefix + "/collections/capabilities",
		"POST " + Prefix + "/collections/groups",
		"GET " + Prefix + "/collections/groups/order",
		"PUT " + Prefix + "/collections/groups/order",
		"DELETE " + Prefix + "/collections/groups/{id}",
		"GET " + Prefix + "/collections/groups/{id}",
		"PATCH " + Prefix + "/collections/groups/{id}",
		"POST " + Prefix + "/collections/import/mdblist",
		"GET " + Prefix + "/collections/import/mdblist/search",
		"GET " + Prefix + "/collections/import/mdblist/top",
		"POST " + Prefix + "/collections/import/tmdb",
		"POST " + Prefix + "/collections/import/trakt",
		"GET " + Prefix + "/collections/order",
		"PUT " + Prefix + "/collections/order",
		"POST " + Prefix + "/collections/preview",
		"GET " + Prefix + "/collections/server",
		"DELETE " + Prefix + "/collections/sort-preference",
		"PUT " + Prefix + "/collections/sort-preference",
		"GET " + Prefix + "/collections/templates",
		"DELETE " + Prefix + "/collections/{id}",
		"GET " + Prefix + "/collections/{id}",
		"PATCH " + Prefix + "/collections/{id}",
		"DELETE " + Prefix + "/collections/{id}/image",
		"GET " + Prefix + "/collections/{id}/items",
		"GET " + Prefix + "/collections/{id}/items/order",
		"PUT " + Prefix + "/collections/{id}/items/order",
		"DELETE " + Prefix + "/collections/{id}/items/{item_id}",
		"PUT " + Prefix + "/collections/{id}/items/{item_id}",
		"PUT " + Prefix + "/collections/{id}/poster",
		"POST " + Prefix + "/collections/{id}/sync",
		"GET " + Prefix + "/markers/files/{file_id}",
		"GET " + Prefix + "/markers/items/{item_id}",
		"PUT " + Prefix + "/markers/files/{file_id}",
		"PUT " + Prefix + "/markers/items/{item_id}",
		"DELETE " + Prefix + "/markers/files/{file_id}/{segment}",
		"GET " + Prefix + "/catalog/search/capabilities", "GET " + Prefix + "/catalog", "GET " + Prefix + "/catalog/audiobook-groups", "GET " + Prefix + "/catalog/filters", "GET " + Prefix + "/catalog/filters/search", "POST " + Prefix + "/catalog/query",
		"GET " + Prefix + "/catalog/items/{id}", "GET " + Prefix + "/catalog/items/{id}/episodes", "GET " + Prefix + "/catalog/items/{id}/manga-files", "GET " + Prefix + "/catalog/items/{id}/versions",
		"GET " + Prefix + "/catalog/series/{id}/seasons", "GET " + Prefix + "/catalog/series/{id}/seasons/{num}", "GET " + Prefix + "/catalog/series/{id}/seasons/{num}/episodes",
		"GET " + Prefix + "/capabilities/trailers", "POST " + Prefix + "/catalog/items/{id}/trailers/refresh", "GET " + Prefix + "/capabilities/metadata-ai", "POST " + Prefix + "/catalog/items/{id}/translate-description",
		"GET " + Prefix + "/catalog/people", "GET " + Prefix + "/catalog/people/{id}", "POST " + Prefix + "/catalog/people/{id}/refresh", "GET " + Prefix + "/catalog/works/{work_id}",
	}

	observed = append(observed, "GET /api/v2/admin/history-import-sources", "POST /api/v2/admin/history-import-sources", "DELETE /api/v2/admin/history-import-sources/{id}", "GET /api/v2/admin/history-import-sources/{id}", "PUT /api/v2/admin/history-import-sources/{id}", "GET /api/v2/admin/history-imports/capabilities", "GET /api/v2/admin/history-imports/mappings", "POST /api/v2/admin/history-imports/mappings", "DELETE /api/v2/admin/history-imports/mappings/{id}", "GET /api/v2/admin/history-imports/mappings/{id}", "PUT /api/v2/admin/history-imports/mappings/{id}", "POST /api/v2/admin/history-imports/mappings/{id}/run", "POST /api/v2/admin/history-imports/plex/login", "GET /api/v2/admin/history-imports/runs", "GET /api/v2/admin/history-imports/runs/{id}", "POST /api/v2/admin/history-imports/runs/{id}/cancel", "POST /api/v2/admin/history-imports/sources/{id}/bulk-run", "DELETE /api/v2/admin/history-imports/sources/{id}/token", "PUT /api/v2/admin/history-imports/sources/{id}/token", "GET /api/v2/admin/history-imports/sources/{id}/users")
	observed = append(observed, "DELETE /api/v2/admin/collection-groups/{id}", "DELETE /api/v2/admin/collections/{id}", "DELETE /api/v2/admin/collections/{id}/image", "DELETE /api/v2/admin/collections/{id}/items/{item_id}", "GET /api/v2/admin/collection-groups/{group_id}/collections/order", "GET /api/v2/admin/collection-groups/{id}", "GET /api/v2/admin/collection-jobs/{job_id}", "GET /api/v2/admin/collections", "GET /api/v2/admin/collections/capabilities", "GET /api/v2/admin/collections/order", "GET /api/v2/admin/collections/template-bundles", "GET /api/v2/admin/collections/templates", "GET /api/v2/admin/collections/{id}", "GET /api/v2/admin/collections/{id}/items", "GET /api/v2/admin/collections/{id}/items/order", "GET /api/v2/admin/libraries/{library_id}/collection-groups", "GET /api/v2/admin/libraries/{library_id}/collection-groups/order", "PATCH /api/v2/admin/collection-groups/{id}", "PATCH /api/v2/admin/collections/{id}", "POST /api/v2/admin/collections", "POST /api/v2/admin/collections/import/mdblist", "POST /api/v2/admin/collections/import/tmdb", "POST /api/v2/admin/collections/import/trakt", "POST /api/v2/admin/collections/preview", "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply", "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply-job", "POST /api/v2/admin/collections/{id}/sync", "POST /api/v2/admin/libraries/{library_id}/collection-groups", "PUT /api/v2/admin/collection-groups/{group_id}/collections/order", "PUT /api/v2/admin/collections/order", "PUT /api/v2/admin/collections/{id}/backdrop", "PUT /api/v2/admin/collections/{id}/items/order", "PUT /api/v2/admin/collections/{id}/items/{item_id}", "PUT /api/v2/admin/collections/{id}/poster", "PUT /api/v2/admin/libraries/{library_id}/collection-groups/order")

	observed = append(observed, "GET /api/v2/sync/progress/capabilities", "POST /api/v2/sync/progress/snapshots", "GET /api/v2/sync/progress/snapshots/{snapshot_id}")
	observed = append(observed,
		"GET /api/v2/admin/sections", "POST /api/v2/admin/sections",
		"GET /api/v2/admin/sections/{id}", "PATCH /api/v2/admin/sections/{id}", "DELETE /api/v2/admin/sections/{id}",
		"GET /api/v2/admin/sections/order", "PUT /api/v2/admin/sections/order", "PUT /api/v2/admin/sections/defaults",
		"POST /api/v2/admin/sections/bulk", "POST /api/v2/admin/sections/preview", "GET /api/v2/admin/sections/capabilities",
	)

	observed = append(observed, "GET /api/v2/admin/policy/decisions", "GET /api/v2/admin/policy/decisions/{id}", "GET /api/v2/admin/policy/documents", "POST /api/v2/admin/policy/documents", "DELETE /api/v2/admin/policy/documents/{id}", "GET /api/v2/admin/policy/documents/{id}", "PATCH /api/v2/admin/policy/documents/{id}", "GET /api/v2/admin/policy/documents/{id}/versions", "POST /api/v2/admin/policy/documents/{id}/versions", "GET /api/v2/admin/policy/documents/{id}/versions/{version}", "PUT /api/v2/admin/policy/documents/{id}/active-version", "POST /api/v2/admin/policy/simulate", "POST /api/v2/admin/policy/validate", "GET /api/v2/admin/policy/vendor")

	observed = append(observed, "POST /api/v2/account/password", "GET /api/v2/account/password/capability", "GET /api/v2/auth/device", "POST /api/v2/auth/device/approve", "POST /api/v2/auth/device/approve-handoff", "GET /api/v2/auth/device/capability", "POST /api/v2/auth/device/deny", "POST /api/v2/auth/device/poll", "POST /api/v2/auth/device/start", "POST /api/v2/auth/impersonation/end", "POST /api/v2/auth/login", "POST /api/v2/auth/logout", "POST /api/v2/auth/oauth/complete", "GET /api/v2/auth/providers", "POST /api/v2/auth/refresh", "GET /api/v2/auth/sessions", "DELETE /api/v2/auth/sessions/{id}", "POST /api/v2/auth/setup", "GET /api/v2/auth/signup", "POST /api/v2/auth/signup")
	observed = append(observed, "GET /api/v2/admin/api-keys/capabilities", "GET /api/v2/admin/api-keys", "POST /api/v2/admin/api-keys", "GET /api/v2/admin/api-keys/{id}", "PUT /api/v2/admin/api-keys/{id}/tier", "DELETE /api/v2/admin/api-keys/{id}")
	observed = append(observed, "GET /api/v2/invitations/capabilities", "GET /api/v2/invitations/{token}", "POST /api/v2/invitations/{token}/accept", "GET /api/v2/admin/invitations/capabilities", "GET /api/v2/admin/invitations", "GET /api/v2/admin/invitations/{id}", "POST /api/v2/admin/invitations", "POST /api/v2/admin/invitations/{id}/resend", "DELETE /api/v2/admin/invitations/{id}")
	observed = append(observed, "POST /api/v2/admin/catalog/export", "POST /api/v2/admin/catalog/export-jobs", "POST /api/v2/admin/catalog/import", "POST /api/v2/admin/catalog/import-jobs", "POST /api/v2/admin/catalog/export-jobs/{id}/publish", "GET /api/v2/admin/catalog/search/status", "GET /api/v2/admin/catalog/import-sources", "GET /api/v2/admin/catalog/local-import-sources", "GET /api/v2/admin/filesystem/browse", "GET /api/v2/admin/jobs", "GET /api/v2/admin/jobs/{id}", "GET /api/v2/admin/tasks", "GET /api/v2/admin/tasks/{key}", "GET /api/v2/admin/tasks/{key}/history", "GET /api/v2/admin/tasks/{key}/metrics", "GET /api/v2/admin/tasks/{key}/triggers", "POST /api/v2/admin/tasks/{key}/run", "POST /api/v2/admin/tasks/{key}/cancel", "PUT /api/v2/admin/tasks/{key}/triggers")
	observed = append(observed, "GET /api/v2/admin/access-groups", "POST /api/v2/admin/access-groups", "DELETE /api/v2/admin/access-groups/{id}", "GET /api/v2/admin/access-groups/{id}", "PUT /api/v2/admin/access-groups/{id}", "GET /api/v2/admin/ips", "POST /api/v2/admin/users", "GET /api/v2/admin/users/capabilities", "DELETE /api/v2/admin/users/{id}", "GET /api/v2/admin/users/{id}", "PUT /api/v2/admin/users/{id}", "GET /api/v2/admin/users/{id}/api-keys", "POST /api/v2/admin/users/{id}/impersonate", "GET /api/v2/admin/users/{id}/ips", "GET /api/v2/admin/users/{id}/profiles", "GET /api/v2/admin/users/{id}/settings/values", "DELETE /api/v2/admin/users/{id}/settings/values/{key}", "PUT /api/v2/admin/users/{id}/settings/values/{key}")
	observed = append(observed, "GET /api/v2/notifications", "GET /api/v2/notifications/capabilities", "GET /api/v2/notifications/preferences", "PUT /api/v2/notifications/preferences", "POST /api/v2/notifications/read-all", "GET /api/v2/notifications/sync", "GET /api/v2/notifications/unread-count", "GET /api/v2/notifications/{id}", "POST /api/v2/notifications/{id}/read")
	observed = append(observed,
		"DELETE /api/v2/admin/invite-codes/{id}",
		"DELETE /api/v2/admin/literary-works/{work_id}/items/{content_id}",
		"DELETE /api/v2/admin/notifications/push/relay",
		"DELETE /api/v2/api-keys/{id}",
		"GET /api/v2/admin/diagnostics/reports/{id}/download",
		"GET /api/v2/admin/invite-codes",
		"GET /api/v2/admin/invite-codes/capabilities",
		"GET /api/v2/admin/literary-works/items/{content_id}/candidates",
		"GET /api/v2/admin/plugins/catalog-settings",
		"GET /api/v2/admin/plugins/catalog-status",
		"GET /api/v2/admin/recommendations/status",
		"GET /api/v2/admin/settings",
		"GET /api/v2/admin/settings/effective",
		"GET /api/v2/admin/settings/restart-keys",
		"GET /api/v2/admin/settings/sensitive-status",
		"GET /api/v2/admin/system/build",
		"GET /api/v2/admin/stream-telemetry/parity",
		"GET /api/v2/admin/plugins/repositories",
		"POST /api/v2/admin/plugins/repositories",
		"PUT /api/v2/admin/plugins/repositories/{id}",
		"DELETE /api/v2/admin/plugins/repositories/{id}",
		"POST /api/v2/admin/autoscan/connections/test",
		"GET /api/v2/admin/autoscan/scans",
		"GET /api/v2/admin/autoscan/events",
		"DELETE /api/v2/admin/autoscan/sources/{id}",
		"POST /api/v2/admin/autoscan/sources",
		"PUT /api/v2/admin/autoscan/sources/{id}",
		"PUT /api/v2/admin/autoscan/settings",
		"POST /api/v2/admin/autoscan/connections",
		"PUT /api/v2/admin/autoscan/connections/{id}",
		"DELETE /api/v2/admin/autoscan/connections/{id}",
		"GET /api/v2/admin/system/hw-accel",
		"GET /api/v2/admin/system/resources",
		"GET /api/v2/api-keys",
		"GET /api/v2/api-keys/scopes",
		"GET /api/v2/branding/assets/{kind}",
		"GET /api/v2/compat/connect-info",
		"GET /api/v2/images/capabilities",
		"GET /api/v2/notifications/discord-preferences",
		"GET /api/v2/notifications/email-preferences",
		"GET /api/v2/notifications/push/apple/display/{delivery_id}",
		"GET /api/v2/policy/capability",
		"GET /api/v2/theme/admin-css",
		"GET /api/v2/theme/branding",
		"GET /api/v2/theme/capabilities",
		"HEAD /api/v2/branding/assets/{kind}",
		"POST /api/v2/admin/invite-codes",
		"POST /api/v2/admin/invite-codes/{id}/top-up",
		"POST /api/v2/admin/literary-works/link",
		"POST /api/v2/admin/literary-works/matches/confirm",
		"POST /api/v2/admin/literary-works/matches/ignore",
		"POST /api/v2/admin/notifications/push/apple/test",
		"POST /api/v2/admin/notifications/push/fcm/test",
		"POST /api/v2/admin/notifications/push/relay/register",
		"POST /api/v2/admin/recommendations/trigger/cowatch",
		"POST /api/v2/admin/recommendations/trigger/embeddings",
		"POST /api/v2/admin/recommendations/trigger/recommendations",
		"POST /api/v2/admin/recommendations/trigger/taste-profiles",
		"POST /api/v2/admin/settings/check/{kind}",
		"POST /api/v2/api-keys",
		"PUT /api/v2/admin/invite-codes/{id}",
		"PUT /api/v2/admin/plugins/catalog-settings",
		"PUT /api/v2/notifications/discord-preferences",
		"PUT /api/v2/notifications/email-preferences",
	)
	observed = append(observed,
		"DELETE /api/v2/admin/diagnostics/reports/{id}",
		"GET /api/v2/admin/diagnostics/reports",
		"GET /api/v2/admin/diagnostics/reports/{id}",
	)
	observed = append(observed,
		"GET /api/v2/admin/items/{id}/metadata-translation/jobs",
		"PATCH /api/v2/admin/items/{id}/metadata",
		"PATCH /api/v2/admin/people/{id}",
		"POST /api/v2/admin/items/{id}/metadata-translation",
		"POST /api/v2/admin/items/{id}/metadata-translation/jobs/{job_id}/cancel",
		"POST /api/v2/admin/items/{id}/refresh-metadata",
		"POST /api/v2/admin/people/{id}/refresh",
	)
	observed = append(observed,
		"GET /api/v2/theme/catalog",
		"GET /api/v2/theme/catalog/capabilities",
		"GET /api/v2/theme/download",
		"POST /api/v2/theme/catalog/refresh",
	)
	observed = append(observed,
		"GET /api/v2/capabilities/ebooks",
		"GET /api/v2/ebooks/{content_id}/files/{file_id}/read",
		"GET /api/v2/ebooks/{content_id}/progress",
		"GET /api/v2/ebooks/{content_id}/reader-config",
		"HEAD /api/v2/ebooks/{content_id}/files/{file_id}/read",
		"PUT /api/v2/ebooks/{content_id}/progress",
		"PUT /api/v2/ebooks/{content_id}/reader-config",
	)
	observed = append(observed,
		"GET /api/v2/admin/notifications/server-channels",
		"DELETE /api/v2/admin/notifications/server-channels/{id}",
		"GET /api/v2/notifications/push/devices/capabilities",
		"POST /api/v2/notifications/push/devices",
		"DELETE /api/v2/notifications/push/devices/{device_id}",
		"GET /api/v2/notifications/web-push/subscriptions",
		"POST /api/v2/notifications/web-push/subscriptions",
		"POST /api/v2/notifications/web-push/unsubscribe",
		"DELETE /api/v2/notifications/web-push/subscriptions/{id}",
		"GET /api/v2/notifications/webhooks",
		"DELETE /api/v2/notifications/webhooks/{id}",
		"PUT /api/v2/notifications/webhooks/{id}",
		"DELETE /api/v2/notifications/discord-link",
		"DELETE /api/v2/notifications/email-preferences/address",
		"POST /api/v2/admin/notifications/discord/test",
	)
	observed = append(observed,
		"DELETE /api/v2/ebooks/{content_id}/annotations/{annotation_id}",
		"GET /api/v2/ebooks/{content_id}/annotations",
		"PATCH /api/v2/ebooks/{content_id}/annotations/{annotation_id}",
		"POST /api/v2/ebooks/{content_id}/annotations",
	)
	observed = append(observed,
		"GET /api/v2/admin/devices",
		"GET /api/v2/admin/devices/capabilities",
		"GET /api/v2/admin/devices/{user_id}/{device_id}",
		"GET /api/v2/user/libraries",
		"GET /api/v2/user/libraries/capabilities",
	)
	observed = append(observed,
		"GET /api/v2/admin/nodes",
		"GET /api/v2/admin/stats/downloads",
		"GET /api/v2/admin/stats/playback-activity",
		"GET /api/v2/admin/stats/timeseries",
		"GET /api/v2/admin/stats/top-activity",
	)
	observed = append(observed,
		"GET /api/v2/admin/server/status",
		"POST /api/v2/admin/email/test",
	)
	observed = append(observed,
		"POST /api/v2/notifications/webhooks/{id}/test",
		"POST /api/v2/notifications/webhooks/{id}/rotate-secret",
		"POST /api/v2/admin/notifications/server-channels/{id}/test",
		"GET /api/v2/notifications/email/verify",
		"GET /api/v2/notifications/email/unsubscribe",
		"POST /api/v2/notifications/email/unsubscribe",
	)
	observed = append(observed,
		"GET /api/v2/subtitles/ai/jobs",
		"GET /api/v2/subtitles/ai/jobs/{job_id}",
		"GET /api/v2/subtitles/ai/quota",
	)
	observed = append(observed,
		"GET /api/v2/auth/oauth/capabilities",
		"POST /api/v2/auth/oauth/{install_id}/init",
		"GET /api/v2/auth/oauth/{install_id}/callback",
		"GET /api/v2/webhook-sync/capabilities",
		"POST /api/v2/webhook-sync/webhooks/{secret}",
	)
	observed = append(observed,
		"GET /api/v2/downloads",
		"PATCH /api/v2/downloads/{id}",
		"DELETE /api/v2/downloads/{id}",
		"GET /api/v2/capabilities/downloads",
		"GET /api/v2/downloads/{id}/file",
		"HEAD /api/v2/downloads/{id}/file",
		"GET /api/v2/downloads/{id}/file-proxy",
		"HEAD /api/v2/downloads/{id}/file-proxy",
		"GET /api/v2/downloads/{id}/artwork/{kind}",
		"GET /api/v2/downloads/{id}/subtitles/{ref}",
		"GET /api/v2/downloads/{id}/manifest",
		"GET /api/v2/downloads/batches/{batch_id}/manifests",
		"GET /api/v2/downloads/subscriptions",
		"GET /api/v2/downloads/subscriptions/{id}",
	)
	observed = append(observed,
		"GET /api/v2/admin/files/{fileId}/contributions",
		"GET /api/v2/admin/markers/files/{fileId}/history",
		"GET /api/v2/admin/markers/history",
		"GET /api/v2/admin/markers/items/{id}/history",
		"GET /api/v2/admin/markers/providers",
		"POST /api/v2/admin/files/{fileId}/contribute",
		"POST /api/v2/admin/items/{id}/redetect-intro",
		"POST /api/v2/admin/items/{id}/refresh-markers",
		"POST /api/v2/admin/markers/providers/{provider}/validate",
		"PUT /api/v2/admin/markers/providers/{provider}",
	)
	observed = append(observed, "GET /api/v2/playback/capabilities", "POST /api/v2/playback/start", "POST /api/v2/playback/{session_id}/progress", "DELETE /api/v2/playback/{session_id}", "POST /api/v2/playback/route-events", "POST /api/v2/playback/{session_id}/replan")
	unaccounted, unserved, err := reconcileSpec(observed, contracts.OpenAPI, nil)
	if err != nil || len(unaccounted) != 0 || len(unserved) != 0 {
		t.Fatalf("baseline: %v %v %v", unaccounted, unserved, err)
	}
	// A raw route the router serves but nothing describes.
	unaccounted, _, _ = reconcileSpec(append(observed, "GET "+ws.Path), contracts.OpenAPI, nil)
	if len(unaccounted) != 1 || unaccounted[0] != "GET "+ws.Path {
		t.Fatalf("raw route not reported: %v", unaccounted)
	}
	// The same route with its manual-registry entry.
	unaccounted, unserved, _ = reconcileSpec(append(observed, "GET "+ws.Path), contracts.OpenAPI, []RawHandshake{ws})
	if len(unaccounted) != 0 || len(unserved) != 0 {
		t.Fatalf("manual entry did not account for the raw route: %v %v", unaccounted, unserved)
	}
	// A manual entry for a route nobody serves is reported.
	_, unserved, _ = reconcileSpec(observed, contracts.OpenAPI, []RawHandshake{ws})
	if len(unserved) != 1 || !strings.HasPrefix(unserved[0], "GET "+ws.Path) {
		t.Fatalf("unserved manual entry not reported: %v", unserved)
	}
	// A documented operation the router does not serve (stale artifact).
	var withoutInfo []string
	for _, route := range observed {
		if route != "GET "+Prefix+"/system/info" {
			withoutInfo = append(withoutInfo, route)
		}
	}
	_, unserved, _ = reconcileSpec(withoutInfo, contracts.OpenAPI, nil)
	if len(unserved) != 1 || !strings.HasPrefix(unserved[0], "GET "+Prefix+"/system/info") {
		t.Fatalf("stale artifact not reported: %v", unserved)
	}
	// A route cannot be both.
	if _, _, err := reconcileSpec(observed, contracts.OpenAPI, []RawHandshake{{Method: http.MethodGet, Path: Prefix + "/system/info"}}); err == nil {
		t.Fatal("an operation doubling as a manual entry was accepted")
	}
}

// TestDeterministicRegistration: the route table does not depend on the
// wiring; missing gates fail closed at request time.
func TestDeterministicRegistration(t *testing.T) {
	bare, _ := routeinventory.Observed(newChiRouter(Dependencies{testRegister: registerProbes}))
	wired, _ := routeinventory.Observed(newChiRouter(Dependencies{testRegister: registerProbes, Auth: fakeAuth(nil)}))
	if strings.Join(bare, "\n") != strings.Join(wired, "\n") {
		t.Fatalf("route table depends on wiring:\n%v\n%v", bare, wired)
	}
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/authenticated", `{"name":"x","cleared":null}`, nil)
	requireProblem(t, rec, TypeDependencyUnavailable)
}

func TestRegisterRefusesBadDeclarations(t *testing.T) {
	cases := map[string]Operation{
		"upper id": {Operation: humaOp(http.MethodGet, Prefix+"/x", "GetX", "x", ""), Class: ClassPublic},
		"two tags": {Operation: func() huma_Operation {
			o := humaOp(http.MethodGet, Prefix+"/x", "getX", "x", "")
			o.Tags = append(o.Tags, "y")
			return o
		}(), Class: ClassPublic},
		"trailing": {Operation: humaOp(http.MethodGet, Prefix+"/x/", "getX", "x", ""), Class: ClassPublic},
		"no class": {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", "")},
		// A declaration that would be inert or fail per request is refused at
		// registration, where a panic is a build failure.
		"demo on public":      {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic, DemoRestricted: true},
		"unknown permission":  {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPermissionGated, Permission: "not_a_permission"},
		"curation without id": {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration},
		"huma body limit": {Operation: func() huma_Operation {
			o := humaOp(http.MethodPost, Prefix+"/x", "postX", "x", "")
			o.MaxBodyBytes = 10
			return o
		}(), Class: ClassPublic, RetrySafety: RetrySafetyNaturalIdempotent},
		"negative body limit": {Operation: humaOp(http.MethodPost, Prefix+"/x", "postX", "x", ""), Class: ClassPublic, MaxBodyBytes: -1, RetrySafety: RetrySafetyNaturalIdempotent},
		// Retry safety is a required declaration on every mutation and
		// meaningless on a read.
		"mutation without retry safety": {Operation: humaOp(http.MethodPost, Prefix+"/x", "postX", "x", ""), Class: ClassPublic},
		"unknown retry safety":          {Operation: humaOp(http.MethodDelete, Prefix+"/x", "deleteX", "x", ""), Class: ClassPublic, RetrySafety: RetrySafety("retry_later")},
		"read with retry safety":        {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic, RetrySafety: RetrySafetyNaturalIdempotent},
		"perm class":                    {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPermissionGated},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			newChiRouter(Dependencies{testRegister: func(reg *Registry) {
				Register(reg, op, func(context.Context, *struct{}) (*probeOutput, error) { return nil, nil })
			}})
		})
	}
	t.Run("item-scoped permission with an id parameter", func(t *testing.T) {
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{
				Operation:   humaOp(http.MethodPatch, Prefix+"/items/{id}", "patchItem", "x", ""),
				Class:       ClassPermissionGated,
				Permission:  policy.PermissionMetadataCuration,
				RetrySafety: RetrySafetyNaturalIdempotent,
			}, func(context.Context, *struct {
				ID string `path:"id"`
			}) (*probeOutput, error) {
				return nil, nil
			})
		}})
	})
	t.Run("slice without explode", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "explode") {
				t.Fatalf("recover = %v", r)
			}
		}()
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic},
				func(context.Context, *struct {
					IDs []string `query:"ids"`
				}) (*probeOutput, error) {
					return nil, nil
				})
		}})
	})
	t.Run("uppercase enum", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "lowercase") {
				t.Fatalf("recover = %v", r)
			}
		}()
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic},
				func(context.Context, *struct{}) (*struct {
					Body struct {
						Kind string `json:"kind" enum:"Movie,series"`
					}
				}, error) {
					return nil, nil
				})
		}})
	})
}

// --- Framework configuration -----------------------------------------------

func TestUnknownQueryParameterIs422(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info?bogus=1", "", nil)
	p := requireProblem(t, rec, TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.bogus" || p.Errors[0].Code != "unknown_parameter" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestUnacceptableAcceptIs406(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": "text/xml"})
	requireProblem(t, rec, TypeNotAcceptable)
	for _, accept := range []string{"", "*/*", "application/*", "application/json", "text/html;q=0.9, */*;q=0.1", "application/json; charset=utf-8"} {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": accept})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("Accept %q: %d %s", accept, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// A bare "json" is not a media type and must not select the alias.
	rec = do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": "json"})
	requireProblem(t, rec, TypeNotAcceptable)
	// The most specific matching range decides: an explicit q=0 on JSON is
	// not overridden by a later wildcard, and a q=0 wildcard does not veto an
	// explicit JSON range.
	for _, accept := range []string{"application/json;q=0, */*;q=1", "application/*;q=0, */*", "application/json;q=0"} {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": accept})
		if rec.Code != http.StatusNotAcceptable {
			t.Errorf("Accept %q: %d, want 406", accept, rec.Code)
		}
	}
	for _, accept := range []string{"*/*;q=0, application/json", "*/*;q=0, application/*;q=0.5", "text/html, application/json;q=0.1"} {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": accept})
		if rec.Code != http.StatusOK {
			t.Errorf("Accept %q: %d, want 200", accept, rec.Code)
		}
	}
}

func TestMediaTypeGuard(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	body := `{"name":"x","cleared":null}`
	rejected := []string{"", "application/vnd.silo+json", "application/json; charset=iso-8859-1", "application/json; boundary=x", "text/plain", "application/json; charset=utf-8; foo=bar"}
	for _, ct := range rejected {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/probe/public", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: status %d, want 415: %s", ct, rec.Code, rec.Body.String())
			continue
		}
		requireProblem(t, rec, TypeUnsupportedMediaType)
	}
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "application/json;charset=UTF-8"} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Type": ct})
		if rec.Code != 200 {
			t.Errorf("Content-Type %q: status %d: %s", ct, rec.Code, rec.Body.String())
		}
	}
}

func TestCollectionsNeverNull(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/list", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("collection body: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, nil)
	if !strings.Contains(rec.Body.String(), `"tags":[]`) || !strings.Contains(rec.Body.String(), `"labels":{}`) {
		t.Fatalf("echo body: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "$schema") {
		t.Fatalf("$schema in success body: %s", rec.Body.String())
	}
}

func TestInstantWire(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	// UTC conversion and exactly three fractional digits.
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","when":"2024-03-01T12:34:56.7+02:00","cleared":null}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"when":"2024-03-01T10:34:56.700Z"`) {
		t.Fatalf("when: %d %s", rec.Code, rec.Body.String())
	}
	// Omission when unset; explicit null where the schema allows it.
	if !strings.Contains(rec.Body.String(), `"cleared":null`) {
		t.Fatalf("explicit null lost: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, nil)
	if strings.Contains(rec.Body.String(), `"when"`) {
		t.Fatalf("unset instant not omitted: %s", rec.Body.String())
	}
	// Zero-value rejection on input.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","when":"0001-01-01T00:00:00Z","cleared":null}`, nil)
	p := requireProblem(t, rec, TypeValidationFailed)
	if len(p.Errors) == 0 || p.Errors[0].Location != "body.when" {
		t.Fatalf("zero instant: %+v", p.Errors)
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","when":"yesterday","cleared":null}`, nil)
	requireProblem(t, rec, TypeValidationFailed)
	// Zero-value refusal on output is an internal error, never a bogus wire value.
	rec = do(t, h, http.MethodGet, "/api/v2/probe/zero", "", nil)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "0001-01-01") {
		t.Fatalf("zero output: %d %s", rec.Code, rec.Body.String())
	}
	if got := NewInstant(time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.FixedZone("x", 3600))).String(); got != "2024-01-02T02:04:05.123Z" {
		t.Fatalf("String = %s", got)
	}
}

func TestSliceQueryUsesRepeatedKeys(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public?tags=a&tags=b", `{"name":"x","cleared":null}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tags":["a","b"]`) {
		t.Fatalf("repeated keys: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public?tags=a,b", `{"name":"x","cleared":null}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tags":["a,b"]`) {
		t.Fatalf("comma form must not split: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNoBuiltInDocsRoutes(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for _, path := range []string{"/api/v2/docs", "/api/v2/openapi.yaml", "/api/v2/schemas/Problem.json", "/docs", "/openapi.json"} {
		rec := do(t, h, http.MethodGet, path, "", nil)
		if rec.Code != 404 {
			t.Errorf("%s served %d", path, rec.Code)
		}
	}
	// /api/v2/openapi.json is Silo's own operation (getOpenAPIDocument), not
	// Huma's built-in route: TestOpenAPIDocumentIsTheEmbeddedArtifact.
}

// TestOpenAPIDocumentIsTheEmbeddedArtifact: the served bytes are the committed
// artifact exactly, and the digest the discovery document reports is the
// digest of those bytes.
func TestOpenAPIDocumentIsTheEmbeddedArtifact(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/openapi.json", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if !bytes.Equal(rec.Body.Bytes(), contracts.OpenAPI) {
		t.Fatal("served bytes differ from the embedded artifact")
	}
	sum := sha256.Sum256(rec.Body.Bytes())
	digest := hex.EncodeToString(sum[:])
	if digest != ContractDigest() {
		t.Fatalf("served digest %s != embedded %s", digest, ContractDigest())
	}
	if rec.Header().Get("ETag") != `"`+digest+`"` {
		t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
	}
	var info SystemInfo
	if err := json.Unmarshal(do(t, h, http.MethodGet, "/api/v2/system/info", "", nil).Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ContractDigest != digest {
		t.Fatalf("system/info digest %s != served %s", info.ContractDigest, digest)
	}
	// The artifact is a parseable OpenAPI 3.1 document with the operation
	// that serves it.
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Paths["/api/v2/openapi.json"]["get"] == nil {
		t.Fatalf("unexpected document: %s", rec.Body.String())
	}
}

// TestGenerateOpenAPIIsDeterministic: two generations in one process and the
// document's own hygiene rules (no servers, nothing build-specific; schema
// examples are fictional fixture-shaped values and are allowed).
func TestGenerateOpenAPIIsDeterministic(t *testing.T) {
	a, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("generation is not deterministic")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"servers", "webhooks", "externalDocs"} {
		if _, ok := doc[forbidden]; ok {
			t.Errorf("document carries %q", forbidden)
		}
	}
	// The home routes (/api/v2/home/...) are a real path segment, not a
	// Unix home directory; strip that prefix before the leak check.
	scrubbed := bytes.ReplaceAll(a, []byte(Prefix+"/home/"), nil)
	for _, needle := range []string{"/Users/", "/home/", "localhost"} {
		if bytes.Contains(scrubbed, []byte(needle)) {
			t.Errorf("document contains %s", needle)
		}
	}
}

func TestBodyCapIsEnforcedBeforeDecoding(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	pad := func(n int64) string {
		prefix := `{"name":"x","cleared":null,"note":"`
		suffix := `"}`
		return prefix + strings.Repeat("a", int(n)-len(prefix)-len(suffix)) + suffix
	}
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", pad(MaxJSONBodyBytes), nil)
	if rec.Code != 200 {
		t.Fatalf("exactly at the cap: %d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", pad(MaxJSONBodyBytes+1), nil)
	requireProblem(t, rec, TypePayloadTooLarge)
	// Not even valid JSON is required: the cap fires before decoding.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", strings.Repeat("x", int(MaxJSONBodyBytes)+1), nil)
	requireProblem(t, rec, TypePayloadTooLarge)
}

// TestBodyReadTimeout exercises the 408 boundary over a real connection with
// an injected deadline, so the test does not wait for BodyReadTimeout.
func TestBodyReadTimeout(t *testing.T) {
	if BodyReadTimeout != 30*time.Second {
		t.Fatalf("BodyReadTimeout = %v; #135 ratified the 30 s server baseline on 2026-09-02", BodyReadTimeout)
	}
	h := newTestHandler(t, Dependencies{bodyReadTimeout: 150 * time.Millisecond})
	srv := httptest.NewServer(h)
	defer srv.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "POST /api/v2/probe/public HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"name\":")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufioReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d body %s", resp.StatusCode, body)
	}
	var p problemDoc
	_ = json.Unmarshal(body, &p)
	if p.Type != TypeRequestTimeout.URI() || resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("408 envelope: %s", body)
	}
}

// --- Problem details --------------------------------------------------------

func TestFrameworkAndApplicationValidationShapesMatch(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	framework := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/probe/public", `{"cleared":null}`, nil), TypeValidationFailed)
	app := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/probe/apperror", "", nil), TypeValidationFailed)
	if len(framework.Errors) != 1 || len(app.Errors) != 1 {
		t.Fatalf("errors: %+v / %+v", framework.Errors, app.Errors)
	}
	if framework.Errors[0] != app.Errors[0] {
		t.Fatalf("shapes differ:\n%+v\n%+v", framework.Errors[0], app.Errors[0])
	}
	if framework.Detail != app.Detail || framework.Title != app.Title {
		t.Fatalf("envelopes differ: %+v / %+v", framework, app)
	}
}

func TestFrameworkFailuresMapToCatalog(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	cases := []struct {
		name         string
		method, path string
		body         string
		headers      map[string]string
		want         ProblemType
		location     string
		code         string
	}{
		{"malformed json: truncated", http.MethodPost, "/api/v2/probe/public", `{"name":`, nil, TypeMalformedRequest, "body", "malformed_json"},
		{"malformed json: invalid character", http.MethodPost, "/api/v2/probe/public", `{"name":"x",}`, nil, TypeMalformedRequest, "body", "malformed_json"},
		{"unknown field", http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null,"zzz":1}`, nil, TypeValidationFailed, "body.zzz", "unknown_field"},
		{"missing required", http.MethodPost, "/api/v2/probe/public", `{"cleared":null}`, nil, TypeValidationFailed, "body.name", "required"},
		{"closed enum", http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null,"kind":"Movie"}`, nil, TypeValidationFailed, "body.kind", "invalid_enum"},
		{"range", http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null,"count":11}`, nil, TypeValidationFailed, "body.count", "out_of_range"},
		{"unknown query", http.MethodGet, "/api/v2/system/info?x=1", "", nil, TypeValidationFailed, "query.x", "unknown_parameter"},
		{"limit above max", http.MethodGet, "/api/v2/probe/list?limit=201", "", nil, TypeValidationFailed, "query.limit", "out_of_range"},
		{"bad sort", http.MethodGet, "/api/v2/probe/list?sort=name,-nope", "", nil, TypeValidationFailed, "query.sort", "invalid_sort_field"},
		{"bool literal", http.MethodPost, "/api/v2/probe/public?flag=TRUE", `{"name":"x","cleared":null}`, nil, TypeValidationFailed, "query.flag", "invalid_type"},
		{"bool 1", http.MethodPost, "/api/v2/probe/public?flag=1", `{"name":"x","cleared":null}`, nil, TypeValidationFailed, "query.flag", "invalid_type"},
		{"406", http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": "image/png"}, TypeNotAcceptable, "", ""},
		{"405", http.MethodDelete, "/api/v2/system/info", "", nil, TypeMethodNotAllowed, "", ""},
		{"404", http.MethodGet, "/api/v2/nothing", "", nil, TypeNotFound, "", ""},
		{"415", http.MethodPost, "/api/v2/probe/public", `{}`, map[string]string{"Content-Type": "text/json"}, TypeUnsupportedMediaType, "", ""},
		{"invalid path value", http.MethodGet, "/api/v2/probe/item/abc", "", nil, TypeValidationFailed, "path.id", "invalid_type"},
		{"invalid header value", http.MethodGet, "/api/v2/probe/item/7", "", map[string]string{"X-Probe-Count": "abc"}, TypeValidationFailed, "header.x-probe-count", "invalid_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := requireProblem(t, do(t, h, tc.method, tc.path, tc.body, tc.headers), tc.want)
			if tc.location == "" {
				if len(p.Errors) != 0 {
					t.Fatalf("unexpected errors: %+v", p.Errors)
				}
				return
			}
			found := false
			for _, e := range p.Errors {
				if e.Location == tc.location && e.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s/%s in %+v", tc.location, tc.code, p.Errors)
			}
			for _, rejected := range []string{"Movie", "11", "abc"} {
				if strings.Contains(p.Detail+p.Errors[0].Detail, rejected) {
					t.Fatalf("rejected value %q echoed: %+v", rejected, p)
				}
			}
		})
	}
}

func TestQueryGrammar(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/probe/public?flag=true&flag=false", `{"name":"x","cleared":null}`, nil), TypeMalformedRequest)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/system/info?%zz=1", "", nil), TypeMalformedRequest)
	rec := do(t, h, http.MethodGet, "/api/v2/system/info/", "", nil)
	if rec.Code != 404 || rec.Header().Get("Location") != "" {
		t.Fatalf("trailing slash: %d %v", rec.Code, rec.Header())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/list?sort=name,-added_at&limit=5", "", nil)
	if rec.Code != 200 {
		t.Fatalf("valid sort: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRetryAfterOn429And503(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/ratelimited", "", nil)
	requireProblem(t, rec, TypeRateLimited)
	if rec.Header().Get("Retry-After") != "7" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/unavailable", "", nil)
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
}

func TestPanicIsInternalErrorWithoutLeak(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/panic", "", nil)
	p := requireProblem(t, rec, TypeInternalError)
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "boom") || len(p.Errors) != 0 {
		t.Fatalf("panic detail leaked: %s", rec.Body.String())
	}
}

func TestCanceledRequestWritesNothing(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/api/v2/probe/slow", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		h.ServeHTTP(rec, r)
	}()
	cancel()
	<-done
	if rec.Body.Len() != 0 {
		t.Fatalf("body written after cancel: %s", rec.Body.String())
	}
}

func TestSuccessDefaultsNoStoreAndNoSchema(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if strings.Contains(rec.Body.String(), "$schema") {
		t.Fatal("$schema present")
	}
}

func TestPatchTransport(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for body, want := range map[string]string{
		`{"name":"x","cleared":null}`:             `"note_set":false,"note_null":false,"note":""`,
		`{"name":"x","cleared":null,"note":null}`: `"note_set":true,"note_null":true,"note":""`,
		`{"name":"x","cleared":null,"note":"hi"}`: `"note_set":true,"note_null":false,"note":"hi"`,
	} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s -> %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// --- Discovery --------------------------------------------------------------

func TestSystemInfo(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	var info SystemInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.APIMajor != 2 || info.ServerVersion == "" || len(info.ContractDigest) != 64 ||
		info.Links.OpenAPI != "/api/v2/openapi.json" || info.Links.Capabilities != "/api/v2/capabilities" {
		t.Fatalf("%+v", info)
	}
	if info.ContractDigest != ContractDigest() {
		t.Fatal("digest not from the embedded bytes")
	}
	if rec.Body.Len() > 1024 {
		t.Fatalf("discovery document is not bounded: %d bytes", rec.Body.Len())
	}
	// Reproducible: the same bytes, the same digest.
	if do(t, h, http.MethodGet, "/api/v2/system/info", "", nil).Body.String() != rec.Body.String() {
		t.Fatal("discovery document varies between requests")
	}
}

// --- Cursor and helpers -----------------------------------------------------

func TestCursors(t *testing.T) {
	c := NewCursors([]byte("k"))
	scope := CursorScope{OperationID: "listX", Security: "u1", Filter: "a=1", Sort: "name", Tiebreaker: "id"}
	type pos struct{ Name, ID string }
	cur, err := c.Encode(scope, pos{"n", "7"})
	if err != nil || strings.ContainsAny(cur, "+/=") {
		t.Fatalf("encode: %v %q", err, cur)
	}
	var got pos
	if p := c.Decode(scope, cur, &got); p != nil || got != (pos{"n", "7"}) {
		t.Fatalf("decode: %v %+v", p, got)
	}
	other := scope
	other.Security = "u2"
	for name, bad := range map[string]struct {
		s CursorScope
		c string
	}{
		"other scope": {other, cur},
		"tampered":    {scope, "x" + cur[1:]},
		"garbage":     {scope, "not-a-cursor"},
		"other key":   {scope, func() string { k, _ := NewCursors([]byte("k2")).Encode(scope, pos{}); return k }()},
	} {
		if p := c.Decode(bad.s, bad.c, &got); p == nil || p.Type != TypeInvalidCursor.URI() || p.Status != 400 {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

func TestRequestIDNeverAdoptsClientValue(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	check := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		id := requestIDHeader(rec)
		if id == "" || strings.Contains(id, "evil") || strings.Contains(rec.Body.String(), "evil") {
			t.Fatalf("client value adopted: header %q body %s", id, rec.Body.String())
		}
		var p problemDoc
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.Instance != "urn:silo:request:"+id {
			t.Fatalf("instance %q vs header %q", p.Instance, id)
		}
		return id
	}
	// Standalone: both header spellings a client might try.
	for _, name := range []string{"X-Request-ID", "X-Request-Id"} {
		rec := do(t, h, http.MethodGet, "/api/v2/nothing", "", map[string]string{name: "evil"})
		check(t, rec)
	}
	// Under the API listener's base chain, the server-generated ID from the
	// context is reused verbatim and the client value is still ignored.
	outer := chi.NewRouter()
	outer.Use(apimw.RequestID)
	var ctxID string
	outer.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctxID = chiRequestIDFrom(r)
			next.ServeHTTP(w, r)
		})
	})
	outer.Handle(DelegationPattern, h)
	r := httptest.NewRequest(http.MethodGet, "/api/v2/nothing", nil)
	r.Header.Set("X-Request-Id", "evil")
	rec := httptest.NewRecorder()
	outer.ServeHTTP(rec, r)
	if id := check(t, rec); id != ctxID {
		t.Fatalf("v2 header %q differs from the context id %q the v1 logs use", id, ctxID)
	}
}

// TestCompressedRequestBodyIsRejected: the media-type guard refuses a
// non-identity Content-Encoding before the body is read, so a gzip header over
// a plain body is a 415, not a 200 or a parse error.
func TestCompressedRequestBodyIsRejected(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	body := `{"name":"x","cleared":null}`
	for _, enc := range []string{"gzip", "br", "deflate", "GZIP", "gzip, identity"} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Encoding": enc})
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Encoding %q: status %d, want 415: %s", enc, rec.Code, rec.Body.String())
			continue
		}
		requireProblem(t, rec, TypeUnsupportedMediaType)
	}
	// A real gzip body is refused for the same reason, before decoding.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(body))
	_ = zw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/v2/probe/public", bytes.NewReader(gz.Bytes()))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeUnsupportedMediaType)
	// identity is the one encoding an unencoded body may declare.
	if rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Encoding": "identity"}); rec.Code != 200 {
		t.Fatalf("identity: %d %s", rec.Code, rec.Body.String())
	}
}

// TestMediaTypeIsCanonicalizedBeforeHuma: the guard, not Huma's
// case-sensitive format table, is the single 415 authority, so a media type
// that differs only in case is accepted.
func TestMediaTypeCaseIsAcceptedByTheGuard(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	body := `{"name":"x","cleared":null}`
	for _, ct := range []string{"APPLICATION/JSON", "Application/Json", "APPLICATION/JSON; CHARSET=UTF-8"} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Type": ct})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type %q: %d %q %s", ct, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

// TestMethodNotAllowedSendsAllow: RFC 9110 requires Allow on a 405, and the
// set comes from the registry's declared rows for the matched path.
func TestMethodNotAllowedSendsAllow(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for _, method := range []string{http.MethodDelete, http.MethodHead} {
		rec := do(t, h, method, "/api/v2/system/info", "", nil)
		requireProblem(t, rec, TypeMethodNotAllowed)
		if got := rec.Header().Get("Allow"); got != "GET" {
			t.Errorf("%s /api/v2/system/info: Allow = %q, want %q", method, got, "GET")
		}
	}
	// A path parameter is matched, not compared literally.
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/item/7", "", nil)
	requireProblem(t, rec, TypeMethodNotAllowed)
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow = %q, want GET", got)
	}
}

// TestNamespaceRootIsNotAV2Path: /api/v2 (no slash) is outside the v2 surface
// and answered by the legacy listener; /api/v2/ is a v2 not_found problem.
func TestNamespaceRootIsNotAV2Path(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/", "", nil)
	requireProblem(t, rec, TypeNotFound)

	// Through the delegation the API listener registers, the same holds and
	// /api/v2 never reaches this package.
	outer := chi.NewRouter()
	outer.Use(apimw.RequestID)
	outer.Handle(DelegationPattern, h)
	outer.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	r := httptest.NewRequest(http.MethodGet, "/api/v2", nil)
	rec = httptest.NewRecorder()
	outer.ServeHTTP(rec, r)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("/api/v2 reached the v2 listener: %d %s", rec.Code, rec.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v2/", nil)
	rec = httptest.NewRecorder()
	outer.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeNotFound)
}

// TestOperationBodyLimitOverride: an operation's own cap gets the same
// off-by-one translation as the default, and the 413 names that cap.
func TestOperationBodyLimitOverride(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	pad := func(n int64) string {
		prefix := `{"name":"x","cleared":null,"note":"`
		suffix := `"}`
		return prefix + strings.Repeat("a", int(n)-len(prefix)-len(suffix)) + suffix
	}
	if rec := do(t, h, http.MethodPost, "/api/v2/probe/smallbody", pad(ProbeSmallBodyLimit), nil); rec.Code != 200 {
		t.Fatalf("exactly at the override: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodPost, "/api/v2/probe/smallbody", pad(ProbeSmallBodyLimit+1), nil)
	p := requireProblem(t, rec, TypePayloadTooLarge)
	if !strings.Contains(p.Detail, strconv.FormatInt(ProbeSmallBodyLimit, 10)) {
		t.Fatalf("413 detail names the wrong limit: %q", p.Detail)
	}
	// The default limit still renders its own value.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", pad(MaxJSONBodyBytes+1), nil)
	p = requireProblem(t, rec, TypePayloadTooLarge)
	if !strings.Contains(p.Detail, strconv.FormatInt(MaxJSONBodyBytes, 10)) {
		t.Fatalf("default 413 detail: %q", p.Detail)
	}
}

// TestTamperedCursorThroughRouter: a cursor the operation decodes fails with
// the invalid_cursor problem end to end, envelope and cache policy included.
func TestTamperedCursorThroughRouter(t *testing.T) {
	h := newTestHandler(t, Dependencies{CursorSecret: []byte("k")})
	good, err := NewCursors([]byte("k")).Encode(probeCursorScope, probeCursor{Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodGet, "/api/v2/probe/list?cursor="+good, "", nil); rec.Code != 200 {
		t.Fatalf("valid cursor: %d %s", rec.Code, rec.Body.String())
	}
	for name, cursor := range map[string]string{
		"tampered": "x" + good[1:],
		"garbage":  "not-a-cursor",
		"foreign":  func() string { c, _ := NewCursors([]byte("other")).Encode(probeCursorScope, probeCursor{}); return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, "/api/v2/probe/list?cursor="+cursor, "", nil)
			requireProblem(t, rec, TypeInvalidCursor)
		})
	}
}

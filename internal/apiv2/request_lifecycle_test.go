package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type fakeLifecycle struct {
	viewer     mediarequests.Viewer
	id, reason string
	err        error
}

func (f *fakeLifecycle) Cancel(_ context.Context, v mediarequests.Viewer, id, reason string) (*mediarequests.Request, error) {
	f.viewer = v
	f.id = id
	f.reason = reason
	return fixtureMediaRequest(id, 949), f.err
}
func (f *fakeLifecycle) GetFeatureStatus(_ context.Context, v mediarequests.Viewer) (mediarequests.FeatureStatus, error) {
	f.viewer = v
	return mediarequests.FeatureStatus{RequestsEnabled: true, RatingRestrictionsEnforced: true}, f.err
}

type fakeWatchLifecycle struct {
	version               watchsync.ConnectionVersion
	updateCalls           int
	staleUpdate           bool
	missingConnection     bool
	credentialsConfigured bool
	displayName           string
	WatchProviderService
	user         int
	profile, key string
	limit        int
	err          error
}

func (f *fakeWatchLifecycle) ListProviders() []watchsync.ProviderSummary { return nil }
func (f *fakeWatchLifecycle) GetConnectionStatus(_ context.Context, u int, p, key string) (watchsync.ConnectionStatus, error) {
	f.user, f.profile, f.key = u, p, key
	if f.version.ID == "" && !f.missingConnection {
		f.version = watchsync.ConnectionVersion{ID: "connection-1", UpdatedAt: fixedTime()}
	}
	return watchsync.ConnectionStatus{Version: f.version, Provider: key, Connected: !f.missingConnection, CredentialsConfigured: f.credentialsConfigured, DisplayName: f.displayName}, f.err
}
func (f *fakeWatchLifecycle) PollDeviceAuth(_ context.Context, u int, p, key, id string) (watchsync.Connection, error) {
	f.user, f.profile, f.key = u, p, key
	return watchsync.Connection{AccessToken: "secret-token"}, f.err
}
func (f *fakeWatchLifecycle) StartDeviceAuth(_ context.Context, u int, p, key string) (watchsync.DeviceAuthSession, error) {
	f.user, f.profile, f.key = u, p, key
	return watchsync.DeviceAuthSession{ID: "auth-1", DeviceCode: "private-code", ExpiresAt: fixedTime(), UserCode: "PUBLIC", Provider: key}, f.err
}
func (f *fakeWatchLifecycle) ListSyncRuns(_ context.Context, u int, p, key string, limit int) ([]watchsync.SyncRun, error) {
	f.user, f.profile, f.key, f.limit = u, p, key, limit
	return nil, f.err
}
func (f *fakeWatchLifecycle) RequestManualSync(_ context.Context, u int, p, key string) (watchsync.ManualSyncResult, error) {
	return watchsync.ManualSyncResult{Run: watchsync.SyncRun{ID: "run-1", ConnectionID: "connection-1", Provider: key, Trigger: "manual", Status: "running", StartedAt: fixedTime(), CreatedAt: fixedTime()}}, f.err
}
func lifecycleHandler(r *fakeLifecycle, w *fakeWatchLifecycle) http.Handler {
	deps := requestDeps(fixtureRequests())
	deps.RequestLifecycle = r
	deps.WatchProviders = w
	return NewHandler(deps)
}

func TestWatchProviderLifecycleScopeAndSecrets(t *testing.T) {
	w := &fakeWatchLifecycle{}
	h := lifecycleHandler(&fakeLifecycle{}, w)
	for _, path := range []string{"/watch-providers", "/watch-providers/trakt/connection", "/watch-providers/trakt/sync-runs"} {
		rec := do(t, h, http.MethodGet, Prefix+path, "", requestOwner)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if w.user != 1 || w.profile != "p-owner" || w.limit != 10 {
		t.Fatalf("scope/window: %+v", w)
	}
	for _, tc := range []struct{ path, body string }{{"device-code", ""}, {"poll", `{"auth_session_id":"00000000-0000-4000-8000-000000000001"}`}} {
		rec := do(t, h, http.MethodPost, Prefix+"/watch-providers/trakt/auth/"+tc.path, tc.body, requestOwner)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret-token") || strings.Contains(rec.Body.String(), "private-code") {
			t.Fatal("provider credential exposed")
		}
	}
	rec := do(t, h, http.MethodGet, Prefix+"/watch-providers/trakt/sync-runs?limit=51", "", requestOwner)
	requireProblem(t, rec, TypeValidationFailed)
	rec = do(t, h, http.MethodGet, Prefix+"/watch-providers/trakt/connection", "", nil)
	requireProblem(t, rec, TypeAuthenticationRequired)
}
func TestWatchProviderLifecycleErrors(t *testing.T) {
	w := &fakeWatchLifecycle{}
	h := lifecycleHandler(&fakeLifecycle{}, w)
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{{watchsync.UnknownProviderError{Key: "missing"}, TypeNotFound}, {watchsync.ErrAuthSessionMismatch, TypeNotFound}, {watchsync.ErrAuthSessionCompleted, TypeConflict}, {fmt.Errorf("provider rejected the key: %w", watchsync.ErrInvalidCredential), TypeValidationFailed}, {errors.New("private database failure"), TypeInternalError}} {
		w.err = tc.err
		rec := do(t, h, http.MethodPost, Prefix+"/watch-providers/trakt/auth/poll", `{"auth_session_id":"00000000-0000-4000-8000-000000000001"}`, requestOwner)
		requireProblem(t, rec, tc.want)
		if strings.Contains(rec.Body.String(), "private database") {
			t.Fatal("internal error exposed")
		}
	}
	w.err = watchsync.SyncCooldownError{RetryAfterSeconds: 42}
	rec := do(t, h, http.MethodPost, Prefix+"/watch-providers/trakt/sync", "", requestOwner)
	requireProblem(t, rec, TypeRateLimited)
	if rec.Header().Get("Retry-After") != "42" {
		t.Fatal("cooldown header lost")
	}
	w.err = watchsync.RateLimitedError{Provider: "trakt", RetryAfter: 1500 * time.Millisecond}
	rec = do(t, h, http.MethodPost, Prefix+"/watch-providers/trakt/auth/poll", `{"auth_session_id":"00000000-0000-4000-8000-000000000001"}`, requestOwner)
	requireProblem(t, rec, TypeRateLimited)
	if rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("provider rate limit Retry-After = %q, want 2", rec.Header().Get("Retry-After"))
	}
}

// A built-in provider that rejects the supplied API key is a client problem,
// not an internal failure, on the connect route.
func TestWatchProviderConnectAPIKeyRejectedCredential(t *testing.T) {
	w := &fakeWatchLifecycle{err: fmt.Errorf("mdblist request GET /user rejected: status 401 (check api key): %w", watchsync.ErrInvalidCredential)}
	h := lifecycleHandler(&fakeLifecycle{}, w)
	rec := do(t, h, http.MethodPost, Prefix+"/watch-providers/mdblist/auth/api-key", `{"api_key":"wrong-key"}`, requestOwner)
	requireProblem(t, rec, TypeValidationFailed)
	if strings.Contains(rec.Body.String(), "wrong-key") {
		t.Fatal("supplied credential echoed back")
	}
}

func TestRequestLifecycleViewerAndCancel(t *testing.T) {
	r := &fakeLifecycle{}
	h := lifecycleHandler(r, &fakeWatchLifecycle{})
	rec := do(t, h, http.MethodGet, Prefix+"/requests/status", "", requestOwner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, Prefix+"/requests/r-1/cancel", `{"reason":"No longer needed"}`, requestOwner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if r.viewer.UserID != 1 || r.viewer.ProfileID != "p-owner" || r.viewer.IsAdmin || r.id != "r-1" || r.reason != "No longer needed" {
		t.Fatalf("wrong viewer or input: %+v", r)
	}
	r.err = mediarequests.ErrForbidden
	rec = do(t, h, http.MethodPost, Prefix+"/requests/r-2/cancel", `{}`, requestOwner)
	requireProblem(t, rec, TypePermissionDenied)
}

func (f *fakeWatchLifecycle) UpdateConnection(ctx context.Context, u int, p, key string, update watchsync.ConnectionUpdate) (watchsync.ConnectionStatus, error) {
	return f.GetConnectionStatus(ctx, u, p, key)
}
func (f *fakeWatchLifecycle) DeleteConnection(_ context.Context, u int, p, key string) error {
	f.user, f.profile, f.key = u, p, key
	return f.err
}
func (f *fakeWatchLifecycle) ConnectAPIKeyWithConfig(_ context.Context, u int, p, key, apiKey string, config watchsync.ConnectionConfigValues) (watchsync.Connection, error) {
	f.user, f.profile, f.key = u, p, key
	return watchsync.Connection{AccessToken: "secret-token"}, f.err
}

func requestLifecycleFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	cases := []fixtureCase{
		{name: "watch_providers_ok", operationID: "listWatchProviders", method: http.MethodGet, path: Prefix + "/watch-providers", schema: "CollectionWatchProviderSummary"},
		{name: "watch_provider_runs_ok", operationID: "listWatchProviderSyncRuns", method: http.MethodGet, path: Prefix + "/watch-providers/trakt/sync-runs", schema: "CollectionWatchProviderSyncRun"},
		{name: "watch_provider_sync_ok", operationID: "triggerWatchProviderSync", method: http.MethodPost, path: Prefix + "/watch-providers/trakt/sync", schema: "WatchProviderSyncOutputBody", status: http.StatusAccepted},
		{name: "watch_provider_delete_ok", operationID: "deleteWatchProviderConnection", method: http.MethodDelete, path: Prefix + "/watch-providers/trakt/connection", status: http.StatusNoContent},
		{name: "request_status_ok", operationID: "getRequestStatus", method: http.MethodGet, path: Prefix + "/requests/status", schema: "FeatureStatus"},
		{name: "cancel_request_ok", operationID: "cancelRequest", method: http.MethodPost, path: Prefix + "/requests/r-1/cancel", body: `{"reason":"No longer needed"}`, schema: "MediaRequest"},
		{name: "watch_provider_connection_ok", operationID: opGetWatchProviderConnection, method: http.MethodGet, path: Prefix + "/watch-providers/trakt/connection", schema: "WatchProviderConnection"},
		{name: "watch_provider_device_auth_ok", operationID: "startWatchProviderDeviceAuth", method: http.MethodPost, path: Prefix + "/watch-providers/trakt/auth/device-code", schema: "WatchProviderDeviceAuth"},
		{name: "watch_provider_poll_ok", operationID: "pollWatchProviderDeviceAuth", method: http.MethodPost, path: Prefix + "/watch-providers/trakt/auth/poll", body: `{"auth_session_id":"00000000-0000-4000-8000-000000000001"}`, schema: "WatchProviderConnection"},
		{name: "watch_provider_api_key_ok", operationID: "connectWatchProviderAPIKey", method: http.MethodPost, path: Prefix + "/watch-providers/trakt/auth/api-key", body: `{"api_key":"synthetic-token"}`, schema: "WatchProviderConnection"},
		{name: "watch_provider_update_ok", operationID: "updateWatchProviderConnection", method: http.MethodPatch, path: Prefix + "/watch-providers/trakt/connection", body: `{"scrobble_enabled":true}`, schema: "WatchProviderSettings"},
		{name: "watch_provider_settings_ok", operationID: "getWatchProviderSettings", method: http.MethodGet, path: Prefix + "/watch-providers/trakt/connection/settings", schema: "WatchProviderSettings"},
	}
	for i := range cases {
		cases[i].headers = viewer
		if cases[i].operationID == "updateWatchProviderConnection" {
			fake := new(fakeWatchLifecycle)
			current, _ := fake.GetConnectionStatus(context.Background(), 1, "p-owner", "trakt")
			cases[i].headers = with(viewer, "If-Match", watchConnectionTag(1, "p-owner", "trakt", current).String())
		}
		if cases[i].operationID == "getWatchProviderSettings" || cases[i].operationID == "updateWatchProviderConnection" {
			cases[i].assertHeaders = []string{"Content-Type", "Cache-Control", "ETag"}
		}

		if cases[i].status == 0 {
			cases[i].status = http.StatusOK
		}
		cases[i].scenario = "Profile-scoped lifecycle response with synthetic provider state."
		if len(cases[i].assertHeaders) == 0 {
			cases[i].assertHeaders = []string{"Content-Type", "Cache-Control"}
		}
		if cases[i].status == http.StatusNoContent {
			cases[i].assertHeaders = []string{"Cache-Control"}
		} else {
			cases[i].schema = "#/components/schemas/" + cases[i].schema
		}
	}
	return cases
}

func (f *fakeWatchLifecycle) UpdateConnectionConditional(ctx context.Context, u int, p, key string, expected watchsync.ConnectionVersion, update watchsync.ConnectionUpdate) (watchsync.ConnectionStatus, error) {
	f.updateCalls++
	if f.staleUpdate {
		f.version.UpdatedAt = f.version.UpdatedAt.Add(time.Microsecond)
		return watchsync.ConnectionStatus{}, watchsync.ErrStaleConnection
	}
	if expected != f.version {
		return watchsync.ConnectionStatus{}, watchsync.ErrStaleConnection
	}
	f.version.UpdatedAt = f.version.UpdatedAt.Add(time.Microsecond)
	return f.GetConnectionStatus(ctx, u, p, key)
}

func TestWatchProviderConnectionGuard(t *testing.T) {
	fake := &fakeWatchLifecycle{}
	h := lifecycleHandler(&fakeLifecycle{}, fake)
	path := Prefix + "/watch-providers/trakt/connection"
	get := do(t, h, http.MethodGet, path+"/settings", "", requestOwner)
	if get.Code != 200 || get.Header().Get("ETag") == "" {
		t.Fatalf("GET=%d %s", get.Code, get.Body.String())
	}
	tag := get.Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"scrobble_enabled":false}`, requestOwner), TypePreconditionRequired)
	headers := maps.Clone(requestOwner)
	headers["If-Match"] = `"stale"`
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"scrobble_enabled":false}`, headers), TypePreconditionFailed)
	if fake.updateCalls != 0 {
		t.Fatal("failed precondition reached update")
	}
	headers["If-Match"] = tag
	success := do(t, h, http.MethodPatch, path, `{"scrobble_enabled":false}`, headers)
	if success.Code != 200 || success.Header().Get("ETag") == tag {
		t.Fatalf("PATCH=%d %s", success.Code, success.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"scrobble_enabled":false}`, headers), TypePreconditionFailed)
	headers["If-Match"] = success.Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodPatch, Prefix+"/watch-providers/simkl/connection", `{"scrobble_enabled":false}`, headers), TypePreconditionFailed)
	fake.staleUpdate = true
	raced := do(t, h, http.MethodPatch, path, `{"scrobble_enabled":false}`, headers)
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") == "" || raced.Header().Get("ETag") == headers["If-Match"] {
		t.Fatal("stale storage comparison did not return latest validator")
	}
}

func TestWatchProviderMetadataHasNoSettingsValidator(t *testing.T) {
	fake := &fakeWatchLifecycle{displayName: "Before"}
	h := lifecycleHandler(&fakeLifecycle{}, fake)
	path := Prefix + "/watch-providers/trakt/connection"
	metadata := do(t, h, http.MethodGet, path, "", requestOwner)
	if metadata.Code != 200 || metadata.Header().Get("ETag") != "" {
		t.Fatalf("metadata GET=%d etag=%q", metadata.Code, metadata.Header().Get("ETag"))
	}
	before := do(t, h, http.MethodGet, path+"/settings", "", requestOwner)
	if before.Code != 200 || before.Header().Get("ETag") == "" {
		t.Fatalf("settings GET=%d", before.Code)
	}
	fake.credentialsConfigured = true
	fake.displayName = "After"
	changed := do(t, h, http.MethodGet, path, "", requestOwner)
	if changed.Code != 200 || changed.Header().Get("ETag") != "" || changed.Body.String() == metadata.Body.String() {
		t.Fatal("metadata change retained a validator or failed to change representation")
	}
	after := do(t, h, http.MethodGet, path+"/settings", "", requestOwner)
	if before.Header().Get("ETag") != after.Header().Get("ETag") || before.Body.String() != after.Body.String() {
		t.Fatal("dynamic provider metadata changed guarded settings")
	}
	var fields map[string]any
	if err := json.Unmarshal(after.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 14 {
		t.Fatalf("settings fields=%v", fields)
	}
	for key, value := range fields {
		if _, ok := value.(bool); !ok {
			t.Fatalf("non-preference field %s=%v", key, value)
		}
	}
	headers := maps.Clone(requestOwner)
	headers["If-Match"] = before.Header().Get("ETag")
	patched := do(t, h, http.MethodPatch, path, `{"scrobble_enabled":true}`, headers)
	if patched.Code != 200 {
		t.Fatalf("metadata invalidated patch: %d %s", patched.Code, patched.Body.String())
	}
	fields = nil
	if err := json.Unmarshal(patched.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 14 {
		t.Fatalf("PATCH returned metadata: %v", fields)
	}
}

func TestWatchProviderSettingsMissingConnection(t *testing.T) {
	fake := &fakeWatchLifecycle{missingConnection: true}
	h := lifecycleHandler(&fakeLifecycle{}, fake)
	path := Prefix + "/watch-providers/trakt/connection"
	status := do(t, h, http.MethodGet, path, "", requestOwner)
	if status.Code != 200 || status.Header().Get("ETag") != "" {
		t.Fatalf("disconnected status=%d %s", status.Code, status.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path+"/settings", "", requestOwner), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"scrobble_enabled":true}`, requestOwner), TypeNotFound)
}

func (f *fakeLifecycle) RequestCapabilityAllowed(context.Context, mediarequests.Viewer) (bool, error) {
	return true, f.err
}

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeAutoscanStore struct {
	getSettingsFn      func() (autoscan.Settings, error)
	updateSettingsFn   func(autoscan.Settings) (autoscan.Settings, error)
	listConnectionsFn  func() ([]autoscan.Connection, error)
	createConnectionFn func(autoscan.Connection) (autoscan.Connection, error)
	updateConnectionFn func(autoscan.Connection) (autoscan.Connection, error)
	deleteConnectionFn func(string) error
	listSourcesFn      func() ([]autoscan.Source, error)
	getSourceFn        func(string) (autoscan.Source, error)
	createSourceFn     func(autoscan.Source) (autoscan.Source, error)
	updateSourceFn     func(autoscan.Source) (autoscan.Source, error)
	deleteSourceFn     func(string) error
}

func (f *fakeAutoscanStore) GetSettings(context.Context) (autoscan.Settings, error) {
	if f.getSettingsFn != nil {
		return f.getSettingsFn()
	}
	return autoscan.Settings{}, nil
}

func (f *fakeAutoscanStore) UpdateSettings(_ context.Context, s autoscan.Settings) (autoscan.Settings, error) {
	if f.updateSettingsFn != nil {
		return f.updateSettingsFn(s)
	}
	return s, nil
}

func (f *fakeAutoscanStore) ListConnections(context.Context) ([]autoscan.Connection, error) {
	if f.listConnectionsFn != nil {
		return f.listConnectionsFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) CreateConnection(_ context.Context, c autoscan.Connection) (autoscan.Connection, error) {
	if f.createConnectionFn != nil {
		return f.createConnectionFn(c)
	}
	return c, nil
}

func (f *fakeAutoscanStore) UpdateConnection(_ context.Context, c autoscan.Connection) (autoscan.Connection, error) {
	if f.updateConnectionFn != nil {
		return f.updateConnectionFn(c)
	}
	return c, nil
}

func (f *fakeAutoscanStore) DeleteConnection(_ context.Context, id string) error {
	if f.deleteConnectionFn != nil {
		return f.deleteConnectionFn(id)
	}
	return nil
}

func (f *fakeAutoscanStore) ListSources(context.Context) ([]autoscan.Source, error) {
	if f.listSourcesFn != nil {
		return f.listSourcesFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) GetSource(_ context.Context, id string) (autoscan.Source, error) {
	if f.getSourceFn != nil {
		return f.getSourceFn(id)
	}
	return autoscan.Source{ID: id}, nil
}

func (f *fakeAutoscanStore) CreateSource(_ context.Context, s autoscan.Source) (autoscan.Source, error) {
	if f.createSourceFn != nil {
		return f.createSourceFn(s)
	}
	return s, nil
}

func (f *fakeAutoscanStore) UpdateSource(_ context.Context, s autoscan.Source) (autoscan.Source, error) {
	if f.updateSourceFn != nil {
		return f.updateSourceFn(s)
	}
	return s, nil
}

func (f *fakeAutoscanStore) DeleteSource(_ context.Context, id string) error {
	if f.deleteSourceFn != nil {
		return f.deleteSourceFn(id)
	}
	return nil
}

// fakeTriggerUpdater records the last UpdateTriggers call so a test can assert
// the handler reschedules the poll task with the new interval.
type fakeTriggerUpdater struct {
	called   bool
	key      string
	triggers []taskmanager.TriggerConfig
	err      error
}

func (f *fakeTriggerUpdater) UpdateTriggers(key string, cfgs []taskmanager.TriggerConfig) error {
	f.called = true
	f.key = key
	f.triggers = cfgs
	return f.err
}

type fakeAutoscanTriggerer struct {
	called bool
	err    error
	// available is returned by ListAvailableScanSources (the Add-source picker /
	// create-validation set).
	available []autoscan.AvailableScanSource
	// testResult / testErr drive TestConnection + TestConnectionByID.
	testResult autoscan.ConnectionTestResult
	testErr    error
	// suggestions / suggestErr drive SuggestRewrites.
	suggestions autoscan.RewriteSuggestions
	suggestErr  error
	// done, when non-nil, receives once PollOnce runs. HandleTrigger dispatches
	// PollOnce on a detached goroutine, so tests synchronize on this instead of
	// reading `called` straight after the handler returns (which races the
	// goroutine and is also an unsynchronized read of `called`). The channel
	// send happens-before the test's receive, so reading `called` afterwards is
	// race-free.
	done chan struct{}
}

func (f *fakeAutoscanTriggerer) PollOnce(context.Context) error {
	f.called = true
	if f.done != nil {
		f.done <- struct{}{}
	}
	return f.err
}

func (f *fakeAutoscanTriggerer) ListAvailableScanSources(context.Context) ([]autoscan.AvailableScanSource, error) {
	return f.available, nil
}

func (f *fakeAutoscanTriggerer) TestConnection(context.Context, autoscan.Connection) (autoscan.ConnectionTestResult, error) {
	return f.testResult, f.testErr
}

func (f *fakeAutoscanTriggerer) TestConnectionByID(context.Context, string) (autoscan.ConnectionTestResult, error) {
	return f.testResult, f.testErr
}

func (f *fakeAutoscanTriggerer) SuggestRewrites(context.Context, string) (autoscan.RewriteSuggestions, error) {
	return f.suggestions, f.suggestErr
}

func newAutoscanRequest(method, target, body, id string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if id != "" {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", id)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
	}
	return r
}

func TestAutoscanHandleGetSettingsReturnsJSON(t *testing.T) {
	store := &fakeAutoscanStore{
		getSettingsFn: func() (autoscan.Settings, error) {
			return autoscan.Settings{Enabled: true, DefaultPollIntervalSeconds: 300, DebounceSeconds: 30}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	rec := httptest.NewRecorder()
	h.HandleGetSettings(rec, httptest.NewRequest("GET", "/api/v1/admin/autoscan/settings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body autoscanSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || body.DefaultPollIntervalSeconds != 300 {
		t.Errorf("settings = %+v", body)
	}
}

func TestAutoscanHandleUpdateSettingsRejectsZeroInterval(t *testing.T) {
	h := NewAutoscanHandler(&fakeAutoscanStore{}, &fakeAutoscanTriggerer{})

	req := httptest.NewRequest("PUT", "/api/v1/admin/autoscan/settings",
		strings.NewReader(`{"enabled":true,"default_poll_interval_seconds":0,"debounce_seconds":10}`))
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleListSourcesListsOnly(t *testing.T) {
	// Sources are now operator-created (no auto-seed on list). The list endpoint
	// must simply return the stored rows.
	listed := false
	store := &fakeAutoscanStore{
		listSourcesFn: func() ([]autoscan.Source, error) {
			listed = true
			return []autoscan.Source{{ID: "src-1", InstallationID: 1, CapabilityID: "arr"}}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	rec := httptest.NewRecorder()
	h.HandleListSources(rec, newAutoscanRequest("GET", "/api/v1/admin/autoscan/sources", "", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !listed {
		t.Fatal("HandleListSources did not list stored sources")
	}
	var body struct {
		Sources []autoscanSourceResponse `json:"sources"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sources) != 1 || body.Sources[0].ID != "src-1" {
		t.Fatalf("unexpected sources: %+v", body.Sources)
	}
}

func TestAutoscanHandleListAvailableScanSources(t *testing.T) {
	trig := &fakeAutoscanTriggerer{available: []autoscan.AvailableScanSource{
		{InstallationID: 1, CapabilityID: "arr", PluginID: "sonarr", DisplayName: "Sonarr"},
	}}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	rec := httptest.NewRecorder()
	h.HandleListAvailableScanSources(rec, newAutoscanRequest("GET", "/api/v1/admin/autoscan/scan-source-plugins", "", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Plugins []autoscanScanSourcePluginResponse `json:"plugins"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Plugins) != 1 || body.Plugins[0].PluginID != "sonarr" || body.Plugins[0].DisplayName != "Sonarr" {
		t.Fatalf("unexpected plugins: %+v", body.Plugins)
	}
}

func TestAutoscanHandleCreateSourceSucceeds(t *testing.T) {
	var got autoscan.Source
	store := &fakeAutoscanStore{
		createSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			got = s
			s.ID = "new-src"
			return s, nil
		},
	}
	trig := &fakeAutoscanTriggerer{available: []autoscan.AvailableScanSource{
		{InstallationID: 1, CapabilityID: "arr"},
	}}
	h := NewAutoscanHandler(store, trig)

	req := newAutoscanRequest("POST", "/api/v1/admin/autoscan/sources",
		`{"installation_id":1,"capability_id":"arr","connection_id":"conn-1","enabled":true}`, "")
	rec := httptest.NewRecorder()
	h.HandleCreateSource(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if got.InstallationID != 1 || got.CapabilityID != "arr" || got.ConnectionID == nil || *got.ConnectionID != "conn-1" {
		t.Fatalf("unexpected created source: %+v", got)
	}
	var body autoscanSourceResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != "new-src" {
		t.Fatalf("expected created id echoed, got %+v", body)
	}
}

func TestAutoscanHandleCreateSourceRejectsUnknownCapability(t *testing.T) {
	created := false
	store := &fakeAutoscanStore{
		createSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			created = true
			return s, nil
		},
	}
	// Installed set does NOT include (1, "arr").
	trig := &fakeAutoscanTriggerer{available: []autoscan.AvailableScanSource{
		{InstallationID: 2, CapabilityID: "other"},
	}}
	h := NewAutoscanHandler(store, trig)

	req := newAutoscanRequest("POST", "/api/v1/admin/autoscan/sources",
		`{"installation_id":1,"capability_id":"arr","enabled":false}`, "")
	rec := httptest.NewRecorder()
	h.HandleCreateSource(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if created {
		t.Fatal("CreateSource was called for an uninstalled capability")
	}
}

func TestAutoscanHandleCreateSourceEnableWithoutConnectionReturns400(t *testing.T) {
	created := false
	store := &fakeAutoscanStore{
		createSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			created = true
			return s, nil
		},
	}
	trig := &fakeAutoscanTriggerer{available: []autoscan.AvailableScanSource{
		{InstallationID: 1, CapabilityID: "arr"},
	}}
	h := NewAutoscanHandler(store, trig)

	req := newAutoscanRequest("POST", "/api/v1/admin/autoscan/sources",
		`{"installation_id":1,"capability_id":"arr","enabled":true}`, "")
	rec := httptest.NewRecorder()
	h.HandleCreateSource(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if created {
		t.Fatal("CreateSource was called when enabling without a connection")
	}
}

func TestAutoscanHandleTestConnectionOK(t *testing.T) {
	trig := &fakeAutoscanTriggerer{testResult: autoscan.ConnectionTestResult{OK: true, Version: "4.0.1"}}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	req := newAutoscanRequest("POST", "/api/v1/admin/autoscan/connections/test",
		`{"base_url":"http://radarr:7878","api_key_ref":"k"}`, "")
	rec := httptest.NewRecorder()
	h.HandleTestConnection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body autoscanTestConnectionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Version != "4.0.1" {
		t.Fatalf("unexpected test result: %+v", body)
	}
}

func TestAutoscanHandleTestConnectionFailureIs200WithError(t *testing.T) {
	trig := &fakeAutoscanTriggerer{testResult: autoscan.ConnectionTestResult{OK: false, Err: "arr: HTTP 401"}}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	req := newAutoscanRequest("POST", "/api/v1/admin/autoscan/connections/test",
		`{"connection_id":"conn-1"}`, "")
	rec := httptest.NewRecorder()
	h.HandleTestConnection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body autoscanTestConnectionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.OK || body.Error == "" {
		t.Fatalf("expected ok=false with error, got %+v", body)
	}
}

func TestAutoscanHandleTestConnectionRejectsEmptyInput(t *testing.T) {
	h := NewAutoscanHandler(&fakeAutoscanStore{}, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("POST", "/api/v1/admin/autoscan/connections/test", `{}`, "")
	rec := httptest.NewRecorder()
	h.HandleTestConnection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleRewriteSuggestions(t *testing.T) {
	trig := &fakeAutoscanTriggerer{suggestions: autoscan.RewriteSuggestions{
		Proposed:  []autoscan.ProposedRewrite{{From: "/data/tv", To: "/mnt/media/tv", MatchDepth: 1}},
		Unmatched: []string{},
		Ambiguous: []autoscan.AmbiguousRoot{},
		Covered:   []string{},
	}}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	req := newAutoscanRequest("GET", "/api/v1/admin/autoscan/sources/src-1/rewrite-suggestions", "", "src-1")
	rec := httptest.NewRecorder()
	h.HandleRewriteSuggestions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body autoscan.RewriteSuggestions
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Proposed) != 1 || body.Proposed[0].From != "/data/tv" {
		t.Fatalf("unexpected suggestions: %+v", body)
	}
}

func TestAutoscanHandleRewriteSuggestionsNoConnectionReturns400(t *testing.T) {
	trig := &fakeAutoscanTriggerer{suggestErr: autoscan.ErrNoConnection}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	req := newAutoscanRequest("GET", "/api/v1/admin/autoscan/sources/src-1/rewrite-suggestions", "", "src-1")
	rec := httptest.NewRecorder()
	h.HandleRewriteSuggestions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleListConnectionsOmitsSecrets(t *testing.T) {
	store := &fakeAutoscanStore{
		listConnectionsFn: func() ([]autoscan.Connection, error) {
			return []autoscan.Connection{
				{
					ID:        "conn-1",
					Name:      "Radarr",
					Kind:      "radarr",
					BaseURL:   "http://radarr.internal:7878",
					APIKeyRef: "secret-ref-xyz",
				},
			}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	rec := httptest.NewRecorder()
	h.HandleListConnections(rec, httptest.NewRequest("GET", "/api/v1/admin/autoscan/connections", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "api_key_ref") || strings.Contains(raw, "secret-ref-xyz") {
		t.Errorf("connections response leaks api_key_ref: %s", raw)
	}
	// has_api_key should be reported true (so operators know a key is set) without
	// disclosing the ref itself.
	var body struct {
		Connections []autoscanConnectionResponse `json:"connections"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Connections) != 1 || !body.Connections[0].HasAPIKey {
		t.Errorf("connections = %+v", body.Connections)
	}
}

func TestAutoscanHandleCreateConnectionRejectsBothEmpty(t *testing.T) {
	created := false
	store := &fakeAutoscanStore{
		createConnectionFn: func(c autoscan.Connection) (autoscan.Connection, error) {
			created = true
			return c, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := httptest.NewRequest("POST", "/api/v1/admin/autoscan/connections",
		strings.NewReader(`{"name":"x"}`))
	rec := httptest.NewRecorder()
	h.HandleCreateConnection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if created {
		t.Fatal("CreateConnection was called for a both-empty connection")
	}
}

func TestAutoscanHandleUpdateConnectionRejectsBothEmpty(t *testing.T) {
	updated := false
	store := &fakeAutoscanStore{
		updateConnectionFn: func(c autoscan.Connection) (autoscan.Connection, error) {
			updated = true
			return c, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// Whitespace-only request_integration_id must count as absent.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/connections/conn-1",
		`{"name":"x","base_url":"","request_integration_id":"  "}`, "conn-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateConnection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if updated {
		t.Fatal("UpdateConnection was called for a both-empty connection")
	}
}

func TestAutoscanHandleUpdateSourceNotFoundReturns404(t *testing.T) {
	store := &fakeAutoscanStore{
		updateSourceFn: func(autoscan.Source) (autoscan.Source, error) {
			return autoscan.Source{}, autoscan.ErrNotFound
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/missing",
		`{"enabled":true,"connection_id":"conn-1"}`, "missing")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleUpdateSourceEnableWithoutConnectionReturns400(t *testing.T) {
	upserted := false
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			// Existing source has no connection bound yet.
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: nil}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			upserted = true
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":true}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if upserted {
		t.Fatal("UpsertSource was called when enabling without a connection")
	}
}

func TestAutoscanHandleUpdateSourceEnableWithConnectionSucceeds(t *testing.T) {
	var got autoscan.Source
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: ptr("conn-1")}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			got = s
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// Full-state update: enable=true and supply the connection_id explicitly.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":true,"connection_id":"conn-1"}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.ConnectionID == nil || *got.ConnectionID != "conn-1" {
		t.Fatalf("expected connection bound, got %+v", got.ConnectionID)
	}
}

func TestAutoscanHandleUpdateSourceBindConnectionSucceeds(t *testing.T) {
	var got autoscan.Source
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			// Source starts with no connection.
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: nil}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			got = s
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// Bind a connection while leaving the source disabled.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":false,"connection_id":"conn-42"}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.ConnectionID == nil || *got.ConnectionID != "conn-42" {
		t.Fatalf("expected connection bound to conn-42, got %+v", got.ConnectionID)
	}
}

func TestAutoscanHandleUpdateSourceUnbindConnectionSucceeds(t *testing.T) {
	var got autoscan.Source
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			// Source starts with a connection already bound.
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: ptr("conn-1")}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			got = s
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// Unbind: send connection_id: null explicitly with enabled: false.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":false,"connection_id":null}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.ConnectionID != nil {
		t.Fatalf("expected connection unbound (nil), got %+v", got.ConnectionID)
	}
}

func TestAutoscanHandleUpdateSourceUnbindWhileEnabledReturns400(t *testing.T) {
	upserted := false
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: ptr("conn-1")}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			upserted = true
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// Attempting to enable=true while sending connection_id: null must be rejected.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":true,"connection_id":null}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if upserted {
		t.Fatal("UpsertSource was called when enabling with null connection")
	}
}

func TestAutoscanHandleDeleteConnectionNotFoundReturns404(t *testing.T) {
	store := &fakeAutoscanStore{
		deleteConnectionFn: func(string) error {
			return autoscan.ErrNotFound
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("DELETE", "/api/v1/admin/autoscan/connections/missing", "", "missing")
	rec := httptest.NewRecorder()
	h.HandleDeleteConnection(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleUpdateConnectionBlankKeyPassedThroughForKeep(t *testing.T) {
	// Fix 1: a metadata-only edit omits api_key_ref ("leave blank to keep
	// existing"). The handler must pass a blank api_key_ref through to the repo,
	// whose UpdateConnection SQL keeps the stored key (CASE WHEN $5 = '' THEN
	// api_key_ref ...). Here we assert the handler does NOT fabricate/clear a key:
	// it forwards the empty string so the repo's keep-semantics fire.
	var got autoscan.Connection
	store := &fakeAutoscanStore{
		updateConnectionFn: func(c autoscan.Connection) (autoscan.Connection, error) {
			got = c
			// Emulate the repo keep-semantics: a blank incoming ref leaves the
			// previously-stored key in place.
			if strings.TrimSpace(c.APIKeyRef) == "" {
				c.APIKeyRef = "existing-stored-ref"
			}
			return c, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// Update with base_url present but api_key_ref absent from the JSON.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/connections/conn-1",
		`{"name":"Radarr","kind":"radarr","base_url":"http://radarr:7878"}`, "conn-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateConnection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Handler must forward a blank api_key_ref (so the repo keeps the existing key).
	if strings.TrimSpace(got.APIKeyRef) != "" {
		t.Fatalf("expected handler to forward blank api_key_ref, got %q", got.APIKeyRef)
	}
	// And the response must still report HasAPIKey=true (the stored key survived).
	var body autoscanConnectionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.HasAPIKey {
		t.Fatalf("expected preserved key reflected as has_api_key=true, got %+v", body)
	}
}

func TestAutoscanHandleDeleteSourceSucceeds(t *testing.T) {
	deleted := ""
	store := &fakeAutoscanStore{
		deleteSourceFn: func(id string) error {
			deleted = id
			return nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("DELETE", "/api/v1/admin/autoscan/sources/src-1", "", "src-1")
	rec := httptest.NewRecorder()
	h.HandleDeleteSource(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if deleted != "src-1" {
		t.Fatalf("expected DeleteSource called with src-1, got %q", deleted)
	}
}

func TestAutoscanHandleDeleteSourceNotFoundReturns404(t *testing.T) {
	store := &fakeAutoscanStore{
		deleteSourceFn: func(string) error {
			return autoscan.ErrNotFound
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("DELETE", "/api/v1/admin/autoscan/sources/missing", "", "missing")
	rec := httptest.NewRecorder()
	h.HandleDeleteSource(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleUpdateSettingsReschedulesPollTask(t *testing.T) {
	// Fix 5: a successful settings update must reschedule the poll task with the
	// new default_poll_interval_seconds.
	store := &fakeAutoscanStore{
		updateSettingsFn: func(s autoscan.Settings) (autoscan.Settings, error) {
			return s, nil
		},
	}
	trig := &fakeTriggerUpdater{}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})
	h.SetTriggerUpdater(trig)

	req := httptest.NewRequest("PUT", "/api/v1/admin/autoscan/settings",
		strings.NewReader(`{"enabled":true,"default_poll_interval_seconds":300,"debounce_seconds":30}`))
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !trig.called {
		t.Fatal("expected UpdateTriggers to be called on settings update")
	}
	if trig.key != "autoscan_poll" {
		t.Fatalf("rescheduled wrong task key: %q", trig.key)
	}
	if len(trig.triggers) != 1 || trig.triggers[0].Type != taskmanager.TriggerTypeInterval {
		t.Fatalf("unexpected triggers: %+v", trig.triggers)
	}
	// 300 seconds -> 300_000 ms.
	if trig.triggers[0].IntervalMs != 300*1000 {
		t.Fatalf("interval = %d ms, want 300000", trig.triggers[0].IntervalMs)
	}
}

func TestAutoscanHandleUpdateSettingsWithoutTriggerUpdaterSucceeds(t *testing.T) {
	// Fix 5: the reschedule dep is optional; a nil updater must not break the
	// settings update.
	store := &fakeAutoscanStore{}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := httptest.NewRequest("PUT", "/api/v1/admin/autoscan/settings",
		strings.NewReader(`{"enabled":true,"default_poll_interval_seconds":300,"debounce_seconds":30}`))
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleTriggerInvokesPollOnce(t *testing.T) {
	trig := &fakeAutoscanTriggerer{done: make(chan struct{}, 1)}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	rec := httptest.NewRecorder()
	h.HandleTrigger(rec, httptest.NewRequest("POST", "/api/v1/admin/autoscan/trigger", nil))

	// The handler responds immediately and runs PollOnce on a detached
	// goroutine; wait (bounded) for that goroutine to fire.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	select {
	case <-trig.done:
	case <-time.After(2 * time.Second):
		t.Fatal("PollOnce was not invoked within timeout")
	}
	if !trig.called {
		t.Fatal("PollOnce was not invoked")
	}
}

func TestAutoscanHandleUpdateSourceRoundTripsPathRewrites(t *testing.T) {
	var got autoscan.Source
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: ptr("conn-1")}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			got = s
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":true,"connection_id":"conn-1","path_rewrites":[{"from":"/data/tv","to":"/mnt/media/tv"}]}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The repo received the rewrite.
	if len(got.PathRewrites) != 1 || got.PathRewrites[0].From != "/data/tv" || got.PathRewrites[0].To != "/mnt/media/tv" {
		t.Fatalf("expected path_rewrites passed to repo, got %+v", got.PathRewrites)
	}
	// The response echoes it back.
	var body autoscanSourceResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.PathRewrites) != 1 || body.PathRewrites[0].From != "/data/tv" || body.PathRewrites[0].To != "/mnt/media/tv" {
		t.Fatalf("response missing path_rewrites: %+v", body.PathRewrites)
	}
}

func TestAutoscanHandleUpdateSourceRejectsBlankRewrite(t *testing.T) {
	upserted := false
	store := &fakeAutoscanStore{
		getSourceFn: func(id string) (autoscan.Source, error) {
			return autoscan.Source{ID: id, InstallationID: 1, CapabilityID: "arr", ConnectionID: ptr("conn-1")}, nil
		},
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			upserted = true
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	// A rewrite with a blank "to" must be rejected with 400.
	req := newAutoscanRequest("PUT", "/api/v1/admin/autoscan/sources/src-1",
		`{"enabled":true,"connection_id":"conn-1","path_rewrites":[{"from":"/data/tv","to":""}]}`, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if upserted {
		t.Fatal("UpsertSource was called despite an invalid path_rewrite")
	}
}

func TestAutoscanHandleStatusReturnsTrimmedSources(t *testing.T) {
	store := &fakeAutoscanStore{
		getSettingsFn: func() (autoscan.Settings, error) {
			return autoscan.Settings{Enabled: true}, nil
		},
		listSourcesFn: func() ([]autoscan.Source, error) {
			return []autoscan.Source{
				{ID: "src-1", InstallationID: 7, CapabilityID: "scan_source", ConnectionID: ptr("conn-1"), Enabled: true},
			}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	rec := httptest.NewRecorder()
	h.HandleStatus(rec, httptest.NewRequest("GET", "/api/v1/admin/autoscan/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "base_url") || strings.Contains(raw, "api_key_ref") {
		t.Errorf("status leaks secrets: %s", raw)
	}
	var body autoscanStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || len(body.Sources) != 1 || body.Sources[0].ID != "src-1" {
		t.Errorf("status = %+v", body)
	}
}

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
	upsertSourceFn     func(autoscan.Source) (autoscan.Source, error)
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

func (f *fakeAutoscanStore) UpsertSource(_ context.Context, s autoscan.Source) (autoscan.Source, error) {
	if f.upsertSourceFn != nil {
		return f.upsertSourceFn(s)
	}
	return s, nil
}

type fakeAutoscanTriggerer struct {
	called bool
	err    error
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

func TestAutoscanHandleUpdateSourceNotFoundReturns404(t *testing.T) {
	store := &fakeAutoscanStore{
		getSourceFn: func(string) (autoscan.Source, error) {
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

func TestAutoscanHandleStatusReturnsTrimmedSources(t *testing.T) {
	store := &fakeAutoscanStore{
		getSettingsFn: func() (autoscan.Settings, error) {
			return autoscan.Settings{Enabled: true}, nil
		},
		listSourcesFn: func() ([]autoscan.Source, error) {
			return []autoscan.Source{
				{ID: "src-1", InstallationID: 7, CapabilityID: "scan_source", ConnectionID: "conn-1", Enabled: true},
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

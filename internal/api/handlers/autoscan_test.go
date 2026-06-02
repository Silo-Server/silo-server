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
	getSettingsFn    func() (autoscan.Settings, error)
	updateSettingsFn func(autoscan.Settings) (autoscan.Settings, error)
	listSourcesFn    func() ([]autoscan.Source, error)
	upsertSourceFn   func(string, autoscan.SourceUpdate) (*autoscan.Source, error)
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

func (f *fakeAutoscanStore) ListAllSources(context.Context) ([]autoscan.Source, error) {
	if f.listSourcesFn != nil {
		return f.listSourcesFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) UpsertSource(_ context.Context, id string, u autoscan.SourceUpdate) (*autoscan.Source, error) {
	if f.upsertSourceFn != nil {
		return f.upsertSourceFn(id, u)
	}
	return &autoscan.Source{IntegrationID: id, Enabled: u.Enabled, PathRewrites: u.PathRewrites}, nil
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

func (f *fakeAutoscanTriggerer) SuggestRewrites(context.Context, string) (autoscan.RewriteSuggestions, error) {
	return autoscan.RewriteSuggestions{}, nil
}

func TestAutoscanHandleGetSettingsReturnsJSON(t *testing.T) {
	store := &fakeAutoscanStore{
		getSettingsFn: func() (autoscan.Settings, error) {
			return autoscan.Settings{Enabled: true, PollIntervalMinutes: 5, DebounceSeconds: 30}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{}, nil)

	rec := httptest.NewRecorder()
	h.HandleGetSettings(rec, httptest.NewRequest("GET", "/api/v1/admin/autoscan/settings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body autoscan.Settings
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || body.PollIntervalMinutes != 5 {
		t.Errorf("settings = %+v", body)
	}
}

func TestAutoscanHandleUpdateSettingsRejectsZeroInterval(t *testing.T) {
	h := NewAutoscanHandler(&fakeAutoscanStore{}, &fakeAutoscanTriggerer{}, nil)

	req := httptest.NewRequest("PUT", "/api/v1/admin/autoscan/settings",
		strings.NewReader(`{"enabled":true,"poll_interval_minutes":0,"debounce_seconds":10}`))
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleListSourcesOmitsSecrets(t *testing.T) {
	now := time.Now()
	store := &fakeAutoscanStore{
		listSourcesFn: func() ([]autoscan.Source, error) {
			return []autoscan.Source{
				{
					IntegrationID: "radarr-1",
					Kind:          "radarr",
					Name:          "Radarr",
					BaseURL:       "http://radarr.internal:7878",
					APIKeyRef:     "secret-ref-xyz",
					Enabled:       true,
					LastPollAt:    &now,
				},
			}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{}, nil)

	rec := httptest.NewRecorder()
	h.HandleListSources(rec, httptest.NewRequest("GET", "/api/v1/admin/autoscan/sources", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "base_url") || strings.Contains(raw, "radarr.internal") {
		t.Errorf("response leaks base_url: %s", raw)
	}
	if strings.Contains(raw, "api_key_ref") || strings.Contains(raw, "secret-ref-xyz") {
		t.Errorf("response leaks api_key_ref: %s", raw)
	}
}

func TestAutoscanHandleUpsertSourceNotFoundReturns404(t *testing.T) {
	store := &fakeAutoscanStore{
		upsertSourceFn: func(string, autoscan.SourceUpdate) (*autoscan.Source, error) {
			return nil, errIntegrationNotFound
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{}, nil)

	req := httptest.NewRequest("PUT", "/api/v1/admin/autoscan/sources/missing",
		strings.NewReader(`{"enabled":true,"path_rewrites":[]}`))
	rec := httptest.NewRecorder()
	h.HandleUpsertSource(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleTriggerInvokesPollOnce(t *testing.T) {
	trig := &fakeAutoscanTriggerer{done: make(chan struct{}, 1)}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig, nil)

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
				{IntegrationID: "radarr-1", Name: "Radarr", BaseURL: "http://x:7878", APIKeyRef: "k"},
			}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{}, nil)

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
	if !body.Enabled || len(body.Sources) != 1 || body.Sources[0].IntegrationID != "radarr-1" {
		t.Errorf("status = %+v", body)
	}
}

func TestAutoscanHandleRewriteSuggestions(t *testing.T) {
	h := NewAutoscanHandler(&fakeAutoscanStore{}, &fakeAutoscanTriggerer{}, nil)

	req := httptest.NewRequest("GET", "/api/v1/admin/autoscan/sources/radarr-1/rewrite-suggestions", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "radarr-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()

	h.HandleRewriteSuggestions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body autoscan.RewriteSuggestions
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// errIntegrationNotFound is the repository's sentinel, so the handler's
// errors.Is-based 404 mapping is exercised.
var errIntegrationNotFound = autoscan.ErrIntegrationNotFound

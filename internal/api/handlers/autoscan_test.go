package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
}

func (f *fakeAutoscanTriggerer) PollOnce(context.Context) error {
	f.called = true
	return f.err
}

func TestAutoscanHandleGetSettingsReturnsJSON(t *testing.T) {
	store := &fakeAutoscanStore{
		getSettingsFn: func() (autoscan.Settings, error) {
			return autoscan.Settings{Enabled: true, PollIntervalMinutes: 5, DebounceSeconds: 30}, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

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
	h := NewAutoscanHandler(&fakeAutoscanStore{}, &fakeAutoscanTriggerer{})

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
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

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
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	req := httptest.NewRequest("PUT", "/api/v1/admin/autoscan/sources/missing",
		strings.NewReader(`{"enabled":true,"path_rewrites":[]}`))
	rec := httptest.NewRecorder()
	h.HandleUpsertSource(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAutoscanHandleTriggerInvokesPollOnce(t *testing.T) {
	trig := &fakeAutoscanTriggerer{}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, trig)

	rec := httptest.NewRecorder()
	h.HandleTrigger(rec, httptest.NewRequest("POST", "/api/v1/admin/autoscan/trigger", nil))

	if !trig.called {
		t.Fatal("PollOnce was not invoked")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
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
	if !body.Enabled || len(body.Sources) != 1 || body.Sources[0].IntegrationID != "radarr-1" {
		t.Errorf("status = %+v", body)
	}
}

// errIntegrationNotFound mirrors the repository's "integration not found"
// message so the handler's substring-based 404 mapping is exercised.
var errIntegrationNotFound = &autoscanTestError{"integration not found: missing"}

type autoscanTestError struct{ msg string }

func (e *autoscanTestError) Error() string { return e.msg }

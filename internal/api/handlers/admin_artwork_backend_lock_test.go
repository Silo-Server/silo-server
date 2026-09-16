package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

func withURLParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestArtworkBackendLocksOnceStorageIsRecorded(t *testing.T) {
	put := func(h *AdminHandler, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body)))
		return rec
	}
	putOne := func(h *AdminHandler, key, value string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(`{"value":"`+value+`"}`))
		h.HandleUpdateSetting(rec, withURLParam(req, "key", key))
		return rec
	}

	// Before any artwork is stored the backend is free to change.
	open := &fakeServerSettingsStore{values: map[string]string{}}
	if rec := put(&AdminHandler{SettingsRepo: open}, `{"values":{"artwork.storage_backend":"s3"}}`); rec.Code != http.StatusOK {
		t.Fatalf("unlocked batch write: %d %s", rec.Code, rec.Body.String())
	}

	locked := &fakeServerSettingsStore{values: map[string]string{
		"artwork.storage_backend":       "local",
		artworkstore.IdentitySettingKey: "local|/var/lib/silo/artwork",
	}}
	h := &AdminHandler{SettingsRepo: locked}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"batch":  put(h, `{"values":{"artwork.storage_backend":"s3","metadata.cache_images":"true"}}`),
		"single": putOne(h, "artwork.storage_backend", "s3"),
	} {
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "artwork_storage_locked") {
			t.Fatalf("%s write after storage recorded: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if locked.values["artwork.storage_backend"] != "local" || locked.values["metadata.cache_images"] != "" {
		t.Fatalf("locked write mutated settings: %#v", locked.values)
	}
	// Re-saving the current value is a no-op, not a conflict, so forms that
	// submit every key keep working.
	if rec := put(h, `{"values":{"artwork.storage_backend":"local","metadata.cache_images":"true"}}`); rec.Code != http.StatusOK {
		t.Fatalf("same-value write: %d %s", rec.Code, rec.Body.String())
	}
	// The recorded default is auto when the row was never written.
	defaulted := &fakeServerSettingsStore{values: map[string]string{artworkstore.IdentitySettingKey: "s3|https://s3.example|artwork|"}}
	if rec := putOne(&AdminHandler{SettingsRepo: defaulted}, "artwork.storage_backend", "auto"); rec.Code != http.StatusOK {
		t.Fatalf("auto on an unset locked row: %d %s", rec.Code, rec.Body.String())
	}
	if rec := putOne(&AdminHandler{SettingsRepo: defaulted}, "artwork.storage_backend", "local"); rec.Code != http.StatusConflict {
		t.Fatalf("local on a locked s3 store: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminServerStatusReportsArtworkStorageLock(t *testing.T) {
	read := func(h *AdminHandler) adminArtworkStorageStatus {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleGetServerStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/server/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body struct {
			ArtworkStorage adminArtworkStorageStatus `json:"artwork_storage"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.ArtworkStorage
	}
	fresh := &AdminHandler{RestartStatus: NewServerRestartStatusTracker(), ArtworkBackend: "local", SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}}}
	if got := read(fresh); got.Locked || got.Backend != "local" {
		t.Fatalf("fresh install: %+v", got)
	}
	recorded := &AdminHandler{RestartStatus: NewServerRestartStatusTracker(), ArtworkBackend: "s3", SettingsRepo: &fakeServerSettingsStore{values: map[string]string{artworkstore.IdentitySettingKey: "s3|https://s3.example|artwork|"}}}
	if got := read(recorded); !got.Locked || got.Backend != "s3" {
		t.Fatalf("recorded storage: %+v", got)
	}
}

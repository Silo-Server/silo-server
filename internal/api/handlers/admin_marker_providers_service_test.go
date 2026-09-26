package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminMarkerProviderPartialUpdatePersists(t *testing.T) {
	pool := catalogTransferPool(t)
	store := markers.NewProviderConfigStore(pool)
	provider := "test-catalog-provider-partial"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM marker_provider_config WHERE provider=$1`, provider)
	})
	initial := markers.ProviderConfig{Provider: provider, FetchEnabled: true, FetchPriority: 17, ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9}
	if err := store.Update(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	h := NewAdminMarkerProvidersHandler(nil, store, nil, nil)
	router := chi.NewRouter()
	router.Put("/providers/{provider}", h.HandleUpdateProvider)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("PUT", "/providers/"+provider, strings.NewReader(`{"fetch_enabled":false,"fetch_priority":0,"contribute_min_confidence":null}`)))
	if rec.Code != 200 {
		t.Fatalf("legacy update %d %s", rec.Code, rec.Body)
	}
	var out providerConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.FetchEnabled || out.FetchPriority != 0 || out.ContributeMinConfidence != 0.9 || !out.ContributeAutoLocal {
		t.Fatalf("partial: %+v", out)
	}
	_, err := h.UpdateMarkerProvider(t.Context(), provider, MarkerProviderUpdate{ContributeEnabled: new(false), ContributeMinConfidence: new(2.0)})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 {
		t.Fatalf("invalid: %v", err)
	}
	if err := store.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Get(provider)
	if !saved.ContributeEnabled || saved.ContributeMinConfidence != 0.9 {
		t.Fatalf("rejected edit persisted: %+v", saved)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/providers/"+provider, strings.NewReader(`{"contribute_min_confidence":2}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error":"bad_request"`) {
		t.Fatalf("legacy error: %d %s", rec.Code, rec.Body)
	}
}

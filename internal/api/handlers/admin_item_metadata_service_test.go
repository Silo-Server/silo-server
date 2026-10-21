package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/go-chi/chi/v5"
)

func TestItemMetadataTimezoneValidationPreservesV1(t *testing.T) {
	h := &AdminHandler{DetailSvc: &catalog.DetailService{}}
	router := chi.NewRouter()
	router.Patch("/items/{id}/metadata", h.HandleUpdateItemMetadata)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/items/item-1/metadata", strings.NewReader(`{"air_timezone":"Not/AZone"}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error":"bad_request"`) {
		t.Fatalf("timezone: %d %s", rec.Code, rec.Body)
	}
}

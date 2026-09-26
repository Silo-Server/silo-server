package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/go-chi/chi/v5"
)

type itemMetadataScope struct {
	mode adminjob.ItemRefreshMode
	err  error
}

func (s *itemMetadataScope) Resolve(ctx context.Context, id string) (*adminjob.ItemRefreshRequest, error) {
	return s.ResolveWithMode(ctx, id, adminjob.ItemRefreshModeQuick)
}
func (s *itemMetadataScope) ResolveWithMode(_ context.Context, id string, mode adminjob.ItemRefreshMode) (*adminjob.ItemRefreshRequest, error) {
	s.mode = mode
	return &adminjob.ItemRefreshRequest{RequestedContentID: id, RefreshContentID: id, Mode: mode}, s.err
}
func TestItemMetadataRefreshPersistsBeforeResponse(t *testing.T) {
	pool := catalogTransferPool(t)
	userID := catalogTransferAdmin(t, pool)
	repo := adminjob.NewRepository(pool)
	scope := &itemMetadataScope{}
	h := &AdminHandler{JobRepo: repo, ItemRefreshResolver: scope}
	job, err := h.CreateItemMetadataRefresh(t.Context(), "synthetic-item", adminjob.ItemRefreshModeComplete, userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=$1`, job.ID) })
	saved, err := repo.GetByID(t.Context(), job.ID)
	if err != nil || saved.Status != adminjob.StatusQueued || saved.CreatedByUserID != userID || scope.mode != adminjob.ItemRefreshModeComplete {
		t.Fatalf("persisted refresh: %#v %v", saved, err)
	}
	scope.err = &adminjob.ScopeResolutionError{StatusCode: 409, Message: "Cannot refresh this scope"}
	_, err = h.CreateItemMetadataRefresh(t.Context(), "synthetic-item", adminjob.ItemRefreshModeQuick, userID)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 409 || apiErr.Code != "conflict" {
		t.Fatalf("scope error: %v", err)
	}
}
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

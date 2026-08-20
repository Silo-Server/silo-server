package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollectionCapabilitiesAdvertiseSortSupport(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/collections/capabilities", nil)
	NewCollectionHandler(nil).HandleCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got collectionCapabilitiesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.CollectionDefaultSort || !got.CollectionSortPreferences || !got.EffectiveCollectionSort {
		t.Fatalf("sort capabilities not fully advertised: %+v", got)
	}
}

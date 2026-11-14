package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestCatalogAccessFiltersCarryExplicitDeviceIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v2/items/movie-1", nil)
	req.Header.Set(deviceIDHeader, "  apple-tv  ")

	if got := requestAccessFilter(req).DeviceID; got != "apple-tv" {
		t.Fatalf("requestAccessFilter().DeviceID = %q, want apple-tv", got)
	}
}

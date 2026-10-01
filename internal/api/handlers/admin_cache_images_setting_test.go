package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCacheImagesEnableDoesNotRequireS3(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	h := &AdminHandler{SettingsRepo: settings}
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"values":{"metadata.cache_images":"true"}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

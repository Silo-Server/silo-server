package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The retired "let profiles add rule rows" setting reads true on every v1
// route, and a v1 write is accepted without changing anything.
func TestSectionSettingsAlwaysAllowProfileRuleRows(t *testing.T) {
	h := &SectionSettingsHandler{}
	for name, serve := range map[string]func(http.ResponseWriter, *http.Request){
		"admin get":    h.HandleGet,
		"profile flag": h.HandleGetProfileFlag,
	} {
		rec := httptest.NewRecorder()
		serve(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"allow_profile_custom_sections":true}` {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	h.HandlePut(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"allow_profile_custom_sections":false}`)))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"allow_profile_custom_sections":true}` {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.HandlePut(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed put: %d %s", rec.Code, rec.Body.String())
	}
}

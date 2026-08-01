package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSystemCapabilitiesAdvertisesOnlyImplementedCapabilities(t *testing.T) {
	handler := NewSystemCapabilitiesHandler()
	recorder := httptest.NewRecorder()

	handler.HandleGet(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/system/capabilities", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var got systemCapabilitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := systemCapabilitiesResponse{
		APIVersion:   "2.2",
		Capabilities: []string{"branding.v1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

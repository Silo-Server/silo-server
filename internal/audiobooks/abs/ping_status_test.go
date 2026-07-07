package abs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestABSPing_HasSuccess guards the reachability probe: real ABS /ping returns
// {"success": true} and the ABS apps validate a server address by reading that
// field. Without it the app reports "unable to reach".
func TestABSPing_HasSuccess(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleABSPing(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["success"] != true {
		t.Errorf("/ping success = %v, want true", m["success"])
	}
}

// TestABSStatus_HasAuthMethods guards that /status carries authMethods (drives
// the login form) — real ABS Server.js /status shape.
func TestABSStatus_HasAuthMethods(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleABSStatus(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"app", "serverVersion", "isInit", "language", "authMethods", "authFormData"} {
		if _, ok := m[k]; !ok {
			t.Errorf("/status missing key %q", k)
		}
	}
	if methods, ok := m["authMethods"].([]any); !ok || len(methods) == 0 {
		t.Errorf("authMethods = %v, want non-empty array", m["authMethods"])
	}
}

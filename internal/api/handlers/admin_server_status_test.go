package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminServerStatusClearsAfterProcessRestart(t *testing.T) {
	t.Parallel()

	restartStatus := NewServerRestartStatusTracker()
	restartStatus.MarkRequired("settings")

	handler := &AdminHandler{RestartStatus: restartStatus}
	req := httptest.NewRequest(http.MethodGet, "/admin/server/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.RestartRequired {
		t.Fatal("RestartRequired = false, want true")
	}

	restartedHandler := &AdminHandler{RestartStatus: NewServerRestartStatusTracker()}
	rec = httptest.NewRecorder()
	restartedHandler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status after restart = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response after restart: %v", err)
	}
	if resp.RestartRequired {
		t.Fatal("RestartRequired = true after new process tracker, want false")
	}
}

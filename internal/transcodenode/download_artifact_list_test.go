package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
)

func TestHealthCarriesPathFreeArtifactUsage(t *testing.T) {
	s := newTestServer(t)
	s.artifactProber = downloadstorage.NewProber(time.Hour)
	if _, ok := s.artifactUsage(); ok {
		t.Fatal("no measurement has finished yet")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := s.artifactUsage(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("artifact measurement never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var health HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Artifacts == nil || health.Artifacts.Dir != "" {
		t.Fatalf("health artifacts = %+v, want a measurement without its path", health.Artifacts)
	}
}

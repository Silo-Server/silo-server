package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
)

func TestListDownloadArtifactsReportsFilesAndDirectory(t *testing.T) {
	s := newTestServer(t)
	if err := os.MkdirAll(s.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"art-1.mp4": 100, "art-2.mp4.part": 40, "art-1.mp4.receipt.json": 8} {
		if err := os.WriteFile(filepath.Join(s.artifactRoot, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	s.handleListDownloadArtifacts(rec, httptest.NewRequest(http.MethodGet, "/downloads/artifacts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var listing downloadstorage.Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Usage.Dir != s.artifactRoot || listing.Usage.Files != 1 || listing.Usage.Bytes != 100 ||
		listing.Usage.PartialFiles != 1 || listing.Usage.OtherBytes != 8 || len(listing.Files) != 3 {
		t.Fatalf("listing = %+v", listing)
	}
	if !listing.Usage.SharesScratch {
		t.Fatal("the default directory inside the transcode directory shares its filesystem")
	}
}

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

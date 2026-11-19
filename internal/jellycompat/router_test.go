package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestRouterRejectsArbitraryCompatWebDirectory(t *testing.T) {
	root := t.TempDir()
	arbitrary := t.TempDir()
	if err := os.WriteFile(filepath.Join(arbitrary, "index.html"), []byte("<!doctype html>secret"), 0o644); err != nil {
		t.Fatalf("write arbitrary index: %v", err)
	}
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         arbitrary,
		"jellyfin_compat.web_version":     "10.11.6",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg})

	req := httptest.NewRequest(http.MethodGet, "/web/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("arbitrary web_dir content was served: %q", rec.Body.String())
	}
}

package jellycompat

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestRouterCompressesJSONResponses(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg})

	req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}

	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read compressed body: %v", err)
	}
	if !strings.Contains(string(body), `"ProductName":"Jellyfin Server"`) {
		t.Fatalf("unexpected response body %q", string(body))
	}
}

func TestRouterServesCompatWebAssetsCreatedAfterStartup(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_version":     "10.11.6",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg})

	missingReq := httptest.NewRequest(http.MethodGet, "/web/", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d", missingRec.Code, http.StatusNotFound)
	}

	release := filepath.Join(root, "10.11.6")
	writeValidWebRelease(t, release, "10.11.6")
	if err := os.WriteFile(filepath.Join(release, "index.html"), []byte("<!doctype html>ready"), 0o644); err != nil {
		t.Fatalf("write ready index: %v", err)
	}
	if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/web/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "ready") {
		t.Fatalf("unexpected response body %q", rec.Body.String())
	}
}

func TestRouterReportsDisabledCompatWebAssets(t *testing.T) {
	for _, tt := range []struct {
		name     string
		settings map[string]string
		wantBody string
	}{
		{
			name: "proxy disabled",
			settings: map[string]string{
				"jellyfin_compat.enabled":     "false",
				"jellyfin_compat.web_enabled": "true",
			},
			wantBody: "Jellyfin Web UI is disabled because the Jellyfin compatibility proxy is disabled",
		},
		{
			name: "web disabled",
			settings: map[string]string{
				"jellyfin_compat.enabled":     "true",
				"jellyfin_compat.web_enabled": "false",
			},
			wantBody: "Jellyfin Web UI is disabled in Silo settings",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			release := filepath.Join(root, "10.11.6")
			writeValidWebRelease(t, release, "10.11.6")
			if err := os.WriteFile(filepath.Join(release, "index.html"), []byte("<!doctype html>ready"), 0o644); err != nil {
				t.Fatalf("write ready index: %v", err)
			}
			if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
				t.Fatalf("symlink current: %v", err)
			}
			settings := map[string]string{
				"jellyfin_compat.web_install_dir": root,
				"jellyfin_compat.web_version":     "10.11.6",
			}
			for key, value := range tt.settings {
				settings[key] = value
			}
			cfg, err := config.LoadFromDB(settings)
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
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("response body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), "assets are not installed") {
				t.Fatalf("response body = %q, should not report installed assets as missing", rec.Body.String())
			}
		})
	}
}

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

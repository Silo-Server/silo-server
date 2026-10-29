package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// A row admitted before the host refused "." and ".." identities never loads.
// It must not fail either list: the admin list keeps it so it can be
// uninstalled, and the user settings list leaves it out.
func TestPluginListsKeepWorkingWithAnInvalidIdentityRowDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	pluginID := fmt.Sprintf("test.legacy-identity-%d", time.Now().UnixNano())
	installDir := t.TempDir()
	manifest := fmt.Sprintf(`{"pluginId":%q,"version":"..","checksum":"0","siloApiVersion":"v1",`+
		`"supportedPlatforms":[{"os":"linux","arch":"amd64"}]}`, pluginID)
	if err := os.WriteFile(filepath.Join(installDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	var legacyID int
	err = pool.QueryRow(t.Context(),
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy)
		 VALUES ($1, '..', $2, true, 'manual')
		 RETURNING id`,
		pluginID, filepath.Join(installDir, "plugin")).Scan(&legacyID)
	if err != nil {
		t.Fatalf("seed legacy installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, legacyID)
	})

	repositories := plugins.NewRepositoryStore(pool)
	installations := plugins.NewInstallationStore(pool)
	configs := plugins.NewRuntimeConfigStore(pool)
	h := &PluginHandler{
		repositories:  repositories,
		installations: installations,
		configs:       configs,
		service:       plugins.NewService(repositories, installations, configs, nil, nil, nil),
	}

	rec := httptest.NewRecorder()
	h.HandleListInstallations(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/installations", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("installations list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var listed []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode installations: %v", err)
	}
	found := false
	for _, inst := range listed {
		found = found || inst.ID == legacyID
	}
	if !found {
		t.Errorf("installations list left out legacy installation %d", legacyID)
	}

	rec = httptest.NewRecorder()
	h.HandleListUserPluginSettings(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/plugins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("user settings list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

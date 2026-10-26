package plugins

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The reserved builtin plugin id must be rejected at install time so a
// malicious or accidental catalog entry cannot hijack the reserved row.
func TestReservedPluginIDRejected(t *testing.T) {
	if !isReservedPluginID("silo.builtin") {
		t.Error("silo.builtin must be reserved")
	}
	if isReservedPluginID("silo.tmdb") || isReservedPluginID("silo.tvdb") {
		t.Error("first-party plugin ids must stay installable")
	}
	if isReservedPluginID("community.example") {
		t.Error("community plugin ids must stay installable")
	}
}

// PreloadEnabled must skip builtin installations explicitly: the sentinel
// install path has no manifest on disk, and without the skip preload would
// fail (its caller is log.Fatalf) or at best emit spurious warnings.
func TestPreloadEnabledSkipsBuiltinInstallation(t *testing.T) {
	store := newFakeServiceInstallationStore(&Installation{
		ID:          1,
		PluginID:    "silo.builtin",
		Kind:        KindBuiltin,
		Enabled:     true,
		InstallPath: "/nonexistent/silo-builtin",
	})
	svc := &Service{installations: store}

	if err := svc.PreloadEnabled(context.Background()); err != nil {
		t.Fatalf("PreloadEnabled with builtin-only store = %v, want nil (builtin must be skipped)", err)
	}
}

func builtinGuardTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedResidentQueryInstallation(t *testing.T, pool *pgxpool.Pool, enabled bool) int {
	t.Helper()
	var id int
	pluginID := "silo.test.resident-query" + time.Now().UTC().Format("-20060102150405.000000000")
	err := pool.QueryRow(context.Background(),
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		 VALUES ($1, '0', '/nonexistent/resident-query-test', $2, 'manual', 'plugin')
		 RETURNING id`, pluginID, enabled).Scan(&id)
	if err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	return id
}

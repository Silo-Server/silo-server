package plugins

import (
	"os"
	"strings"
	"testing"
)

func TestApprovedCommunityMigrationKeepsFreshInstallsOffAndScopesLegacyMoves(t *testing.T) {
	data, err := os.ReadFile("../../migrations/sql/20260709191109_plugin_catalog_community_migration.sql")
	if err != nil {
		t.Fatalf("read approved community migration: %v", err)
	}
	sql := string(data)

	required := []string{
		"EXISTS (SELECT 1 FROM public.users)",
		"OR EXISTS (SELECT 1 FROM public.plugin_repositories)",
		"OR EXISTS (SELECT 1 FROM public.plugin_installations)",
		"CASE WHEN legacy_instance THEN 'true' ELSE 'false' END",
		"'plugins.include_approved_community_plugins'",
		"'approved-community'",
		"'approved_community'",
		"WHERE repository_id = official_repository_id",
		"plugin_id IN ('silo.requests.arr', 'silo.requests.seerr')",
		"available_version = NULL",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}

	legacyCheck := strings.Index(sql, "legacy_instance BOOLEAN :=")
	officialInsert := strings.Index(sql, "INSERT INTO public.plugin_repositories")
	if legacyCheck < 0 || officialInsert < 0 || legacyCheck > officialInsert {
		t.Fatal("legacy-instance state must be captured before managed repositories are inserted")
	}

	for _, dependentTable := range []string{
		"plugin_runtime_configs",
		"plugin_task_bindings",
		"plugin_auth_bindings",
		"plugin_capabilities",
	} {
		if strings.Contains(sql, "UPDATE public."+dependentTable) || strings.Contains(sql, "DELETE FROM public."+dependentTable) {
			t.Fatalf("migration must preserve installation-owned data in %s", dependentTable)
		}
	}
}

package migrations

import (
	"strings"
	"testing"
)

func TestACLFoundationResetsHistoricalMarkerEditDefault(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260629193000_acl_foundation.sql")
	if err != nil {
		t.Fatalf("read ACL foundation migration: %v", err)
	}
	migration := string(migrationBytes)

	requiredSnippets := []string{
		"ALTER TABLE public.users\n    ALTER COLUMN permissions SET DEFAULT '{}'::text[];",
		"UPDATE public.users\nSET permissions = array_remove(permissions, 'marker_edit'),\n    access_policy_revision = access_policy_revision + 1,\n    updated_at = now()\nWHERE COALESCE(role, 'user') <> 'admin'\n  AND 'marker_edit' = ANY (permissions);",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(migration, snippet) {
			t.Fatalf("ACL foundation migration missing marker_edit cleanup snippet:\n%s", snippet)
		}
	}

	cleanupIdx := strings.Index(migration, "UPDATE public.users\nSET permissions = array_remove(permissions, 'marker_edit')")
	userBackfillIdx := strings.Index(migration, "WITH standard_user_group AS")
	if cleanupIdx < 0 || userBackfillIdx < 0 {
		t.Fatalf("failed to locate cleanup/user backfill blocks")
	}
	if cleanupIdx > userBackfillIdx {
		t.Fatalf("marker_edit cleanup must run before user ACL backfill")
	}
}

func TestACLFoundationSeedsUserGroupsWithClearLabels(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260629193000_acl_foundation.sql")
	if err != nil {
		t.Fatalf("read ACL foundation migration: %v", err)
	}
	migration := string(migrationBytes)

	requiredSnippets := []string{
		"('standard_user', 'User', 'Normal media access.', true, false)",
		"('restricted_user', 'Restricted User', 'Media access with tighter limits.', true, false)",
		"SELECT id FROM public.acl_groups WHERE slug = 'standard_user'",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(migration, snippet) {
			t.Fatalf("ACL foundation migration missing user group naming snippet:\n%s", snippet)
		}
	}

	forbiddenSnippets := []string{
		"('viewer', 'Viewer'",
		"('restricted_viewer', 'Restricted Viewer'",
		"WHERE slug = 'viewer'",
	}
	for _, snippet := range forbiddenSnippets {
		if strings.Contains(migration, snippet) {
			t.Fatalf("ACL foundation migration still contains old viewer naming snippet:\n%s", snippet)
		}
	}
}

func TestACLFoundationAddsGroupPolicyObject(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260629193000_acl_foundation.sql")
	if err != nil {
		t.Fatalf("read ACL foundation migration: %v", err)
	}
	migration := string(migrationBytes)

	requiredSnippets := []string{
		"policy jsonb NOT NULL DEFAULT '{}'::jsonb",
		"CONSTRAINT acl_groups_policy_object_check CHECK (jsonb_typeof(policy) = 'object')",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(migration, snippet) {
			t.Fatalf("ACL foundation migration missing group policy snippet:\n%s", snippet)
		}
	}
}

func TestACLFoundationSeedsDefaultUserFacingGrants(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260629193000_acl_foundation.sql")
	if err != nil {
		t.Fatalf("read ACL foundation migration: %v", err)
	}
	migration := string(migrationBytes)

	requiredSnippets := []string{
		"WITH built_in_user_grants(subject_id, action, resource_type, name) AS",
		"('standard_user', 'playback.play', 'media_item', 'User playback')",
		"('standard_user', 'personal_lists.manage', 'media_item', 'User personal lists')",
		"('standard_user', 'requests.create', 'request', 'User request creation')",
		"('restricted_user', 'playback.play', 'media_item', 'Restricted user playback')",
		"grant_subjects(subject_type) AS",
		"('group'),",
		"existing.subject_type = grant_subjects.subject_type",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(migration, snippet) {
			t.Fatalf("ACL foundation migration missing default grant snippet:\n%s", snippet)
		}
	}
}

func TestACLRepairMigrationBackfillsMutableGroupSchema(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260630124500_repair_acl_builtin_defaults.sql")
	if err != nil {
		t.Fatalf("read ACL repair migration: %v", err)
	}
	migration := string(migrationBytes)

	requiredSnippets := []string{
		"ALTER TABLE public.acl_groups\n    ADD COLUMN IF NOT EXISTS policy jsonb;",
		"ALTER TABLE public.acl_groups\n    ALTER COLUMN policy SET DEFAULT '{}'::jsonb;",
		"ALTER TABLE public.acl_groups\n    ALTER COLUMN policy SET NOT NULL;",
		"ADD CONSTRAINT acl_groups_policy_object_check CHECK (jsonb_typeof(policy) = 'object')",
		"('standard_user', 'User', 'Normal media access.', '{}'::jsonb, true, false)",
		"('restricted_user', 'Restricted User', 'Media access with tighter limits.', '{}'::jsonb, true, false)",
		"grant_subjects(subject_type) AS",
		"('group'),",
		"('builtin_role')",
		"existing.subject_type = grant_subjects.subject_type",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(migration, snippet) {
			t.Fatalf("ACL repair migration missing schema/default snippet:\n%s", snippet)
		}
	}
}

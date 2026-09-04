package migrations

import (
	"strings"
	"testing"
)

func TestAlignUserCollectionSyncSchedulesDoesNotRewriteDataOnRollback(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260903104629_align_user_collection_sync_schedules.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	parts := strings.SplitN(string(migrationBytes), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("migration missing goose Down section")
	}
	if strings.Contains(parts[1], "UPDATE user_personal_collections") {
		t.Fatal("down migration must not rewrite schedules created or edited after the up migration")
	}
}

package migrations

import (
	"strings"
	"testing"
)

func TestEbookDetailsMigrationFilesExist(t *testing.T) {
	migration, err := FS.ReadFile("sql/184_ebook_details.sql")
	if err != nil {
		t.Fatalf("expected ebook migration: %v", err)
	}
	sql := string(migration)
	for _, marker := range []string{"-- +goose Up", "-- +goose Down", "CREATE TABLE IF NOT EXISTS public.ebook_details"} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("expected ebook migration to contain %q", marker)
		}
	}
}

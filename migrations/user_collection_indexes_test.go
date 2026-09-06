package migrations

import (
	"strings"
	"testing"
)

func TestUserCollectionIndexesAreConcurrentAndRetrySafe(t *testing.T) {
	const imageIndex = "idx_user_personal_collections_image_candidates"
	const keyIndex = "idx_user_personal_collections_compat_key"
	for _, tc := range []struct {
		file string
		up   string
		down string
	}{
		{"20260829190916_index_user_collection_image_candidates.sql", imageIndex, ""},
		{"20260831080145_index_user_collection_compat_keys.sql", keyIndex, imageIndex},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := FS.ReadFile("sql/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			migration := string(data)
			if !strings.Contains(migration, "-- +goose NO TRANSACTION") {
				t.Error("concurrent index operations must run outside a transaction")
			}
			for _, line := range strings.Split(migration, "\n") {
				line = strings.TrimSpace(line)
				for _, operation := range []string{"CREATE INDEX ", "DROP INDEX "} {
					if strings.HasPrefix(line, operation) && !strings.HasPrefix(line, operation+"CONCURRENTLY ") {
						t.Errorf("blocking index operation: %s", line)
					}
				}
			}
			up, down, ok := strings.Cut(migration, "-- +goose Down")
			if !ok {
				t.Fatal("missing Down migration")
			}
			for _, direction := range []struct {
				sql    string
				build  string
				remove string
			}{
				{normalizeSQL(up), tc.up, tc.down},
				{normalizeSQL(down), tc.down, tc.up},
			} {
				createAt := -1
				if direction.build != "" {
					// A failed concurrent build leaves an invalid index: IF NOT EXISTS
					// alone would skip it instead of rebuilding it on the next attempt.
					cleanupAt := strings.Index(direction.sql, "DROP INDEX CONCURRENTLY IF EXISTS "+direction.build+";")
					createAt = strings.Index(direction.sql, "CREATE INDEX CONCURRENTLY "+direction.build+" ON ")
					if cleanupAt < 0 || createAt < 0 || cleanupAt > createAt {
						t.Errorf("%s must have concurrent retry cleanup before its build", direction.build)
					}
				}
				if direction.remove != "" {
					dropAt := strings.Index(direction.sql, "DROP INDEX CONCURRENTLY IF EXISTS "+direction.remove+";")
					if dropAt < 0 || dropAt < createAt {
						t.Errorf("%s must be dropped concurrently after the replacement is built", direction.remove)
					}
				}
			}
			if tc.up == keyIndex {
				if !strings.Contains(up, "CREATE OR REPLACE FUNCTION jellycompat_user_collection_key(") {
					t.Error("function creation must tolerate retry after a partial Up")
				}
				if !strings.Contains(down, "DROP FUNCTION IF EXISTS jellycompat_user_collection_key(text);") {
					t.Error("function removal must tolerate retry after a partial Down")
				}
			}
		})
	}
}

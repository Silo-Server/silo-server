package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

// invalidIndexRebuildVersion sorts after every migration that built one of
// rebuiltConcurrentIndexes.
const invalidIndexRebuildVersion int64 = 20261008165913

// rebuiltConcurrentIndexes come from migrations that ran CREATE INDEX
// CONCURRENTLY IF NOT EXISTS without first removing an invalid copy. If a
// build was interrupted, the retry skipped the invalid leftover and goose
// recorded the migration as applied, so the planner has never used the index.
var rebuiltConcurrentIndexes = []struct{ name, definition string }{
	{"idx_media_files_folder_lower_file_path", "media_files (media_folder_id, lower(file_path))"},
	{"idx_media_files_folder_file_path_pattern", "media_files (media_folder_id, file_path text_pattern_ops)"},
	{"idx_media_items_content_type", "media_items USING btree (content_id, type)"},
	{"idx_uwp_profile_completed_cursor", "user_watch_progress USING btree (user_id, profile_id, updated_at DESC, media_item_id DESC) WHERE completed = TRUE"},
	{"idx_abs_playback_sessions_user_profile_started", "abs_playback_sessions USING btree (user_id, profile_id, started_at)"},
	{"idx_manga_enrichment_state_next_attempt", "manga_enrichment_state (next_attempt_at) WHERE next_attempt_at IS NOT NULL"},
}

// invalidIndexRebuildMigration rebuilds whichever of rebuiltConcurrentIndexes
// an interrupted build left invalid.
//
// A Go migration rather than SQL because valid indexes must be left alone,
// and that condition cannot live in SQL: DROP INDEX CONCURRENTLY refuses to run
// inside a DO block, and a plain DROP INDEX takes an ACCESS EXCLUSIVE lock
// that stalls every replica still serving the table. RunDB so each statement
// runs on its own.
func invalidIndexRebuildMigration() *goose.Migration {
	m := goose.NewGoMigration(
		invalidIndexRebuildVersion,
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error {
			return rebuildInvalidIndexes(ctx, db, "public")
		}},
		// The indexes belong to the migrations that first created them.
		&goose.GoFunc{RunDB: func(context.Context, *sql.DB) error { return nil }},
	)
	m.Source = fmt.Sprintf("%d_rebuild_invalid_concurrent_indexes.go", invalidIndexRebuildVersion)
	return m
}

// rebuildInvalidIndexes drops each invalid index in rebuiltConcurrentIndexes
// and builds it again, both concurrently, leaving valid indexes alone. A
// missing index is built too: the migrations that create these run first, so
// one is missing only when an earlier attempt of this migration stopped
// between the drop and the build.
func rebuildInvalidIndexes(ctx context.Context, db *sql.DB, schema string) error {
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	for _, index := range rebuiltConcurrentIndexes {
		quotedName := pgx.Identifier{index.name}.Sanitize()
		qualified := quotedSchema + "." + quotedName
		var valid bool
		if err := db.QueryRowContext(ctx,
			`SELECT coalesce((SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)), false)`,
			qualified,
		).Scan(&valid); err != nil {
			return fmt.Errorf("checking index %s: %w", index.name, err)
		}
		if valid {
			continue
		}
		if _, err := db.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+qualified); err != nil {
			return fmt.Errorf("dropping invalid index %s: %w", index.name, err)
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s.%s",
			quotedName, quotedSchema, index.definition)); err != nil {
			return fmt.Errorf("rebuilding index %s: %w", index.name, err)
		}
	}
	return nil
}

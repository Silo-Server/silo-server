package backup

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// msgInterrupted is the reason a job queued or running when the backup was
// taken ends with in the restored database.
const msgInterrupted = "interrupted by restore"

// interruptedWork stops, in a restored database, the work that was queued
// or running when the backup was taken: none of it runs by itself after a
// full restore. Each statement ends one table's unfinished rows as their
// own failure or cancellation would; finished rows stay as history.
var interruptedWork = []struct{ table, sql string }{
	// Admin jobs of every kind: backups, restores, imports, storage
	// transitions, scans, exports.
	{"admin_jobs", `UPDATE admin_jobs SET status = 'cancelled', error_message = $1, completed_at = now(), updated_at = now()
		WHERE status IN ('queued', 'running')`},
	{"history_import_runs", `UPDATE history_import_runs SET status = 'cancelled', error_message = $1, completed_at = now()
		WHERE status IN ('queued', 'running')`},
	{"scan_runs", `UPDATE scan_runs SET status = 'cancelled', error_message = $1, completed_at = now(), updated_at = now()
		WHERE status IN ('accepted', 'running')`},
	{"library_collection_sync_runs", `UPDATE library_collection_sync_runs SET status = 'failed', message = $1, completed_at = now()
		WHERE status = 'running'`},
	{"library_collections", `UPDATE library_collections SET last_sync_status = 'failed' WHERE last_sync_status = 'running' AND $1 <> ''`},
	// Deliveries to notification destinations: a pending or retrying one
	// would send the backup's notifications again.
	{"push_delivery_attempts", `UPDATE push_delivery_attempts SET outcome = 'failed', failure_message = $1, updated_at = now()
		WHERE outcome IN ('pending', 'retrying')`},
	{"webhook_delivery_attempts", `UPDATE webhook_delivery_attempts SET outcome = 'failed', failure_message = $1
		WHERE outcome IN ('pending', 'retrying')`},
	{"web_push_delivery_attempts", `UPDATE web_push_delivery_attempts SET outcome = 'failed', failure_message = $1
		WHERE outcome IN ('pending', 'retrying')`},
	// A storage transition's progress belongs to the transition the live
	// server has, if any (InterruptWork).
	{"storage_transition_checkpoints", `DELETE FROM storage_transition_checkpoints WHERE $1 <> ''`},
	{"storage_transition_cursors", `DELETE FROM storage_transition_cursors WHERE $1 <> ''`},
	// Stores kept outside Postgres whose bookkeeping is in it. The search
	// engine's index holds the live catalog, not the restored one, and its
	// change events are not in the dump: without its state the indexer
	// rebuilds the index from the restored catalog when Silo starts.
	{"catalog_search_index_state", `DELETE FROM catalog_search_index_state WHERE $1 <> ''`},
	// Recommendations are rebuilt from the restored history and ratings,
	// with the live server's embedding settings.
	{"user_taste_profiles", `UPDATE user_taste_profiles SET stale_at = now() WHERE stale_at IS NULL AND $1 <> ''`},
}

// InterruptWork ends, in the restored database dbURL, the work interrupted
// by the restore (interruptedWork), and gives it the live server's
// (liveURL's) storage transition target, or none when liveURL is "" or the
// live server has none: a transition canceled since the backup stays
// canceled, and a staged target is never revived. It returns what it
// changed, by table. A table or column an older backup does not have is
// skipped.
func (l PGLoader) InterruptWork(ctx context.Context, dbURL, liveURL string) ([]string, error) {
	var target *string
	if liveURL != "" {
		live, err := readSettingsAt(ctx, liveURL, []string{config.StorageTransitionTargetKey})
		if err != nil {
			return nil, fmt.Errorf("reading the live storage transition: %w", err)
		}
		if v, ok := live[config.StorageTransitionTargetKey]; ok {
			target = &v
		}
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var changed []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, w := range interruptedWork {
			n, err := skipMissing(ctx, tx, w.sql, msgInterrupted)
			if err != nil {
				return fmt.Errorf("%s: %w", w.table, err)
			}
			if n > 0 {
				changed = append(changed, fmt.Sprintf("%s (%d)", w.table, n))
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, config.StorageTransitionTargetKey); err != nil {
			return err
		}
		if target != nil {
			_, err = tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, config.StorageTransitionTargetKey, *target)
		}
		return err
	})
	return changed, err
}

// skipMissing runs sql in a savepoint and reports the rows it changed; a
// table or column the restored schema does not have yet changes nothing.
func skipMissing(ctx context.Context, tx pgx.Tx, sql string, args ...any) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		tag, err := sp.Exec(ctx, sql, args...)
		n = tag.RowsAffected()
		return err
	})
	if missingInSchema(err) {
		return 0, nil
	}
	return n, err
}

// interruptWork ends the restored database's interrupted work and says
// what it ended.
func interruptWork(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, out io.Writer) error {
	changed, err := loader.InterruptWork(ctx, dbURL, liveURL)
	if err != nil {
		return fmt.Errorf("ending the backup's unfinished work: %w", err)
	}
	if len(changed) > 0 {
		_, _ = fmt.Fprintf(out, "Ended the work that was queued or running when the backup was taken, and marked what is rebuilt from the restored data (%s): "+
			"none of it runs by itself after the restore, and the search index and recommendations are rebuilt when Silo starts.\n",
			strings.Join(changed, ", "))
	}
	return nil
}

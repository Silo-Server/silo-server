package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SmartCountRefresher struct {
	Pool     *pgxpool.Pool
	Executor *QueryExecutor
}

func (r *SmartCountRefresher) RefreshAll(ctx context.Context) (refreshed int, errs int) {
	return r.refreshMatching(ctx, nil)
}

// RefreshLibraries refreshes the smart collections whose rules draw from any
// of libraryIDs, including those that draw from every library. A scan that
// adds or removes a library's titles calls it through SmartCountScheduler.
func (r *SmartCountRefresher) RefreshLibraries(ctx context.Context, libraryIDs []int) (refreshed int, errs int) {
	if len(libraryIDs) == 0 {
		return 0, 0
	}
	return r.refreshMatching(ctx, libraryIDs)
}

// refreshMatching refreshes every smart collection when libraryIDs is nil,
// and otherwise only those drawing from one of them.
func (r *SmartCountRefresher) refreshMatching(ctx context.Context, libraryIDs []int) (refreshed int, errs int) {
	if r == nil || r.Pool == nil || r.Executor == nil {
		return 0, 0
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id, query_definition
		FROM library_collections
		WHERE collection_type = 'smart'
	`)
	if err != nil {
		slog.WarnContext(ctx, "smart-count refresher: list query failed", "component", "catalog", "error", err)
		return 0, 1
	}
	defer rows.Close()

	type entry struct {
		id  string
		def []byte
	}
	var batch []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.def); err != nil {
			slog.WarnContext(ctx, "smart-count refresher: scan failed", "component", "catalog", "error", err)
			return refreshed, errs + 1
		}
		if libraryIDs != nil && !smartDefinitionDrawsFrom(e.def, libraryIDs) {
			continue
		}
		batch = append(batch, e)
	}
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "smart-count refresher: row iter failed", "component", "catalog", "error", err)
		return refreshed, errs + 1
	}

	for _, e := range batch {
		if err := r.refreshOne(ctx, e.id, e.def); err != nil {
			slog.WarnContext(ctx, "smart-count refresher: collection failed", "component", "catalog", "collection_id", e.id, "error", err)
			errs++
			continue
		}
		refreshed++
	}
	return refreshed, errs
}

func (r *SmartCountRefresher) RefreshOne(ctx context.Context, collectionID string) error {
	if r == nil || r.Pool == nil || r.Executor == nil {
		return nil
	}
	var def []byte
	var typ string
	if err := r.Pool.QueryRow(ctx, `
		SELECT collection_type, query_definition
		FROM library_collections
		WHERE id = $1
	`, collectionID).Scan(&typ, &def); err != nil {
		return fmt.Errorf("loading collection: %w", err)
	}
	if typ != "smart" {
		return nil
	}
	return r.refreshOne(ctx, collectionID, def)
}

func (r *SmartCountRefresher) refreshOne(ctx context.Context, id string, defJSON []byte) error {
	var def QueryDefinition
	if err := json.Unmarshal(defJSON, &def); err != nil {
		return fmt.Errorf("unmarshal query_definition: %w", err)
	}
	def = ApplySmartCollectionItemLimit(def.Normalize())
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, total, err := r.Executor.Preview(queryCtx, def, AccessFilter{}, 1)
	if err != nil {
		return fmt.Errorf("preview: %w", err)
	}
	if _, err := r.Pool.Exec(ctx, `
		UPDATE library_collections
		SET item_count_cached = $1, item_count_cached_at = NOW()
		WHERE id = $2
	`, total, id); err != nil {
		return fmt.Errorf("update cache: %w", err)
	}
	return nil
}

// smartDefinitionDrawsFrom reports whether a stored smart rule counts titles
// from one of libraryIDs. A rule naming no library draws from all of them. A
// rule that does not parse is left to RefreshAll, which logs it.
func smartDefinitionDrawsFrom(defJSON []byte, libraryIDs []int) bool {
	var def QueryDefinition
	if err := json.Unmarshal(defJSON, &def); err != nil {
		return false
	}
	def = def.Normalize()
	if len(def.LibraryIDs) == 0 {
		return true
	}
	for _, id := range def.LibraryIDs {
		if slices.Contains(libraryIDs, id) {
			return true
		}
	}
	return false
}

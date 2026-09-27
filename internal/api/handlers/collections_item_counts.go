package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// personalCollectionCountSource is the part of a personal collection its item
// count depends on.
type personalCollectionCountSource struct {
	ID              string
	CollectionType  string
	QueryDefinition string
}

// visiblePersonalCollectionCounts answers how many members of each personal
// collection the viewer can see, the total the collection's items page
// returns: visible membership rows for hand-picked and imported collections,
// and the viewer's matches for smart ones. The stored item_count column is
// written only by import syncs, so it cannot answer this. A collection whose
// count cannot be read is absent from the result and keeps its stored count.
//
// Membership is read from the Postgres user store; callers skip stores that
// keep collections elsewhere.
func visiblePersonalCollectionCounts(ctx context.Context, executor *catalog.QueryExecutor, userID int, sources []personalCollectionCountSource, filter catalog.AccessFilter) map[string]int {
	counts := make(map[string]int, len(sources))
	if executor == nil || executor.Pool == nil || len(sources) == 0 {
		return counts
	}
	var memberIDs []string
	for _, s := range sources {
		if !catalog.IsLiveQueryType(s.CollectionType) {
			memberIDs = append(memberIDs, s.ID)
			continue
		}
		n, err := countSmartPersonalCollection(ctx, executor, s.QueryDefinition, filter)
		if err != nil {
			slog.WarnContext(ctx, "counting smart personal collection failed", "component", "collections", "collection_id", s.ID, "error", err)
			continue
		}
		counts[s.ID] = n
	}
	if len(memberIDs) == 0 {
		return counts
	}
	visible, err := catalog.NewItemRepository(executor.Pool).CountVisiblePersonalCollectionMembers(ctx, userID, memberIDs, filter)
	if err != nil {
		slog.WarnContext(ctx, "counting personal collection members failed", "component", "collections", "error", err)
		return counts
	}
	for _, id := range memberIDs {
		counts[id] = visible[id]
	}
	return counts
}

// countSmartPersonalCollection prepares the definition exactly as the smart
// items page does, so the count and the pages agree.
func countSmartPersonalCollection(ctx context.Context, executor *catalog.QueryExecutor, raw string, filter catalog.AccessFilter) (int, error) {
	var def catalog.QueryDefinition
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		return 0, err
	}
	def = catalog.ApplySmartCollectionItemLimit(def.Normalize())
	if err := def.ValidateWithOptions(true, true); err != nil {
		return 0, err
	}
	return executor.Count(ctx, def, filter)
}

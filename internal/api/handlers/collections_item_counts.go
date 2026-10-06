package handlers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// visiblePersonalCollectionCounts answers how many items each personal
// collection shows the viewer, the total of its catalog view: its visible
// members, or its smart definition's matches, narrowed by its display filter.
// The stored item_count column is written only by import syncs, so it cannot
// answer this. A collection whose count cannot be read is absent from the
// result and keeps its stored count.
//
// Membership is read from the Postgres user store; callers skip stores that
// keep collections elsewhere.
func visiblePersonalCollectionCounts(ctx context.Context, executor *catalog.QueryExecutor, userID int, collections []catalog.PersonalCollectionDefinition, filter catalog.AccessFilter) map[string]int {
	counts := make(map[string]int, len(collections))
	if executor == nil || executor.Pool == nil || len(collections) == 0 {
		return counts
	}
	// Hand-picked and imported collections without a display filter share one
	// grouped count; dynamic definitions are deduplicated and batched.
	var memberIDs []string
	var dynamic []catalog.PersonalCollectionDefinition
	for _, c := range collections {
		if !catalog.IsLiveQueryType(c.CollectionType) && strings.TrimSpace(c.DisplayQueryDefinition) == "" {
			memberIDs = append(memberIDs, c.ID)
			continue
		}
		dynamic = append(dynamic, c)
	}
	if len(dynamic) > 0 {
		var err error
		counts, err = catalog.CountPersonalCollections(ctx, executor.Pool, userID, dynamic, filter)
		if err != nil {
			slog.WarnContext(ctx, "counting personal collections failed", "component", "collections", "error", err)
		}
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

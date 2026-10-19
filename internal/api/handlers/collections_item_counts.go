package handlers

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// visiblePersonalCollectionCounts answers how many items each personal
// collection shows the viewer, the total of its catalog view: its visible
// members, or its smart definition's matches, narrowed by its display filter.
// The stored item_count column is written only by import syncs, so it cannot
// answer this. A collection whose count cannot be read is absent from the
// result; callers must mark it unavailable rather than use a stored count.
//
// Membership is read from the Postgres user store; callers skip stores that
// keep collections elsewhere.
func visiblePersonalCollectionCounts(ctx context.Context, executor *catalog.QueryExecutor, userID int, collections []catalog.PersonalCollectionDefinition, filter catalog.AccessFilter) (map[string]int, error) {
	if executor == nil || executor.Pool == nil || len(collections) == 0 {
		return nil, apiError(http.StatusServiceUnavailable, "collection_count_unavailable", "Collection counts are unavailable")
	}
	counts, err := catalog.CountPersonalCollections(ctx, executor.Pool, userID, collections, filter)
	if err != nil {
		slog.WarnContext(ctx, "counting personal collections failed", "component", "collections", "error", err)
	}
	return counts, err
}

const (
	// CollectionCountAvailable means ItemCount is a current evaluated count.
	CollectionCountAvailable = "available"
	// CollectionCountUnavailable means clients must ignore ItemCount.
	CollectionCountUnavailable = "unavailable"
)

// Frozen v1 cannot represent an unavailable count in its response body.
func collectionCountReadError(err error) error {
	if errors.Is(err, catalog.ErrCollectionCountBudget) || errors.Is(err, catalog.ErrQueryComplexity) || errors.Is(err, context.DeadlineExceeded) {
		return apiError(http.StatusServiceUnavailable, "collection_count_unavailable", "Collection count budget exhausted; retry or repair the collection definition")
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to count collection items")
}

// ownedCollectionDefinition is a personal collection's count definition and
// the profile that owns it.
type ownedCollectionDefinition struct {
	catalog.PersonalCollectionDefinition
	CreatorProfileID string
	// WantsCollage marks a collection with no uploaded or imported poster,
	// which shows a collage of its titles instead.
	WantsCollage bool
}

// ownedCollectionReads is what ownerScopedCollectionReads answers.
type ownedCollectionReads struct {
	counts map[string]int
	// posters holds the collage each WantsCollage collection shows the
	// viewer, when one is stored.
	posters map[string]catalog.CollectionPoster
	// unavailable lists the collections whose owner could not be resolved.
	unavailable map[string]bool
	countError  error
}

// ownerScopedCollectionReads counts each collection as viewerProfileID sees
// it, and with collages set finds the collage it shows that viewer: under the
// viewer's filter, limited further to its owner's access when another profile
// owns it (catalog.PersonalCollectionFilter). Collections are read per owner,
// so each owner is resolved once per call. A collection whose owner cannot be
// resolved has no count or collage and is listed in unavailable; callers drop
// it rather than read it under the viewer's access alone.
func ownerScopedCollectionReads(ctx context.Context, executor *catalog.QueryExecutor, owners catalog.PersonalCollectionAccess, collages *catalog.PersonalCollectionCollages, userID int, viewerProfileID string, collections []ownedCollectionDefinition, viewer catalog.AccessFilter) ownedCollectionReads {
	out := ownedCollectionReads{counts: make(map[string]int, len(collections)), posters: map[string]catalog.CollectionPoster{}, unavailable: make(map[string]bool)}
	if executor == nil || executor.Pool == nil {
		out.countError = apiError(http.StatusServiceUnavailable, "collection_count_unavailable", "Collection counts are unavailable")
		return out
	}
	countCtx, cancel := catalog.WithCollectionCountBudget(ctx)
	defer cancel()
	byOwner := make(map[string][]ownedCollectionDefinition)
	var order []string
	for _, c := range collections {
		if _, ok := byOwner[c.CreatorProfileID]; !ok {
			order = append(order, c.CreatorProfileID)
		}
		byOwner[c.CreatorProfileID] = append(byOwner[c.CreatorProfileID], c)
	}
	for _, owner := range order {
		owned := byOwner[owner]
		filter, err := catalog.PersonalCollectionFilter(ctx, owners, viewer, userID, viewerProfileID, owner)
		if err != nil {
			slog.WarnContext(ctx, "resolving personal collection owner access failed", "component", "collections", "owner_profile_id", owner, "error", err)
			for _, c := range owned {
				out.unavailable[c.ID] = true
			}
			continue
		}
		definitions := make([]catalog.PersonalCollectionDefinition, 0, len(owned))
		var collaged []catalog.PersonalCollectionDefinition
		for _, c := range owned {
			definitions = append(definitions, c.PersonalCollectionDefinition)
			if c.WantsCollage {
				collaged = append(collaged, c.PersonalCollectionDefinition)
			}
		}
		counts, countErr := visiblePersonalCollectionCounts(countCtx, executor, userID, definitions, filter)
		maps.Copy(out.counts, counts)
		out.countError = errors.Join(out.countError, countErr)
		maps.Copy(out.posters, collages.Posters(ctx, userID, collaged, filter))
	}
	return out
}

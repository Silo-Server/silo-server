package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
)

// recommendationStaleTimeout bounds marking recommendations stale after a
// merge or split, which runs after the request's own context may have ended.
const recommendationStaleTimeout = 10 * time.Second

// RecommendationStaler marks stale the recommendations of every profile whose
// user state points at the given items, so the stale sweep rebuilds them.
// *recommendations.Repo implements it.
type RecommendationStaler interface {
	MarkProfilesStaleForItems(ctx context.Context, itemIDs []string) (int64, error)
}

// SetRecommendationStaler installs what marks recommendations stale after a
// merge, split or duplicate repair moves user state onto an item. Without it
// those profiles wait for the nightly recommendation jobs.
func (s *MetadataService) SetRecommendationStaler(staler RecommendationStaler) {
	if s != nil {
		s.recsStaler = staler
	}
}

// MarkRecommendationsStale marks stale the profiles whose user state now
// points at itemIDs. Call it after the transaction that moved the state
// committed. A failure only delays the rebuild to the nightly jobs, so it is
// logged rather than returned.
func MarkRecommendationsStale(ctx context.Context, staler RecommendationStaler, itemIDs ...string) {
	if staler == nil || len(itemIDs) == 0 {
		return
	}
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recommendationStaleTimeout)
	defer cancel()
	if _, err := staler.MarkProfilesStaleForItems(markCtx, itemIDs); err != nil {
		slog.WarnContext(ctx, "marking recommendations stale after moving user state failed", "component", "metadata", "item_ids", itemIDs, "error", err)
	}
}

// MovedStateTargets lists the items a reattribution moved user state onto:
// the target item and the target episodes of each moved episode. It is empty
// when nothing moved.
func MovedStateTargets(report *reattribute.Report, targetID string, episodePairs []reattribute.IDPair) []string {
	if !report.MovedUserState() {
		return nil
	}
	ids := make([]string, 0, 1+len(episodePairs))
	ids = append(ids, targetID)
	for _, pair := range episodePairs {
		ids = append(ids, pair.To)
	}
	return ids
}

// MergeItems merges the item at fromContentID into toContentID: files, library
// memberships, provider ids, and all per-user state (via the shared
// reattribution engine inside rebindItemToExistingItem) move to the target and
// the source row is deleted. Both items must exist and share a type. This is
// the admin-facing repair for a wrong split — two catalog items that are one
// logical title.
func (s *MetadataService) MergeItems(ctx context.Context, fromContentID, toContentID string) error {
	if s == nil {
		return fmt.Errorf("metadata service unavailable")
	}
	fromContentID = strings.TrimSpace(fromContentID)
	toContentID = strings.TrimSpace(toContentID)
	if fromContentID == "" || toContentID == "" {
		return fmt.Errorf("merge requires source and target content ids")
	}
	if fromContentID == toContentID {
		return fmt.Errorf("merge source and target are the same item")
	}

	from, err := s.itemRepo.GetByID(ctx, fromContentID)
	if err != nil {
		return fmt.Errorf("loading merge source %s: %w", fromContentID, err)
	}
	to, err := s.itemRepo.GetByID(ctx, toContentID)
	if err != nil {
		return fmt.Errorf("loading merge target %s: %w", toContentID, err)
	}
	if from.Type != to.Type {
		return fmt.Errorf("cannot merge %s item into %s item", from.Type, to.Type)
	}

	// allowMatchedSource: an operator merging duplicates typically merges two
	// fully matched items; the source row must still be deletable.
	return s.rebindItemToExistingItem(ctx, fromContentID, toContentID, true)
}

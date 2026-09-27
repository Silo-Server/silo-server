package catalog

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// personalMembershipSourceWhere limits a query to one personal collection's
// hand-picked or imported members in the Postgres user store. Its SourceArgs
// are the account ID and the collection ID.
const personalMembershipSourceWhere = "EXISTS (SELECT 1 FROM user_personal_collection_items cursor_membership WHERE cursor_membership.user_id=$1 AND cursor_membership.collection_id=$2 AND cursor_membership.sub_item_id='' AND cursor_membership.media_item_id=mi.content_id)"

// PersonalCollectionDefinition is the part of a personal collection that
// decides which items it shows.
type PersonalCollectionDefinition struct {
	ID                     string
	CollectionType         string
	QueryDefinition        string
	DisplayQueryDefinition string
}

// CountPersonalCollection answers how many items a personal collection shows
// the viewer: the total of its catalog view (resolveUserCollectionCursor),
// built from the same predicates. That is its visible members, or its smart
// definition's matches, narrowed by the collection's display filter. It reads
// membership from the Postgres user store. CountVisiblePersonalCollectionMembers
// answers the same for many collections at once when none has a display filter
// or a smart definition.
func CountPersonalCollection(ctx context.Context, pool *pgxpool.Pool, userID int, c PersonalCollectionDefinition, access AccessFilter) (int, error) {
	display := QueryDefinition{}.Normalize()
	canonical, err := NormalizeDisplayQueryFragment([]byte(c.DisplayQueryDefinition))
	if err != nil {
		return 0, err
	}
	if canonical != "" {
		if err := json.Unmarshal([]byte(canonical), &display); err != nil {
			return 0, err
		}
	}
	executor := &QueryExecutor{Pool: pool, BaseRelationSQL: catalogBaseRelationForScope("")}
	if !IsLiveQueryType(c.CollectionType) {
		executor.SourceWhere = personalMembershipSourceWhere
		executor.SourceArgs = []any{userID, c.ID}
		return executor.Count(ctx, display, access)
	}
	def, err := parseCatalogCollectionQueryDefinition([]byte(c.QueryDefinition))
	if err != nil {
		return 0, err
	}
	def = ApplySmartCollectionItemLimit(def)
	base := &QueryExecutor{Pool: pool, Scope: def.MediaScope, BaseRelationSQL: catalogBaseRelationForScope(def.MediaScope)}
	if canonical == "" {
		return base.Count(ctx, def, access)
	}
	predicate, args, err := collectionDefinitionPredicate(base, def, access)
	if err != nil {
		return 0, err
	}
	executor.Scope = def.MediaScope
	executor.SourceWhere = predicate
	executor.SourceArgs = args
	display.MediaScope = def.MediaScope
	return executor.Count(ctx, display, access)
}

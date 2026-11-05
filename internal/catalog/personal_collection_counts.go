package catalog

import (
	"encoding/json"
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

// personalCollectionCountQuery uses the catalog view's predicates for visible
// members or smart matches, including its display filter and item limit.
func personalCollectionCountQuery(userID int, c PersonalCollectionDefinition, access AccessFilter) (string, []any, error) {
	source := ""
	if IsLiveQueryType(c.CollectionType) {
		source = c.QueryDefinition
	}
	if err := ValidatePersonalCollectionQueryBudget(source, c.DisplayQueryDefinition); err != nil {
		return "", nil, err
	}
	display := QueryDefinition{}.Normalize()
	canonical, err := NormalizeDisplayQueryFragment([]byte(c.DisplayQueryDefinition))
	if err != nil {
		return "", nil, err
	}
	if canonical != "" {
		if err := json.Unmarshal([]byte(canonical), &display); err != nil {
			return "", nil, err
		}
	}
	executor := &QueryExecutor{BaseRelationSQL: catalogBaseRelationForScope("")}
	if !IsLiveQueryType(c.CollectionType) {
		executor.SourceWhere = personalMembershipSourceWhere
		executor.SourceArgs = []any{userID, c.ID}
		return executor.buildCountQuery(display, access)
	}
	def, err := parseCatalogCollectionQueryDefinition([]byte(c.QueryDefinition))
	if err != nil {
		return "", nil, err
	}
	def = ApplySmartCollectionItemLimit(def)
	base := &QueryExecutor{Scope: def.MediaScope, BaseRelationSQL: catalogBaseRelationForScope(def.MediaScope)}
	if canonical == "" {
		return base.buildCountQuery(def, access)
	}
	predicate, args, err := collectionDefinitionPredicate(base, def, access)
	if err != nil {
		return "", nil, err
	}
	executor.Scope = def.MediaScope
	executor.SourceWhere = predicate
	executor.SourceArgs = args
	display.MediaScope = def.MediaScope
	return executor.buildCountQuery(display, access)
}

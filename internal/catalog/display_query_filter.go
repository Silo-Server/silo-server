package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	displayFilterFieldType    = "type"
	displayFilterFieldWatched = "watched"
)

// displayFilterFields are the QueryRule fields a personal-collection display
// filter may use. Display filters are intentionally a small, fixed vocabulary
// (the "watched" and "movies/series" controls); richer rules belong in a
// collection's own query_definition, not its display overlay.
var displayFilterFields = map[string]bool{
	displayFilterFieldType:    true,
	displayFilterFieldWatched: true,
}

// NormalizeDisplayQueryFragment validates a personal-collection display filter
// and returns its canonical JSON form, or ("", nil) when the fragment is absent
// or carries no rules.
//
// A display filter is a FILTER-ONLY QueryDefinition fragment: it may set only
// match + groups, with rules drawn from displayFilterFields. Fields that control
// execution rather than filtering — library_ids, media_scope, sort, and limit —
// are rejected, because a stored display filter must never reorder or truncate a
// collection's match set (the exact-collection reader relies on retrieving the
// full match set in source order before paginating).
func NormalizeDisplayQueryFragment(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil
	}

	// A JSON null unmarshals to a zero QueryDefinition with no groups, which the
	// empty-groups guard below already collapses to "".
	var def QueryDefinition
	if err := json.Unmarshal(trimmed, &def); err != nil {
		return "", fmt.Errorf("display_query_definition must be a query definition: %w", err)
	}

	if len(def.LibraryIDs) > 0 || strings.TrimSpace(def.MediaScope) != "" || def.Limit != nil || def.Sort != (QuerySort{}) {
		return "", fmt.Errorf("display_query_definition is filter-only and must not set library_ids, media_scope, sort, or limit")
	}

	cleanGroups := make([]QueryGroup, 0, len(def.Groups))
	for _, group := range def.Groups {
		rules := make([]QueryRule, 0, len(group.Rules))
		for _, rule := range group.Rules {
			field := strings.ToLower(strings.TrimSpace(rule.Field))
			if !displayFilterFields[field] {
				return "", fmt.Errorf("display_query_definition does not support field %q", rule.Field)
			}
			rule.Field = field
			if field == displayFilterFieldType {
				if value, ok := rule.Value.(string); ok {
					rule.Value = strings.ToLower(strings.TrimSpace(value))
				}
			}
			if err := validateDisplayFilterValue(field, rule); err != nil {
				return "", err
			}
			if field == displayFilterFieldType && rule.Value == "all" {
				continue
			}
			rules = append(rules, rule)
		}
		if len(rules) == 0 {
			continue
		}
		cleanGroups = append(cleanGroups, QueryGroup{Match: normalizeMatch(group.Match), Rules: rules})
	}
	if len(cleanGroups) == 0 {
		return "", nil
	}

	// Validate field/op structure against the catalog vocabulary, then emit a
	// minimal {match, groups} object so stored display filters never carry the
	// default sort/limit a full QueryDefinition would serialize.
	canonical := QueryDefinition{Match: normalizeMatch(def.Match), Groups: cleanGroups}
	if err := canonical.Validate(); err != nil {
		return "", fmt.Errorf("invalid display_query_definition: %w", err)
	}

	encoded, err := json.Marshal(struct {
		Match  string       `json:"match"`
		Groups []QueryGroup `json:"groups"`
	}{Match: canonical.Match, Groups: cleanGroups})
	if err != nil {
		return "", fmt.Errorf("encoding display_query_definition: %w", err)
	}
	return string(encoded), nil
}

func validateDisplayFilterValue(field string, rule QueryRule) error {
	switch field {
	case displayFilterFieldWatched:
		if _, ok := rule.Value.(bool); !ok {
			return fmt.Errorf("display_query_definition watched value must be a boolean")
		}
	case displayFilterFieldType:
		value, ok := rule.Value.(string)
		if !ok || value == "" {
			return fmt.Errorf("display_query_definition type value must be a non-empty string")
		}
		switch value {
		case "all", "movie", "series":
		default:
			return fmt.Errorf("display_query_definition type value must be one of %q, %q, or %q", "all", "movie", "series")
		}
	}
	return nil
}

// FilterCollectionItemsByDisplayQuery applies a personal collection's stored
// display filter (a filter-only QueryDefinition fragment) to items already
// loaded in source order, returning the matching items in their original order.
//
// Filtering runs through the catalog query executor constrained to the member
// IDs, so watched/type semantics match catalog browsing and item user-data
// exactly — no separate watched evaluator. The fragment is forced filter-only
// before execution (no scope/sort/library/limit) so it can never reorder or
// truncate the match set; source order and pagination are the caller's job. An
// empty fragment returns items unchanged.
func FilterCollectionItemsByDisplayQuery(
	ctx context.Context,
	pool *pgxpool.Pool,
	items []*models.MediaItem,
	displayQueryDefinition string,
	access AccessFilter,
) ([]*models.MediaItem, error) {
	trimmed := strings.TrimSpace(displayQueryDefinition)
	if trimmed == "" || len(items) == 0 {
		return items, nil
	}

	var def QueryDefinition
	if err := json.Unmarshal([]byte(trimmed), &def); err != nil {
		return nil, fmt.Errorf("parsing collection display_query_definition: %w", err)
	}
	// Filter-only: a stored fragment must never scope, reorder, or truncate the
	// collection's match set.
	def.MediaScope = ""
	def.LibraryIDs = nil
	def.Sort = QuerySort{}
	def.Limit = nil
	if len(def.Groups) == 0 {
		return items, nil
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && strings.TrimSpace(item.ContentID) != "" {
			ids = append(ids, item.ContentID)
		}
	}
	if len(ids) == 0 {
		return items, nil
	}

	queryAccess := access
	queryAccess.AllowedContentIDs = ids
	executor := &QueryExecutor{Pool: pool}
	matched, _, err := executor.Preview(ctx, def, queryAccess, len(ids))
	if err != nil {
		return nil, fmt.Errorf("evaluating collection display_query_definition: %w", err)
	}

	matchedIDs := make(map[string]struct{}, len(matched))
	for _, item := range matched {
		if item != nil {
			matchedIDs[item.ContentID] = struct{}{}
		}
	}

	filtered := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, ok := matchedIDs[item.ContentID]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

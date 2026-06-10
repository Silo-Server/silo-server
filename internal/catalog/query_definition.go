package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const defaultSortField = "added_at"

type queryFieldDef struct {
	columnSQL    string
	isArray      bool
	executable   bool
	personalized bool
	validOps     map[string]bool
}

type querySortDef struct {
	columnSQL     string
	defaultOrder  string
	nullsLast     bool
	personalized  bool
	titleSortOnly bool
}

var queryFieldAliases = map[string]string{
	"rating": "rating_imdb",
}

var querySortAliases = map[string]string{
	"sort_title":     "title",
	"recently_added": "added_at",
	"rating":         "rating_imdb",
}

var queryFieldDefs = map[string]queryFieldDef{
	"type":              {columnSQL: "type", executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"genre":             {columnSQL: "genres", isArray: true, executable: true, validOps: map[string]bool{"is": true, "is_not": true, "contains": true}},
	"year":              {columnSQL: "year", executable: true, validOps: map[string]bool{"is": true, "is_not": true, "gt": true, "gte": true, "lt": true, "lte": true, "between": true}},
	"rating_imdb":       {columnSQL: "rating_imdb", executable: true, validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true}},
	"studio":            {columnSQL: "studios", isArray: true, executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"network":           {columnSQL: "networks", isArray: true, executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"country":           {columnSQL: "countries", isArray: true, executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"original_language": {columnSQL: "original_language", executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"content_rating":    {columnSQL: "content_rating", executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"added_at":          {columnSQL: "created_at", executable: true, validOps: map[string]bool{"gt": true, "lt": true, "between": true, "in_last": true}},
	"release_date":      {columnSQL: "COALESCE(%s.release_date::text, NULLIF(BTRIM(%s.first_air_date), ''))", executable: true, validOps: map[string]bool{"gt": true, "lt": true, "between": true, "in_last": true}},
	"status":            {columnSQL: "status", executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"actor":             {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"director":          {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"writer":            {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"producer":          {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"author":            {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"narrator":          {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"series":            {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"watched":           {executable: true, personalized: true, validOps: map[string]bool{"is": true}},
	"favorited":         {executable: true, personalized: true, validOps: map[string]bool{"is": true}},
	"in_watchlist":      {executable: true, personalized: true, validOps: map[string]bool{"is": true}},
	"in_progress":       {executable: true, personalized: true, validOps: map[string]bool{"is": true}},
	"last_watched":      {executable: true, personalized: true, validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true, "in_last": true}},
	"resolution":        {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"hdr":               {executable: true, validOps: map[string]bool{"is": true}},
	"dolby_vision":      {executable: true, validOps: map[string]bool{"is": true}},
	"bitrate":           {executable: true, validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true}},
	"audio_language":    {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	"subtitle_language": {
		executable: true,
		validOps:   map[string]bool{"is": true, "is_not": true},
	},
}

var querySortDefs = map[string]querySortDef{
	"title":              {columnSQL: "LOWER(COALESCE(NULLIF(BTRIM(%s.sort_title), ''), %s.title))", defaultOrder: "asc", titleSortOnly: true},
	"added_at":           {defaultOrder: "desc"},
	"release_date":       {columnSQL: "COALESCE(%s.release_date::text, NULLIF(BTRIM(%s.first_air_date), ''))", defaultOrder: "desc", nullsLast: true},
	"last_air_date":      {columnSQL: "last_air_date", defaultOrder: "desc", nullsLast: true},
	"year":               {columnSQL: "year", defaultOrder: "desc"},
	"content_rating":     {defaultOrder: "asc"},
	"runtime":            {columnSQL: "runtime", defaultOrder: "desc", nullsLast: true},
	"rating_imdb":        {columnSQL: "rating_imdb", defaultOrder: "desc", nullsLast: true},
	"rating_tmdb":        {columnSQL: "rating_tmdb", defaultOrder: "desc", nullsLast: true},
	"rating_rt_critic":   {columnSQL: "rating_rt_critic", defaultOrder: "desc", nullsLast: true},
	"rating_rt_audience": {columnSQL: "rating_rt_audience", defaultOrder: "desc", nullsLast: true},
	"resolution":         {defaultOrder: "desc", nullsLast: true},
	"bitrate":            {defaultOrder: "desc", nullsLast: true},
	"progress":           {defaultOrder: "desc", nullsLast: true, personalized: true},
	"date_viewed":        {defaultOrder: "desc", nullsLast: true, personalized: true},
	"plays":              {defaultOrder: "desc", nullsLast: true, personalized: true},
	// Audiobook-native sorts. nullsLast so items without an author /
	// narrator / series association still appear (sorted to the end).
	"author":   {defaultOrder: "asc", nullsLast: true},
	"narrator": {defaultOrder: "asc", nullsLast: true},
	"series":   {defaultOrder: "asc", nullsLast: true},
}

type QueryDefinition struct {
	LibraryIDs []int        `json:"library_ids,omitempty"`
	MediaScope string       `json:"media_scope,omitempty"`
	Match      string       `json:"match"`
	Groups     []QueryGroup `json:"groups"`
	Sort       QuerySort    `json:"sort"`
	Limit      *int         `json:"limit,omitempty"`
}

// MediaScopeVideo is the group scope covering all video-side item types. It
// is accepted anywhere a single-type media scope is and expands to the
// underlying media_items.type values via MediaScopeItemTypes.
const MediaScopeVideo = "video"

// IsValidMediaScope reports whether scope (already normalized to lowercase)
// is an accepted media_scope value. Empty means unscoped and is valid.
func IsValidMediaScope(scope string) bool {
	switch scope {
	case "", "movie", "series", "episode", "audiobook", "ebook", "manga", MediaScopeVideo:
		return true
	default:
		return false
	}
}

// MediaScopeItemTypes expands a media scope into the media_items.type values
// it covers. Single-type scopes map to themselves; the empty scope returns
// nil (unscoped).
func MediaScopeItemTypes(scope string) []string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "":
		return nil
	case MediaScopeVideo:
		return []string{"movie", "series"}
	default:
		return []string{scope}
	}
}

// MediaScopeMatchesItemType reports whether an item type falls inside scope.
// An empty scope matches every type.
func MediaScopeMatchesItemType(scope, itemType string) bool {
	types := MediaScopeItemTypes(scope)
	if len(types) == 0 {
		return true
	}
	itemType = strings.ToLower(strings.TrimSpace(itemType))
	for _, t := range types {
		if t == itemType {
			return true
		}
	}
	return false
}

const (
	DefaultSmartCollectionItemLimit = 100
	MaxSmartCollectionItemLimit     = 500
)

func ApplySmartCollectionItemLimit(def QueryDefinition) QueryDefinition {
	limit := DefaultSmartCollectionItemLimit
	if def.Limit != nil && *def.Limit > 0 {
		limit = *def.Limit
	}
	if limit > MaxSmartCollectionItemLimit {
		limit = MaxSmartCollectionItemLimit
	}
	def.Limit = &limit
	return def
}

type QueryGroup struct {
	Match string      `json:"match"`
	Rules []QueryRule `json:"rules"`
}

type QueryRule struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type QuerySort struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

func (q QueryDefinition) Normalize() QueryDefinition {
	normalized := q
	normalized.MediaScope = strings.ToLower(strings.TrimSpace(normalized.MediaScope))
	normalized.Match = normalizeMatch(normalized.Match)
	normalized.LibraryIDs = normalizeLibraryIDs(normalized.LibraryIDs)
	normalized.Sort = NormalizeQuerySort(normalized.Sort)

	if normalized.Groups == nil {
		normalized.Groups = []QueryGroup{}
	}
	for i := range normalized.Groups {
		normalized.Groups[i].Match = normalizeMatch(normalized.Groups[i].Match)
		if normalized.Groups[i].Rules == nil {
			normalized.Groups[i].Rules = []QueryRule{}
		}
		for j := range normalized.Groups[i].Rules {
			field := strings.ToLower(strings.TrimSpace(normalized.Groups[i].Rules[j].Field))
			if canonical, ok := queryFieldAliases[field]; ok {
				field = canonical
			}
			normalized.Groups[i].Rules[j].Field = field
			normalized.Groups[i].Rules[j].Op = strings.ToLower(strings.TrimSpace(normalized.Groups[i].Rules[j].Op))
		}
	}

	return normalized
}

func (q QueryDefinition) Validate() error {
	return q.ValidateWithOptions(true, true)
}

func (q QueryDefinition) ValidateWithSortScope(allowPersonalizedSorts bool) error {
	return q.ValidateWithOptions(allowPersonalizedSorts, true)
}

func (q QueryDefinition) ValidateWithOptions(allowPersonalizedSorts, allowPersonalizedFields bool) error {
	normalized := q.Normalize()

	for _, id := range normalized.LibraryIDs {
		if id <= 0 {
			return fmt.Errorf("library_ids must contain positive library IDs")
		}
	}

	if !IsValidMediaScope(normalized.MediaScope) {
		return fmt.Errorf("media_scope must be 'movie', 'series', 'episode', 'audiobook', 'ebook', 'manga', or 'video'")
	}

	if normalized.Match != "all" && normalized.Match != "any" {
		return fmt.Errorf("match must be 'all' or 'any'")
	}

	for i, group := range normalized.Groups {
		if group.Match != "all" && group.Match != "any" {
			return fmt.Errorf("groups[%d].match must be 'all' or 'any'", i)
		}
		for j, rule := range group.Rules {
			def, ok := queryFieldDefs[rule.Field]
			if !ok {
				return fmt.Errorf("groups[%d].rules[%d].field %q is not supported", i, j, rule.Field)
			}
			if def.personalized && !allowPersonalizedFields {
				return fmt.Errorf("groups[%d].rules[%d].field %q requires profile scope", i, j, rule.Field)
			}
			if normalized.MediaScope == "ebook" && rule.Field == "narrator" {
				return fmt.Errorf("groups[%d].rules[%d].field %q is not supported for ebook media_scope", i, j, rule.Field)
			}
			if !def.validOps[rule.Op] {
				return fmt.Errorf("groups[%d].rules[%d].op %q is not supported for field %q", i, j, rule.Op, rule.Field)
			}
		}
	}

	if normalized.Sort.Field != "" {
		def, ok := querySortDefs[normalized.Sort.Field]
		if !ok {
			return fmt.Errorf("sort.field %q is not supported", normalized.Sort.Field)
		}
		if def.personalized && !allowPersonalizedSorts {
			return fmt.Errorf("sort.field %q requires profile scope", normalized.Sort.Field)
		}
		if normalized.MediaScope == "ebook" && normalized.Sort.Field == "narrator" {
			return fmt.Errorf("sort.field %q is not supported for ebook media_scope", normalized.Sort.Field)
		}
	}
	if normalized.Sort.Order != "" && normalized.Sort.Order != "asc" && normalized.Sort.Order != "desc" {
		return fmt.Errorf("sort.order must be 'asc' or 'desc'")
	}

	if normalized.Limit != nil && *normalized.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}

	return nil
}

func NormalizeQuerySort(sortConfig QuerySort) QuerySort {
	normalized := QuerySort{
		Field: strings.ToLower(strings.TrimSpace(sortConfig.Field)),
		Order: strings.ToLower(strings.TrimSpace(sortConfig.Order)),
	}
	if canonical, ok := querySortAliases[normalized.Field]; ok {
		normalized.Field = canonical
	}

	if normalized.Field == "" {
		normalized.Field = defaultSortField
	}
	if normalized.Order == "" {
		if def, ok := querySortDefs[normalized.Field]; ok {
			normalized.Order = def.defaultOrder
		}
	}

	return normalized
}

func QuerySortFieldSet(allowPersonalizedSorts bool) map[string]bool {
	fields := make(map[string]bool, len(querySortDefs))
	for field, def := range querySortDefs {
		if def.personalized && !allowPersonalizedSorts {
			continue
		}
		fields[field] = true
	}
	return fields
}

func QuerySortRequiresProfile(field string) bool {
	def, ok := querySortDefs[strings.ToLower(strings.TrimSpace(field))]
	return ok && def.personalized
}

func QueryFieldRequiresProfile(field string) bool {
	def, ok := queryFieldDefs[strings.ToLower(strings.TrimSpace(field))]
	return ok && def.personalized
}

func NormalizeLegacySectionFilter(config json.RawMessage) (QueryDefinition, error) {
	var legacy struct {
		FilterType       string       `json:"filter_type"`
		FilterLibraryID  *int         `json:"filter_library_id"`
		FilterLibraryIDs []int        `json:"filter_library_ids"`
		Match            string       `json:"match"`
		Groups           []QueryGroup `json:"groups"`
		Sort             string       `json:"sort"`
		Order            string       `json:"order"`
		Limit            *int         `json:"limit"`
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &legacy); err != nil {
			return QueryDefinition{}, err
		}
	}

	libraryIDs := append([]int{}, legacy.FilterLibraryIDs...)
	if legacy.FilterLibraryID != nil {
		libraryIDs = append(libraryIDs, *legacy.FilterLibraryID)
	}

	def := QueryDefinition{
		LibraryIDs: libraryIDs,
		MediaScope: legacy.FilterType,
		Match:      legacy.Match,
		Groups:     legacy.Groups,
		Sort: QuerySort{
			Field: legacy.Sort,
			Order: legacy.Order,
		},
		Limit: legacy.Limit,
	}.Normalize()

	if err := def.Validate(); err != nil {
		return QueryDefinition{}, err
	}

	return def, nil
}

func normalizeMatch(match string) string {
	normalized := strings.ToLower(strings.TrimSpace(match))
	if normalized == "" {
		return "all"
	}
	return normalized
}

func normalizeLibraryIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}

	seen := make(map[int]struct{}, len(ids))
	normalized := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			normalized = append(normalized, id)
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Ints(normalized)
	return normalized
}

func queryColumnSQL(alias string, raw string) string {
	switch strings.Count(raw, "%s") {
	case 0:
		return alias + "." + raw
	case 1:
		return fmt.Sprintf(raw, alias)
	default:
		return fmt.Sprintf(raw, alias, alias)
	}
}

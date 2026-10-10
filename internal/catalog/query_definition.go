package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
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

	querySortTitle: {columnSQL: querySortTitle, executable: true, validOps: textRuleOps},
	// decade takes the decade's first year (1990 matches 1990 to 1999).
	ruleFieldDecade: {executable: true, validOps: map[string]bool{"is": true, "is_not": true}},
	// runtime holds minutes; 0 means unknown and matches no bound.
	querySortRuntime:          {columnSQL: "NULLIF(%s.runtime, 0)", executable: true, validOps: numberRuleOps},
	querySortRatingTMDb:       {columnSQL: querySortRatingTMDb, executable: true, validOps: numberRuleOps},
	querySortRatingRTCritic:   {columnSQL: querySortRatingRTCritic, executable: true, validOps: numberRuleOps},
	querySortRatingRTAudience: {columnSQL: querySortRatingRTAudience, executable: true, validOps: numberRuleOps},
	// A show's newest episode: when its file arrived, and when it aired.
	querySortLatestEpisodeAdded: {columnSQL: latestEpisodeAddedColumn, executable: true, validOps: dateRuleOps},
	querySortLastAirDate:        {columnSQL: lastAirDateColumn, executable: true, validOps: dateRuleOps},
}

// Rule operators and fields named in several places, and the columns the
// newest-episode fields read.
const (
	ruleOpInLast             = "in_last"
	ruleOpNotInLast          = "not_in_last"
	ruleOpNotContains        = "not_contains"
	ruleOpBeginsWith         = "begins_with"
	ruleOpEndsWith           = "ends_with"
	ruleFieldDecade          = "decade"
	latestEpisodeAddedColumn = "latest_episode_added_at"
	lastAirDateColumn        = "last_air_date_at"
)

// textRuleOps compare a free-text field ignoring case, whole (is, is_not) or
// in part (contains, not_contains, begins_with, ends_with).
var textRuleOps = map[string]bool{"is": true, "is_not": true, "contains": true, ruleOpNotContains: true, ruleOpBeginsWith: true, ruleOpEndsWith: true}

// numberRuleOps compare a number with a bound or an inclusive range.
var numberRuleOps = map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true}

// dateRuleOps compare a date with absolute bounds or a span ending now ("30d").
var dateRuleOps = map[string]bool{"gt": true, "lt": true, "between": true, ruleOpInLast: true}

// allows reports whether the field takes op. not_in_last is the complement of
// in_last among titles that have the date (a title without one matches
// neither), so every field that takes in_last takes it too.
func (d queryFieldDef) allows(op string) bool {
	return d.validOps[op] || (op == ruleOpNotInLast && d.validOps[ruleOpInLast])
}

// v2RuleFields are the rule fields added after /api/v1 froze. A v1 request
// keeps the frozen vocabulary: these fields and not_in_last are refused there,
// with the messages v1 gave them before they existed.
var v2RuleFields = map[string]bool{
	querySortTitle:              true,
	ruleFieldDecade:             true,
	querySortRuntime:            true,
	querySortRatingTMDb:         true,
	querySortRatingRTCritic:     true,
	querySortRatingRTAudience:   true,
	querySortLatestEpisodeAdded: true,
	querySortLastAirDate:        true,
}

// ValidateV1Rules refuses a rule the frozen /api/v1 vocabulary lacks, with
// the message QueryDefinition validation gave it there. input is anything
// QueryBuilder.Build takes.
func ValidateV1Rules(input any) error {
	def, err := normalizeBuilderInput(input)
	if err != nil {
		return err
	}
	for i, group := range def.Groups {
		for j, rule := range group.Rules {
			if v2RuleFields[rule.Field] {
				return fmt.Errorf("groups[%d].rules[%d].field %q is not supported", i, j, rule.Field)
			}
			if rule.Op == ruleOpNotInLast {
				return fmt.Errorf("groups[%d].rules[%d].op %q is not supported for field %q", i, j, rule.Op, rule.Field)
			}
		}
	}
	return nil
}

// validateRuleValue rejects a value the SQL builder cannot use, so a malformed
// span or a new field's malformed value fails validation instead of the
// query that runs it.
func validateRuleValue(rule QueryRule) error {
	if rule.Op == ruleOpInLast || rule.Op == ruleOpNotInLast {
		_, err := ruleInterval(rule)
		return err
	}
	switch rule.Field {
	case querySortTitle:
		// An empty value would make contains, begins_with and ends_with match
		// every title.
		if text, ok := rule.Value.(string); !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("title requires a non-empty string")
		}
	case ruleFieldDecade:
		if _, ok := decadeStart(rule.Value); !ok {
			return fmt.Errorf("decade requires a year such as 1990")
		}
	case "rating_imdb", querySortRuntime, querySortRatingTMDb, querySortRatingRTCritic, querySortRatingRTAudience:
		if rule.Op == "between" {
			if _, ok := catalogFloatRange(rule.Value); !ok {
				return fmt.Errorf("%s between requires [min, max] numbers", rule.Field)
			}
		} else if _, ok := catalogFloat(rule.Value); !ok {
			return fmt.Errorf("%s requires a number", rule.Field)
		}
	case querySortLatestEpisodeAdded, querySortLastAirDate:
		if rule.Op == "between" {
			bounds, ok := catalogStringRange(rule.Value)
			if !ok || !isRuleDate(bounds[0]) || !isRuleDate(bounds[1]) {
				return fmt.Errorf("%s between requires [from, to] dates such as 2024-01-31", rule.Field)
			}
		} else if bound, ok := rule.Value.(string); !ok || !isRuleDate(bound) {
			return fmt.Errorf("%s requires a date such as 2024-01-31", rule.Field)
		}
	}
	return nil
}

// isRuleDate reports whether a date rule's bound is a calendar date
// (2024-01-31) or an RFC 3339 time, the forms the editor and clients send.
// PostgreSQL has no year 0, so the year must be 1 or later.
func isRuleDate(value string) bool {
	value = strings.TrimSpace(value)
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	return err == nil && parsed.Year() >= 1
}

var querySortDefs = map[string]querySortDef{
	"title":         {columnSQL: "LOWER(COALESCE(NULLIF(BTRIM(%s.sort_title), ''), %s.title))", defaultOrder: "asc", titleSortOnly: true},
	"added_at":      {defaultOrder: "desc"},
	"release_date":  {columnSQL: "COALESCE(%s.release_date::text, NULLIF(BTRIM(%s.first_air_date), ''))", defaultOrder: "desc", nullsLast: true},
	"last_air_date": {columnSQL: "last_air_date", defaultOrder: "desc", nullsLast: true},
	// Latest Episodes (issue #202): series ordered by when their newest
	// episode FILE arrived (denormalized media_items.latest_episode_added_at,
	// maintained on the episode_libraries insert paths). Distinct from
	// added_at (when the series itself was first added) and last_air_date
	// (when the newest episode aired).
	"latest_episode_added": {columnSQL: "latest_episode_added_at", defaultOrder: "desc", nullsLast: true},
	"year":                 {columnSQL: "year", defaultOrder: "desc"},
	"content_rating":       {defaultOrder: "asc"},
	"runtime":              {columnSQL: "runtime", defaultOrder: "desc", nullsLast: true},
	"rating_imdb":          {columnSQL: "rating_imdb", defaultOrder: "desc", nullsLast: true},
	"rating_tmdb":          {columnSQL: "rating_tmdb", defaultOrder: "desc", nullsLast: true},
	"rating_rt_critic":     {columnSQL: "rating_rt_critic", defaultOrder: "desc", nullsLast: true},
	"rating_rt_audience":   {columnSQL: "rating_rt_audience", defaultOrder: "desc", nullsLast: true},
	"resolution":           {defaultOrder: "desc", nullsLast: true},
	"bitrate":              {defaultOrder: "desc", nullsLast: true},
	"progress":             {defaultOrder: "desc", nullsLast: true, personalized: true},
	"date_viewed":          {defaultOrder: "desc", nullsLast: true, personalized: true},
	"plays":                {defaultOrder: "desc", nullsLast: true, personalized: true},
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

// MediaScopeVideoWithEpisodes is a search-only scope: a text search over
// movies, series, and TV episodes. Episodes live in the episode catalog rather
// than media_items, so only the text-search path can mix them with media
// items. Stored definitions (smart collections, sections) reject it through
// IsValidMediaScope; a request parser that accepts it records it as
// CatalogRequest.SearchMediaScope and narrows every non-search read of the
// request to MediaScopeVideo.
const MediaScopeVideoWithEpisodes = "video_with_episodes"

// IsValidMediaScope reports whether scope (already normalized to lowercase)
// is an accepted media_scope value. Empty means unscoped and is valid. The
// search-only MediaScopeVideoWithEpisodes is not a definition scope.
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
	case MediaScopeVideoWithEpisodes:
		return []string{"movie", "series", "episode"}
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

// DefaultSmartCollectionItemLimit applies when a smart collection sets no
// explicit limit. Smart collections are deliberately uncapped: an unset
// limit resolves the full result set. The large sentinel keeps the int
// plumbing (SQL LIMIT) intact everywhere a concrete value is required.
const DefaultSmartCollectionItemLimit = 10_000_000

// ApplySmartCollectionItemLimit fills in the default item limit when a smart
// collection's query definition does not set one. An explicit positive limit
// is honored as-is: membership size is an admin decision, and every read path
// that serves collection items either paginates in SQL or scales with the
// actual collection size rather than this value.
func ApplySmartCollectionItemLimit(def QueryDefinition) QueryDefinition {
	limit := DefaultSmartCollectionItemLimit
	if def.Limit != nil && *def.Limit > 0 {
		limit = *def.Limit
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

// NormalizePersonalListSort validates the optional sort configured on a
// watchlist/favorites section and applies the field's default order when none
// is given. Returns false when the field is empty or unsupported, meaning the
// list's stored order should be kept. "added_at" means the date the item was
// added to the list (not to the library) and is resolved from the list
// entries rather than the query executor.
func NormalizePersonalListSort(field, order string) (QuerySort, bool) {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "title", "release_date", "year", "rating_imdb", "added_at":
	default:
		return QuerySort{}, false
	}
	order = strings.ToLower(strings.TrimSpace(order))
	if order != "asc" && order != "desc" {
		order = querySortDefs[field].defaultOrder
	}
	return QuerySort{Field: field, Order: order}, true
}

// NormalizePersonalSourceSort validates a saved Watchlist/Favorites browse
// preference against the same non-personalized vocabulary accepted by the
// live personal-source catalog request.
func NormalizePersonalSourceSort(field, order string) (QuerySort, bool) {
	field = strings.ToLower(strings.TrimSpace(field))
	// Relevance needs a live search query and random is not stable, so neither
	// is meaningful as a persisted preference if they join the general set.
	if field == "relevance" || field == "random" || !QuerySortFieldSet(false)[field] {
		return QuerySort{}, false
	}
	order = strings.ToLower(strings.TrimSpace(order))
	if order == "" {
		order = querySortDefs[field].defaultOrder
	} else if order != "asc" && order != "desc" {
		return QuerySort{}, false
	}
	return QuerySort{Field: field, Order: order}, true
}

// NormalizeCollectionSort validates a collection's default sort (its stored
// sort_config) or a viewer's saved override, applying the field's default order
// when none is given. Returns false when the field or a supplied order is
// unsupported. An empty field means the collection's own source order should
// be kept.
//
// allowPersonalized gates the per-profile sort fields (progress, date viewed,
// plays). They are legitimate for personal collections, which always resolve
// with the owning profile in scope, but not for library collections: those
// resolve with user scope stripped (see stripCatalogUserScope), so the query
// builder's ensureUserScope would reject the sort at browse time.
func NormalizeCollectionSort(field, order string, allowPersonalized bool) (QuerySort, bool) {
	field = strings.ToLower(strings.TrimSpace(field))
	def, ok := querySortDefs[field]
	if !ok {
		return QuerySort{}, false
	}
	if def.personalized && !allowPersonalized {
		return QuerySort{}, false
	}
	order = strings.ToLower(strings.TrimSpace(order))
	if order == "" {
		order = def.defaultOrder
	} else if order != "asc" && order != "desc" {
		return QuerySort{}, false
	}
	return QuerySort{Field: field, Order: order}, true
}

// collectionSortConfig is the shape a collection's sort_config carries when it
// pins a default order. Everything else the column may hold (legacy `{}`, the
// unimplemented manual-pin mode) leaves the collection on source order.
type collectionSortConfig struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

// ParseCollectionDefaultSort reads the default sort a collection's creator
// configured. An absent, empty, or unrecognized sort_config yields false,
// meaning source order.
func ParseCollectionDefaultSort(raw []byte, allowPersonalized bool) (QuerySort, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return QuerySort{}, false
	}
	var cfg collectionSortConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return QuerySort{}, false
	}
	return NormalizeCollectionSort(cfg.Field, cfg.Order, allowPersonalized)
}

// EncodeCollectionDefaultSort renders a validated default sort back into the
// sort_config JSON shape. An empty field encodes as `{}` — source order.
func EncodeCollectionDefaultSort(qs QuerySort, ok bool) (string, error) {
	if !ok || strings.TrimSpace(qs.Field) == "" {
		return "{}", nil
	}
	encoded, err := json.Marshal(collectionSortConfig(qs))
	if err != nil {
		return "", fmt.Errorf("encoding collection sort_config: %w", err)
	}
	return string(encoded), nil
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
			if !def.allows(rule.Op) {
				return fmt.Errorf("groups[%d].rules[%d].op %q is not supported for field %q", i, j, rule.Op, rule.Field)
			}
			if err := validateRuleValue(rule); err != nil {
				return fmt.Errorf("groups[%d].rules[%d]: %w", i, j, err)
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

// IsPersonalized reports whether the definition references any per-profile
// state: a rule field (in any group) or the sort field that injects a
// profile-scoped EXISTS(... user_id/profile_id ...) predicate — the
// personalized fields (watched, favorited, in_watchlist, in_progress,
// last_watched) and sorts (progress, date_viewed, plays). A personalized
// definition's membership/ordering differs per profile, so it must never be
// served from a user-agnostic shared cache keyed only by access scope.
func (q QueryDefinition) IsPersonalized() bool {
	for _, group := range q.Groups {
		for _, rule := range group.Rules {
			if QueryFieldRequiresProfile(rule.Field) {
				return true
			}
		}
	}
	return QuerySortRequiresProfile(q.Sort.Field)
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

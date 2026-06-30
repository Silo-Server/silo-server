package jellycompat

import (
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type itemsQuery struct {
	limit                  int
	startIndex             int
	enableTotalRecordCount bool
	searchTerm             string
	namePrefix             string
	maxOfficialRating      string
	parentLibraryID        int
	parentItemID           string
	parentSeasonID         string
	parentCollectionID     string
	specificIDs            []string
	specificCollectionIDs  []string
	itemTypes              []string
	genreName              string
	isFavorite             bool
	isResumable            bool
	hasItemTypeFilter      bool // true when IncludeItemTypes or ExcludeItemTypes was present in the request
	wantsBoxSets           bool // true when IncludeItemTypes contains BoxSet
	wantsViews             bool // true when IncludeItemTypes contains CollectionFolder
	sortExplicit           bool // true when SortBy was present in the request
	needsDetailFields      bool // true when requested Fields include detail-level data (e.g. MediaSources)
	itemType               string
	sort                   string
	order                  string
	personID               int64
	isPlayed               *bool // nil = not specified
	imageTypeLimit         *int  // nil = not specified
	requireBackdrop        bool  // true when ImageTypes includes Backdrop (filter, not just a hint)
	mediaTypes             []string
	mediaTypesSet          map[string]bool
	mediaTypesExplicit     bool
	requestedFields        map[string]bool // parsed from Fields param
	fieldsExplicit         bool            // true when Fields was in the request
	startItemID            string          // raw encoded ID from StartItemId param
	adjacentTo             string          // raw encoded ID from AdjacentTo param
}

func parseItemsQuery(r *http.Request, codec *ResourceIDCodec) itemsQuery {
	q := newCaseInsensitiveQuery(r.URL.Query())
	result := itemsQuery{
		limit:                  parsePositiveInt(q.Get("Limit"), 24),
		startIndex:             parsePositiveInt(q.Get("StartIndex"), 0),
		enableTotalRecordCount: parseBool(q.Get("EnableTotalRecordCount"), true),
		searchTerm:             strings.TrimSpace(q.Get("SearchTerm")),
		namePrefix:             strings.TrimSpace(firstNonEmpty(q.Get("NameStartsWith"), q.Get("StartsWith"))),
		maxOfficialRating:      strings.TrimSpace(q.Get("MaxOfficialRating")),
		sort:                   mapSortBy(q.Get("SortBy")),
		order:                  mapSortOrder(q.Get("SortOrder")),
	}

	if parentID := strings.TrimSpace(q.Get("ParentId")); parentID != "" {
		if libraryID, err := codec.DecodeIntID(EncodedIDLibrary, parentID); err == nil {
			result.parentLibraryID = int(libraryID)
		} else if collectionID, collErr := codec.DecodeStringID(EncodedIDCollection, parentID); collErr == nil && collectionID != "" {
			result.parentCollectionID = collectionID
		} else if seasonID, seasonErr := codec.DecodeStringID(EncodedIDSeason, parentID); seasonErr == nil && seasonID != "" {
			// A season ParentId means "list this season's episodes". The codec's
			// kind tagging already keeps Season and Item IDs distinct; decoding
			// Season first is defensive in case that separation ever changes.
			result.parentSeasonID = seasonID
		} else if contentID, itemErr := decodeItemID(codec, parentID); itemErr == nil && contentID != "" {
			result.parentItemID = contentID
		}
	}

	if ids := strings.TrimSpace(q.Get("Ids")); ids != "" {
		parts := strings.SplitSeq(ids, ",")
		for part := range parts {
			raw := strings.TrimSpace(part)
			if decoded, err := decodeItemID(codec, raw); err == nil && decoded != "" {
				result.specificIDs = append(result.specificIDs, decoded)
			} else if collectionID, collErr := codec.DecodeStringID(EncodedIDCollection, raw); collErr == nil && collectionID != "" {
				result.specificCollectionIDs = append(result.specificCollectionIDs, collectionID)
			}
		}
	}
	if genreIDs := strings.TrimSpace(firstNonEmpty(q.Get("GenreIds"), q.Get("GenreItems"))); genreIDs != "" {
		for part := range strings.SplitSeq(genreIDs, ",") {
			decoded, err := codec.DecodeStringID(EncodedIDGenre, strings.TrimSpace(part))
			if err == nil && decoded != "" {
				result.genreName = decoded
				break
			}
		}
	}

	if personIDs := strings.TrimSpace(q.Get("PersonIds")); personIDs != "" {
		for part := range strings.SplitSeq(personIDs, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			if decoded, err := codec.DecodeIntID(EncodedIDPerson, trimmed); err == nil && decoded > 0 {
				result.personID = decoded
				break
			}
		}
	}

	rawItemTypes := q.Values("IncludeItemTypes")
	rawExcludedItemTypes := q.Values("ExcludeItemTypes")
	result.hasItemTypeFilter = hasNonEmptyValues(rawItemTypes) || hasNonEmptyValues(rawExcludedItemTypes)
	result.itemTypes = effectiveItemTypes(rawItemTypes, rawExcludedItemTypes)
	result.wantsBoxSets = includeItemTypesContain(rawItemTypes, "boxset")
	result.wantsViews = includeItemTypesContain(rawItemTypes, "collectionfolder")
	result.sortExplicit = strings.TrimSpace(q.Get("SortBy")) != ""
	if len(result.itemTypes) > 0 {
		result.itemType = result.itemTypes[0]
	}
	result.isFavorite = hasFilter(q.Get("Filters"), "IsFavorite") || parseBool(q.Get("IsFavorite"), false)
	result.isResumable = hasFilter(q.Get("Filters"), "IsResumable")

	// IsPlayed filter.
	if isPlayedRaw := q.Get("IsPlayed"); isPlayedRaw != "" {
		val := strings.EqualFold(isPlayedRaw, "true") || isPlayedRaw == "1"
		result.isPlayed = &val
	}

	// ImageTypeLimit.
	if itlRaw := q.Get("ImageTypeLimit"); itlRaw != "" {
		if itl, err := strconv.Atoi(itlRaw); err == nil {
			result.imageTypeLimit = &itl
		}
	}

	// ImageTypes acts as a filter: clients (e.g. Wholphin genre cards) request
	// ImageTypes=Backdrop and assume every returned item has a backdrop. Only
	// Backdrop is enforced — the catalog browse path can filter on backdrop_path.
	for _, raw := range q.Values("ImageTypes") {
		for part := range strings.SplitSeq(raw, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Backdrop") {
				result.requireBackdrop = true
			}
		}
	}

	mediaTypesRaw := q.Values("MediaTypes")
	result.mediaTypes = parseMediaTypes(mediaTypesRaw)
	result.mediaTypesExplicit = len(mediaTypesRaw) > 0 && strings.TrimSpace(strings.Join(mediaTypesRaw, "")) != ""
	if len(result.mediaTypes) > 0 {
		result.mediaTypesSet = make(map[string]bool, len(result.mediaTypes))
		for _, mediaType := range result.mediaTypes {
			result.mediaTypesSet[mediaType] = true
		}
	}

	// Fields — parse requested fields and track if explicitly present.
	// Clients send Fields two ways: comma-separated in a single param (VidHub:
	// Fields=A,B,C) OR as repeated params (Wholphin / jellyfin-sdk-kotlin:
	// Fields=A&Fields=B&Fields=C). q.Get returns only the FIRST repeated value,
	// which silently dropped every field after the first — so a Wholphin
	// playlist request (Fields=PrimaryImageAspectRatio&...&Fields=MediaSources)
	// never triggered the detail path and came back without MediaSources,
	// breaking next-episode playback. Join all values, then split on commas.
	fieldsRaw := strings.Join(q.Values("Fields"), ",")
	result.requestedFields = parseRequestedFields(fieldsRaw)
	result.fieldsExplicit = strings.TrimSpace(fieldsRaw) != ""
	result.needsDetailFields = requestedFieldsNeedDetail(result.requestedFields)

	result.startItemID = strings.TrimSpace(q.Get("StartItemId"))
	result.adjacentTo = strings.TrimSpace(q.Get("AdjacentTo"))

	// Diagnostic: when the request stays on the list path, emit a Debug log
	// listing any requested Fields that mapping.go's itemFromList does not
	// populate AND that are not in the detail-required allowlist. Those
	// fields are silently dropped from the response. Operators can grep for
	// "jellycompat unsatisfied fields" to discover client/server feature
	// drift (e.g., a client asking for RemoteTrailers without also asking
	// for Chapters/MediaSources/People to trigger detail).
	if !result.needsDetailFields {
		if missing := unsatisfiedListFields(result.requestedFields); len(missing) > 0 {
			slog.DebugContext(r.Context(), "jellycompat unsatisfied fields",
				"path", r.URL.Path,
				"fields", missing,
				"hint", "list-path response will omit these; add a detail-required field (e.g. MediaSources) to switch paths")
		}
	}

	return result
}

// parseSuggestionsQuery parses the Jellyfin Suggestions endpoint parameters.
// The Suggestions API uses "type" (not "IncludeItemTypes") to filter item types.
func parseSuggestionsQuery(r *http.Request, codec *ResourceIDCodec) itemsQuery {
	q := newCaseInsensitiveQuery(r.URL.Query())
	result := itemsQuery{
		limit:      parsePositiveInt(q.Get("Limit"), 10),
		startIndex: parsePositiveInt(q.Get("StartIndex"), 0),
	}
	// The Suggestions endpoint uses "type" rather than "IncludeItemTypes".
	// The bracket variant (type[]) is handled by caseInsensitiveQuery.Values.
	typeValues := q.Values("Type")
	mapped := mapIncludeItemTypes(typeValues)
	if len(mapped) > 0 {
		result.itemType = mapped[0]
		result.itemTypes = mapped
	}
	return result
}

func buildLatestBrowseParams(query itemsQuery) url.Values {
	params := buildBrowseParams(query)
	// "recently_added" sorts by mil.first_seen_at, which the
	// idx_item_libraries_folder_seen_content index orders for free —
	// vs "created_at" which forces a full-library top-N heapsort.
	params.Set("sort", "recently_added")
	params.Set("order", "desc")
	return params
}

func buildBrowseParams(query itemsQuery) url.Values {
	params := url.Values{}
	params.Set("limit", strconv.Itoa(query.limit))
	params.Set("offset", strconv.Itoa(query.startIndex))
	if len(query.itemTypes) > 0 {
		params.Set("type", strings.Join(query.itemTypes, ","))
	}
	if query.parentLibraryID > 0 {
		params.Set("library_id", strconv.Itoa(query.parentLibraryID))
	}
	if query.genreName != "" {
		params.Set("genre", query.genreName)
	}
	if query.namePrefix != "" {
		params.Set("name_prefix", query.namePrefix)
	}
	if query.sort != "" {
		params.Set("sort", query.sort)
	}
	if query.order != "" {
		params.Set("order", query.order)
	}
	params.Set("include_total", strconv.FormatBool(query.enableTotalRecordCount))
	if query.personID > 0 {
		params.Set("person_id", strconv.FormatInt(query.personID, 10))
	}
	if query.maxOfficialRating != "" {
		params.Set("max_content_rating", query.maxOfficialRating)
	}
	if query.isPlayed != nil {
		if *query.isPlayed {
			params.Set("is_played", "true")
		} else {
			params.Set("is_played", "false")
		}
	}
	if query.requireBackdrop {
		params.Set("require_backdrop", "true")
	}
	return params
}

func favoriteItemsNeedBrowseFilters(query itemsQuery) bool {
	return query.parentLibraryID > 0 ||
		query.genreName != "" ||
		query.namePrefix != "" ||
		query.maxOfficialRating != "" ||
		query.sort != "" ||
		query.order != "" ||
		query.personID > 0 ||
		query.isPlayed != nil ||
		len(query.specificIDs) > 0
}

// favoriteBrowseFiltersSupportedBySQL reports whether the favorite items query
// can be served by the catalog.BrowseFavorites single-query SQL path. Filters
// that require joining user_progress (isPlayed) or item_people (personID) are
// not supported by that path; specific-ID intersections are also routed to the
// legacy two-query fallback to keep the SQL plan simple. Sorts outside
// catalog.IsBrowseFavoritesSortSupported (e.g. random, rating_imdb) would
// silently fall back to added_at in BrowseFavorites — fall back to the legacy
// path so client-requested ordering is preserved.
func favoriteBrowseFiltersSupportedBySQL(query itemsQuery) bool {
	if query.isPlayed != nil {
		return false
	}
	if query.personID > 0 {
		return false
	}
	if len(query.specificIDs) > 0 {
		return false
	}
	if !catalog.IsBrowseFavoritesSortSupported(query.sort) {
		return false
	}
	return true
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func parseBool(s string, defaultVal bool) bool {
	if s == "" {
		return defaultVal
	}
	return strings.EqualFold(s, "true") || s == "1"
}

func mapIncludeItemTypes(rawValues []string) []string {
	if len(rawValues) == 0 {
		return nil
	}

	seen := map[string]bool{}
	result := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		for part := range strings.SplitSeq(raw, ",") {
			var mapped string
			switch strings.ToLower(strings.TrimSpace(part)) {
			case "movie", "movies":
				mapped = "movie"
			case "series", "tvshows", "show":
				mapped = "series"
			case "episode", "episodes":
				mapped = "episode"
			case "season", "seasons":
				mapped = "season"
			}
			if mapped == "" || seen[mapped] {
				continue
			}
			seen[mapped] = true
			result = append(result, mapped)
		}
	}
	return result
}

func effectiveItemTypes(rawIncluded, rawExcluded []string) []string {
	included := mapIncludeItemTypes(rawIncluded)
	excluded := mapIncludeItemTypes(rawExcluded)
	if len(excluded) == 0 {
		return included
	}

	base := included
	if len(base) == 0 && !hasNonEmptyValues(rawIncluded) {
		base = compatVideoTypeList
	}
	if len(base) == 0 {
		return nil
	}

	excludedSet := make(map[string]struct{}, len(excluded))
	for _, itemType := range excluded {
		excludedSet[itemType] = struct{}{}
	}
	result := make([]string, 0, len(base))
	for _, itemType := range base {
		if _, skip := excludedSet[itemType]; skip {
			continue
		}
		result = append(result, itemType)
	}
	return result
}

func hasNonEmptyValues(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// includeItemTypesContain reports whether a raw IncludeItemTypes value list
// contains the given (lowercase) type, before mapIncludeItemTypes drops
// entries it cannot map to catalog types (e.g. BoxSet).
func includeItemTypesContain(rawValues []string, target string) bool {
	for _, raw := range rawValues {
		for part := range strings.SplitSeq(raw, ",") {
			if strings.ToLower(strings.TrimSpace(part)) == target {
				return true
			}
		}
	}
	return false
}

func parseMediaTypes(rawValues []string) []string {
	if len(rawValues) == 0 {
		return nil
	}

	seen := map[string]bool{}
	result := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		for part := range strings.SplitSeq(raw, ",") {
			mediaType := strings.ToLower(strings.TrimSpace(part))
			if mediaType == "" || seen[mediaType] {
				continue
			}
			seen[mediaType] = true
			result = append(result, mediaType)
		}
	}
	return result
}

func mapSortBy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(raw, ",")[0])) {
	case "sortname", "name":
		return "sort_title"
	case "datecreated":
		return "created_at"
	case "premiered", "premieredate":
		return "release_date"
	case "productionyear":
		return "year"
	case "communityrating":
		return "rating_imdb"
	case "random":
		return "random"
	case "dateplayed", "datelastcontentadded":
		return "created_at"
	default:
		return "created_at"
	}
}

func mapSortOrder(raw string) string {
	if strings.EqualFold(raw, "Ascending") {
		return "asc"
	}
	return "desc"
}

func parseRequestedFields(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	fields := map[string]bool{}
	for part := range strings.SplitSeq(raw, ",") {
		key := strings.ToLower(strings.TrimSpace(part))
		if key != "" {
			fields[key] = true
		}
	}
	return fields
}

// fieldsRequiringDetail enumerates the (case-insensitive) Jellyfin Fields
// values that genuinely require a per-item GetItemDetail call. All other
// fields can be served by browse-level joins; do NOT add fields here just
// to be safe — every entry causes an N+1 amplification (one detail fetch
// per result item, e.g. ~525 queries for /Shows/{id}/Episodes on a
// 500-episode series).
//
// Keys must be lowercase. parseRequestedFields normalizes incoming Fields
// to lowercase before storing them in the requestedFields map.
//
// mediasources is listed because BrowseRepository does not yet project
// per-file media metadata. When the LATERAL JOIN against media_files lands
// (catalog SQL performance overhaul plan §3.2 part b), it can be removed.
var fieldsRequiringDetail = map[string]struct{}{
	"people":       {},
	"chapters":     {},
	"mediastreams": {},
	"mediasources": {},
}

// fieldsServedByList enumerates Fields values that mapping.go's itemFromList
// can populate — gated by `if allFields || fields[X]` blocks. Anything outside
// this set AND outside fieldsRequiringDetail is silently dropped from
// list-path responses (no detail fetch is triggered to fill it).
//
// Keep aligned with mapping.go itemFromList. When you add a new
// `fields[X]`-gated branch there, add the lowercase key here too.
var fieldsServedByList = map[string]struct{}{
	"overview":            {},
	"genres":              {},
	"etag":                {},
	"sortname":            {},
	"studios":             {},
	"taglines":            {},
	"tags":                {},
	"productionlocations": {},
	"criticrating":        {},
	"mediasourcecount":    {},
	"providerids":         {},
}

func requestedFieldsNeedDetail(fields map[string]bool) bool {
	for field := range fields {
		key := strings.ToLower(strings.TrimSpace(field))
		if _, ok := fieldsRequiringDetail[key]; ok {
			return true
		}
	}
	return false
}

// unsatisfiedListFields returns the (sorted, lowercased) Fields values that
// will be silently dropped on list-path responses — neither populated by the
// browse mapper nor recognized as a detail-required field that would trigger
// a per-item GetItemDetail fetch. Returns nil when the request will fall
// into the detail path (which serves all fields) or when no fields are
// unserved. Diagnostic only — does not change response behavior.
func unsatisfiedListFields(fields map[string]bool) []string {
	if len(fields) == 0 {
		return nil
	}
	if requestedFieldsNeedDetail(fields) {
		return nil
	}
	var out []string
	for field := range fields {
		key := strings.ToLower(strings.TrimSpace(field))
		if key == "" || key == "*" {
			continue
		}
		if _, ok := fieldsServedByList[key]; ok {
			continue
		}
		if _, ok := fieldsRequiringDetail[key]; ok {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func hasFilter(raw, target string) bool {
	for part := range strings.SplitSeq(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(part), target) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// caseInsensitiveQuery wraps url.Values with case-insensitive key lookup.
// Jellyfin clients use inconsistent casing (PascalCase, camelCase, lowercase).
type caseInsensitiveQuery struct {
	index map[string]string // lowercase key → original key
	raw   url.Values
}

func newCaseInsensitiveQuery(values url.Values) caseInsensitiveQuery {
	index := make(map[string]string, len(values))
	for key := range values {
		lower := strings.ToLower(key)
		if _, exists := index[lower]; !exists {
			index[lower] = key
		}
	}
	return caseInsensitiveQuery{index: index, raw: values}
}

func (q caseInsensitiveQuery) Get(key string) string {
	lower := strings.ToLower(key)
	if orig, ok := q.index[lower]; ok {
		return q.raw.Get(orig)
	}
	// Jellyfin SDKs may send "key[]" for array params — try bracket variant.
	if orig, ok := q.index[lower+"[]"]; ok {
		return q.raw.Get(orig)
	}
	return ""
}

func (q caseInsensitiveQuery) Values(key string) []string {
	lower := strings.ToLower(key)
	if orig, ok := q.index[lower]; ok {
		return q.raw[orig]
	}
	// Jellyfin SDKs may send "key[]" for array params — try bracket variant.
	if orig, ok := q.index[lower+"[]"]; ok {
		return q.raw[orig]
	}
	return nil
}

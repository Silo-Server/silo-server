package catalog

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	catalogGroupMatchPattern     = regexp.MustCompile(`^groups\[(\d+)\]\[match\]$`)
	catalogGroupRulePattern      = regexp.MustCompile(`^groups\[(\d+)\]\[rules\]\[(\d+)\]\[(field|op|value)\]$`)
	catalogGroupRuleValuePattern = regexp.MustCompile(`^groups\[(\d+)\]\[rules\]\[(\d+)\]\[value\]\[(\d+)\]$`)
)

type catalogOverlay struct {
	searchQuery     string
	namePrefix      string
	query           QueryDefinition
	hasExplicitSort bool
}

type catalogGroupBuilder struct {
	match string
	rules map[int]*catalogRuleBuilder
}

type catalogRuleBuilder struct {
	field         string
	op            string
	values        []any
	indexedValues map[int]any
}

// ParseCatalogRequest converts catalog URL params into a normalized request.
func ParseCatalogRequest(values url.Values) (CatalogRequest, error) {
	req := CatalogRequest{
		Source: CatalogSource(strings.ToLower(strings.TrimSpace(values.Get("source")))),
		Limit:  20,
	}
	if req.Source == "" {
		req.Source = CatalogSourceQuery
	}

	if limit := ParseIntParam(values.Get("limit")); limit > 0 {
		req.Limit = limit
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	req.Offset = max(ParseIntParam(values.Get("offset")), 0)
	req.SkipTotal = parseCatalogSkipTotal(values.Get("include_total"))

	if raw := strings.TrimSpace(values.Get("snapshot")); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return CatalogRequest{}, fmt.Errorf("invalid snapshot timestamp: %w", err)
		}
		req.SnapshotAt = &t
	}

	switch req.Source {
	case CatalogSourceQuery:
		overlay, err := parseCatalogOverlay(values)
		if err != nil {
			return CatalogRequest{}, err
		}
		req.NamePrefix = overlay.namePrefix
		req.SearchQuery = overlay.searchQuery
		req.Query = overlay.query
		req.UseSourceOrder = false
		if !overlay.hasExplicitSort {
			req.Query.Sort = defaultCatalogQuerySort(req.SearchQuery)
		}
	case CatalogSourceFavorites, CatalogSourceWatchlist, CatalogSourceHistory:
		overlay, err := parseCatalogOverlay(values)
		if err != nil {
			return CatalogRequest{}, err
		}
		req.NamePrefix = overlay.namePrefix
		req.SearchQuery = overlay.searchQuery
		req.Query = overlay.query
		req.UseSourceOrder = !overlay.hasExplicitSort
	case CatalogSourcePerson:
		req.PersonID = ParseInt64Param(values.Get("person_id"))
		if req.PersonID <= 0 {
			return CatalogRequest{}, fmt.Errorf("person_id is required")
		}
		overlay, err := parseCatalogOverlay(values)
		if err != nil {
			return CatalogRequest{}, err
		}
		req.NamePrefix = overlay.namePrefix
		req.SearchQuery = overlay.searchQuery
		req.Query = overlay.query
		req.UseSourceOrder = !overlay.hasExplicitSort
	case CatalogSourceSection:
		scope := strings.ToLower(strings.TrimSpace(values.Get("scope")))
		if scope == "" {
			scope = "library"
		}
		if scope != "home" && scope != "library" {
			return CatalogRequest{}, fmt.Errorf("scope must be 'home' or 'library'")
		}
		req.Scope = scope
		req.SectionID = strings.TrimSpace(values.Get("section_id"))
		if req.SectionID == "" {
			return CatalogRequest{}, fmt.Errorf("section_id is required")
		}
		if scope == "library" {
			libraryID := ParseIntParam(values.Get("library_id"))
			if libraryID <= 0 {
				return CatalogRequest{}, fmt.Errorf("library_id is required for library sections")
			}
			req.LibraryID = libraryID
		}
		if hasCatalogOverlayParams(values, scope == "library") {
			return CatalogRequest{}, fmt.Errorf("source %q does not allow overlay params", req.Source)
		}
		req.UseSourceOrder = true
	case CatalogSourceLibraryCollection, CatalogSourceUserCollection:
		req.CollectionID = strings.TrimSpace(values.Get("collection_id"))
		if req.CollectionID == "" {
			return CatalogRequest{}, fmt.Errorf("collection_id is required")
		}
		if hasCatalogOverlayParams(values, false) {
			return CatalogRequest{}, fmt.Errorf("source %q does not allow overlay params", req.Source)
		}
		req.UseSourceOrder = true
	default:
		return CatalogRequest{}, fmt.Errorf("unsupported catalog source %q", req.Source)
	}

	return req, nil
}

func parseCatalogSkipTotal(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	includeTotal, err := strconv.ParseBool(raw)
	if err != nil {
		return false
	}
	return !includeTotal
}

func parseCatalogOverlay(values url.Values) (catalogOverlay, error) {
	def := QueryDefinition{
		Match:  normalizeCatalogMatch(values.Get("match")),
		Groups: []QueryGroup{},
	}

	searchQuery := strings.TrimSpace(values.Get("q"))
	if libraryID := ParseIntParam(values.Get("library_id")); libraryID > 0 {
		def.LibraryIDs = []int{libraryID}
	}
	if mediaScope := parseCatalogMediaScope(values.Get("type")); mediaScope != "" {
		def.MediaScope = mediaScope
	}

	implicitRules := make([]QueryRule, 0, 4)
	if genre := strings.TrimSpace(values.Get("genre")); genre != "" {
		implicitRules = append(implicitRules, QueryRule{Field: "genre", Op: "contains", Value: genre})
	}
	if status := strings.TrimSpace(values.Get("status")); status != "" {
		implicitRules = append(implicitRules, QueryRule{Field: "status", Op: "is", Value: status})
	}

	yearMin := ParseIntParam(values.Get("year_min"))
	yearMax := ParseIntParam(values.Get("year_max"))
	switch {
	case yearMin > 0 && yearMax > 0:
		implicitRules = append(implicitRules, QueryRule{Field: "year", Op: "between", Value: []any{yearMin, yearMax}})
	case yearMin > 0:
		implicitRules = append(implicitRules, QueryRule{Field: "year", Op: "gte", Value: yearMin})
	case yearMax > 0:
		implicitRules = append(implicitRules, QueryRule{Field: "year", Op: "lte", Value: yearMax})
	}

	if len(implicitRules) > 0 {
		def.Groups = append(def.Groups, QueryGroup{
			Match: "all",
			Rules: implicitRules,
		})
	}

	if ratings := ParseContentRatings(values.Get("content_rating")); len(ratings) == 1 {
		def.Groups = append(def.Groups, QueryGroup{
			Match: "all",
			Rules: []QueryRule{{Field: "content_rating", Op: "is", Value: ratings[0]}},
		})
	} else if len(ratings) > 1 {
		rules := make([]QueryRule, 0, len(ratings))
		for _, rating := range ratings {
			rules = append(rules, QueryRule{Field: "content_rating", Op: "is", Value: rating})
		}
		def.Groups = append(def.Groups, QueryGroup{Match: "any", Rules: rules})
	}

	explicitGroups, err := parseCatalogGroups(values)
	if err != nil {
		return catalogOverlay{}, err
	}
	def.Groups = append(def.Groups, explicitGroups...)

	hasExplicitSort := values.Get("sort") != "" || values.Get("order") != ""
	if hasExplicitSort {
		def.Sort = normalizeExplicitCatalogSort(values.Get("sort"), values.Get("order"))
	}

	return catalogOverlay{
		searchQuery:     searchQuery,
		namePrefix:      strings.TrimSpace(values.Get("name_prefix")),
		query:           def,
		hasExplicitSort: hasExplicitSort,
	}, nil
}

func parseCatalogGroups(values url.Values) ([]QueryGroup, error) {
	groupBuilders := map[int]*catalogGroupBuilder{}

	for key, rawValues := range values {
		if matches := catalogGroupMatchPattern.FindStringSubmatch(key); matches != nil {
			groupIdx, _ := strconv.Atoi(matches[1])
			builder := ensureCatalogGroupBuilder(groupBuilders, groupIdx)
			if len(rawValues) > 0 {
				builder.match = rawValues[0]
			}
			continue
		}

		if matches := catalogGroupRulePattern.FindStringSubmatch(key); matches != nil {
			groupIdx, _ := strconv.Atoi(matches[1])
			ruleIdx, _ := strconv.Atoi(matches[2])
			fieldName := matches[3]
			rule := ensureCatalogRuleBuilder(groupBuilders, groupIdx, ruleIdx)
			switch fieldName {
			case "field":
				if len(rawValues) > 0 {
					rule.field = rawValues[0]
				}
			case "op":
				if len(rawValues) > 0 {
					rule.op = rawValues[0]
				}
			case "value":
				rule.values = make([]any, 0, len(rawValues))
				for _, value := range rawValues {
					rule.values = append(rule.values, parseCatalogScalar(value))
				}
			}
			continue
		}

		if matches := catalogGroupRuleValuePattern.FindStringSubmatch(key); matches != nil {
			groupIdx, _ := strconv.Atoi(matches[1])
			ruleIdx, _ := strconv.Atoi(matches[2])
			valueIdx, _ := strconv.Atoi(matches[3])
			rule := ensureCatalogRuleBuilder(groupBuilders, groupIdx, ruleIdx)
			if len(rawValues) == 0 {
				continue
			}
			if rule.indexedValues == nil {
				rule.indexedValues = map[int]any{}
			}
			rule.indexedValues[valueIdx] = parseCatalogScalar(rawValues[0])
		}
	}

	if len(groupBuilders) == 0 {
		return nil, nil
	}

	groupIndexes := make([]int, 0, len(groupBuilders))
	for idx := range groupBuilders {
		groupIndexes = append(groupIndexes, idx)
	}
	slices.Sort(groupIndexes)

	groups := make([]QueryGroup, 0, len(groupIndexes))
	for _, groupIdx := range groupIndexes {
		builder := groupBuilders[groupIdx]
		ruleIndexes := make([]int, 0, len(builder.rules))
		for idx := range builder.rules {
			ruleIndexes = append(ruleIndexes, idx)
		}
		slices.Sort(ruleIndexes)

		rules := make([]QueryRule, 0, len(ruleIndexes))
		for _, ruleIdx := range ruleIndexes {
			ruleBuilder := builder.rules[ruleIdx]
			rules = append(rules, QueryRule{
				Field: ruleBuilder.field,
				Op:    ruleBuilder.op,
				Value: ruleBuilder.value(),
			})
		}

		groups = append(groups, QueryGroup{
			Match: normalizeCatalogMatch(builder.match),
			Rules: rules,
		})
	}

	return groups, nil
}

func ensureCatalogGroupBuilder(groups map[int]*catalogGroupBuilder, groupIdx int) *catalogGroupBuilder {
	builder := groups[groupIdx]
	if builder == nil {
		builder = &catalogGroupBuilder{rules: map[int]*catalogRuleBuilder{}}
		groups[groupIdx] = builder
	}
	return builder
}

func ensureCatalogRuleBuilder(groups map[int]*catalogGroupBuilder, groupIdx, ruleIdx int) *catalogRuleBuilder {
	group := ensureCatalogGroupBuilder(groups, groupIdx)
	rule := group.rules[ruleIdx]
	if rule == nil {
		rule = &catalogRuleBuilder{}
		group.rules[ruleIdx] = rule
	}
	return rule
}

func (r *catalogRuleBuilder) value() any {
	if len(r.indexedValues) > 0 {
		indexes := make([]int, 0, len(r.indexedValues))
		for idx := range r.indexedValues {
			indexes = append(indexes, idx)
		}
		slices.Sort(indexes)

		values := make([]any, 0, len(indexes))
		for _, idx := range indexes {
			values = append(values, r.indexedValues[idx])
		}
		return values
	}

	if len(r.values) == 1 {
		return r.values[0]
	}
	if len(r.values) > 1 {
		return r.values
	}
	return nil
}

func hasCatalogOverlayParams(values url.Values, ignoreLibraryID bool) bool {
	for key, rawValues := range values {
		if len(rawValues) == 0 {
			continue
		}
		if strings.HasPrefix(key, "groups[") {
			return true
		}

		switch key {
		case "source", "scope", "section_id", "collection_id", "title", "limit", "offset", "snapshot":
			continue
		case "library_id":
			if ignoreLibraryID {
				continue
			}
			return true
		case "q", "name_prefix", "match", "type", "genre", "year_min", "year_max", "content_rating", "status", "sort", "order":
			return true
		}
	}

	return false
}

func normalizeCatalogMatch(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "any") {
		return "any"
	}
	return "all"
}

func parseCatalogMediaScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "movie", "series", "episode", "audiobook":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func defaultCatalogQuerySort(searchQuery string) QuerySort {
	if strings.TrimSpace(searchQuery) != "" {
		return QuerySort{Field: "relevance", Order: "desc"}
	}
	return QuerySort{Field: "title", Order: "asc"}
}

func normalizeExplicitCatalogSort(field, order string) QuerySort {
	normalizedField := strings.ToLower(strings.TrimSpace(field))
	normalizedOrder := strings.ToLower(strings.TrimSpace(order))

	if normalizedField == "relevance" {
		if normalizedOrder == "" {
			normalizedOrder = "desc"
		}
		return QuerySort{Field: normalizedField, Order: normalizedOrder}
	}

	normalized := NormalizeQuerySort(QuerySort{Field: normalizedField, Order: normalizedOrder})
	if normalizedField == "" {
		normalized.Field = ""
	}
	return normalized
}

func parseCatalogScalar(raw string) any {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
	case "true":
		return true
	case "false":
		return false
	}

	if i, err := strconv.Atoi(trimmed); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return f
	}

	return trimmed
}

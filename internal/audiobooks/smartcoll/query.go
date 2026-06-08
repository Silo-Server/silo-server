// Package smartcoll implements the rule-based Smart Collection DSL for
// silo's ABS audiobook surface. Audiobook-domain field catalog
// (title, author, narrator, series, genre, year, rating, language,
// publisher, added_at, duration_seconds, plus personalized: finished,
// in_progress, last_played, abandoned, bookmark_count).
//
// All evaluation happens Go-side (see evaluator.go); SQL pushdown is
// a deferred follow-up (parent spec §10).
package smartcoll

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// QueryDefinition is the wire-shape stored in smart_collection.query_def.
// Mirrors the host's QueryDefinition exactly except for the audiobook
// field catalog and the absence of media_scope (audiobook libraries
// are single-type already; podcast libraries handle their own shelves).
type QueryDefinition struct {
	LibraryIDs []int64      `json:"library_ids,omitempty"`
	Match      string       `json:"match"`
	Groups     []QueryGroup `json:"groups"`
	Sort       QuerySort    `json:"sort"`
	Limit      *int         `json:"limit,omitempty"`
}

// QueryGroup combines a list of rules with a per-group all/any boolean
// combinator. Top-level QueryDefinition.Match combines groups; per-
// group QueryGroup.Match combines rules.
type QueryGroup struct {
	Match string      `json:"match"`
	Rules []QueryRule `json:"rules"`
}

// QueryRule is one filter clause: field + op + value. value is any so
// it tolerates strings, numbers, booleans, and 2-tuples for `between`.
type QueryRule struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// QuerySort picks the result ordering. `relevance` is reserved for a
// future embedding-similarity sort; today it falls back to added_at.
type QuerySort struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

// defaultSortField is the fallback ordering when none is supplied —
// newest-first added_at, mirroring real ABS catalog defaults.
const defaultSortField = "added_at"

// queryFieldDef is the catalog entry that pins (a) the wire field name
// to (b) the set of operators that field can use and (c) whether the
// field requires user/profile scope. Personalized fields touch
// per-user state (progress / bookmarks / play counts) and can only
// be evaluated against the requesting user.
type queryFieldDef struct {
	validOps     map[string]bool
	isArray      bool // field holds an array (e.g. genres) — affects "contains"
	personalized bool // requires per-user state
}

// querySortDef is the catalog entry for one sortable column —
// defaultOrder (when caller omits Order) and a personalized flag for
// sorts that read per-user state.
type querySortDef struct {
	defaultOrder string
	personalized bool
}

// queryFieldAliases lets older clients keep working when we canonicalise
// a field name — alias keys map to the canonical entry in
// queryFieldDefs. "rating" → "rating_imdb" is the host pattern; we map
// "narrators" / "authors" plurals back to singular here.
var queryFieldAliases = map[string]string{
	"authors":   "author",
	"narrators": "narrator",
	"genres":    "genre",
}

// querySortAliases analogous to queryFieldAliases for the sort field
// catalog. Keeps URL-style "recently-added" / "sort_title" client
// vocab usable.
var querySortAliases = map[string]string{
	"sort_title":     "title",
	"recently_added": "added_at",
	"duration":       "duration_seconds",
}

// queryFieldDefs is the audiobook-domain rule field catalog. Mirror of
// the host's queryFieldDefs but adapted from video → audio fields.
// Personalized fields (finished / in_progress / last_played /
// abandoned / bookmark_count) require a user_id at evaluate time —
// the validator rejects them when no profile scope is provided.
var queryFieldDefs = map[string]queryFieldDef{
	"title":            {validOps: map[string]bool{"is": true, "is_not": true, "contains": true}},
	"author":           {validOps: map[string]bool{"is": true, "is_not": true, "contains": true}, isArray: true},
	"narrator":         {validOps: map[string]bool{"is": true, "is_not": true, "contains": true}, isArray: true},
	"series":           {validOps: map[string]bool{"is": true, "is_not": true, "contains": true}, isArray: true},
	"genre":            {validOps: map[string]bool{"is": true, "is_not": true, "contains": true}, isArray: true},
	"year":             {validOps: map[string]bool{"is": true, "is_not": true, "gt": true, "gte": true, "lt": true, "lte": true, "between": true}},
	"rating":           {validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true}},
	"language":         {validOps: map[string]bool{"is": true, "is_not": true}},
	"publisher":        {validOps: map[string]bool{"is": true, "is_not": true, "contains": true}},
	"added_at":         {validOps: map[string]bool{"gt": true, "lt": true, "between": true, "in_last": true}},
	"duration_seconds": {validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true}},
	"finished":         {validOps: map[string]bool{"is": true}, personalized: true},
	"in_progress":      {validOps: map[string]bool{"is": true}, personalized: true},
	"last_played":      {validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true, "in_last": true}, personalized: true},
	"abandoned":        {validOps: map[string]bool{"is": true}, personalized: true},
	"bookmark_count":   {validOps: map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "between": true}, personalized: true},
}

// querySortDefs is the audiobook sort catalog. `random` is a sentinel
// that shuffles the result deterministically per-query (seeded by the
// collection id so successive page loads are stable).
var querySortDefs = map[string]querySortDef{
	"title":            {defaultOrder: "asc"},
	"added_at":         {defaultOrder: "desc"},
	"year":             {defaultOrder: "desc"},
	"duration_seconds": {defaultOrder: "desc"},
	"rating":           {defaultOrder: "desc"},
	"random":           {defaultOrder: "asc"},
	"progress":         {defaultOrder: "desc", personalized: true},
	"last_played":      {defaultOrder: "desc", personalized: true},
	"plays":            {defaultOrder: "desc", personalized: true},
}

// Normalize lowercases + trims field/op/match values, applies aliases,
// dedupes library_ids, and supplies sort defaults. Idempotent — calling
// Normalize twice produces the same value.
func (q QueryDefinition) Normalize() QueryDefinition {
	out := q
	out.Match = normalizeMatch(out.Match)
	out.LibraryIDs = normalizeLibraryIDs(out.LibraryIDs)
	out.Sort = NormalizeSort(out.Sort)
	if out.Groups == nil {
		out.Groups = []QueryGroup{}
	}
	for i := range out.Groups {
		out.Groups[i].Match = normalizeMatch(out.Groups[i].Match)
		if out.Groups[i].Rules == nil {
			out.Groups[i].Rules = []QueryRule{}
		}
		for j := range out.Groups[i].Rules {
			field := strings.ToLower(strings.TrimSpace(out.Groups[i].Rules[j].Field))
			if canon, ok := queryFieldAliases[field]; ok {
				field = canon
			}
			out.Groups[i].Rules[j].Field = field
			out.Groups[i].Rules[j].Op = strings.ToLower(strings.TrimSpace(out.Groups[i].Rules[j].Op))
		}
	}
	return out
}

// Validate reports the first structural error in a QueryDefinition.
// Pass allowPersonalized=true when the caller is a user-scoped request
// (the evaluator has a user_id available); false when validating an
// admin-template definition that needs to be reusable across users.
func (q QueryDefinition) Validate(allowPersonalized bool) error {
	n := q.Normalize()
	for _, id := range n.LibraryIDs {
		if id <= 0 {
			return fmt.Errorf("library_ids must contain positive ids")
		}
	}
	if n.Match != "all" && n.Match != "any" {
		return fmt.Errorf("match must be 'all' or 'any'")
	}
	for i, g := range n.Groups {
		if g.Match != "all" && g.Match != "any" {
			return fmt.Errorf("groups[%d].match must be 'all' or 'any'", i)
		}
		for j, r := range g.Rules {
			def, ok := queryFieldDefs[r.Field]
			if !ok {
				return fmt.Errorf("groups[%d].rules[%d].field %q is not supported", i, j, r.Field)
			}
			if def.personalized && !allowPersonalized {
				return fmt.Errorf("groups[%d].rules[%d].field %q requires user scope", i, j, r.Field)
			}
			if !def.validOps[r.Op] {
				return fmt.Errorf("groups[%d].rules[%d].op %q is not valid for field %q", i, j, r.Op, r.Field)
			}
		}
	}
	if n.Sort.Field != "" {
		def, ok := querySortDefs[n.Sort.Field]
		if !ok {
			return fmt.Errorf("sort.field %q is not supported", n.Sort.Field)
		}
		if def.personalized && !allowPersonalized {
			return fmt.Errorf("sort.field %q requires user scope", n.Sort.Field)
		}
	}
	if n.Sort.Order != "" && n.Sort.Order != "asc" && n.Sort.Order != "desc" {
		return fmt.Errorf("sort.order must be 'asc' or 'desc'")
	}
	if n.Limit != nil && *n.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	return nil
}

// NormalizeSort applies aliasing + default-order to a QuerySort.
// Empty Field falls back to defaultSortField; empty Order falls back to
// the field's defaultOrder from querySortDefs.
func NormalizeSort(s QuerySort) QuerySort {
	out := QuerySort{
		Field: strings.ToLower(strings.TrimSpace(s.Field)),
		Order: strings.ToLower(strings.TrimSpace(s.Order)),
	}
	if canon, ok := querySortAliases[out.Field]; ok {
		out.Field = canon
	}
	if out.Field == "" {
		out.Field = defaultSortField
	}
	if out.Order == "" {
		if def, ok := querySortDefs[out.Field]; ok {
			out.Order = def.defaultOrder
		}
	}
	return out
}

// MarshalJSON keeps the JSON-tag-defined shape stable across normalise
// roundtrips. Defined so callers can rely on Marshal(Normalize(x))
// being the canonical wire form.
func (q QueryDefinition) MarshalJSON() ([]byte, error) {
	type alias QueryDefinition
	return json.Marshal(alias(q.Normalize()))
}

// FieldDefs returns the field catalog so other packages (the evaluator,
// the validator, eventually a wizard UI surface) can introspect
// supported fields + ops + whether they're personalized.
func FieldDefs() map[string]struct {
	ValidOps     []string
	IsArray      bool
	Personalized bool
} {
	out := make(map[string]struct {
		ValidOps     []string
		IsArray      bool
		Personalized bool
	}, len(queryFieldDefs))
	for field, def := range queryFieldDefs {
		ops := make([]string, 0, len(def.validOps))
		for op := range def.validOps {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		out[field] = struct {
			ValidOps     []string
			IsArray      bool
			Personalized bool
		}{
			ValidOps:     ops,
			IsArray:      def.isArray,
			Personalized: def.personalized,
		}
	}
	return out
}

// SortFields returns the sort catalog as a name → {defaultOrder,
// personalized} map.
func SortFields() map[string]struct {
	DefaultOrder string
	Personalized bool
} {
	out := make(map[string]struct {
		DefaultOrder string
		Personalized bool
	}, len(querySortDefs))
	for field, def := range querySortDefs {
		out[field] = struct {
			DefaultOrder string
			Personalized bool
		}{
			DefaultOrder: def.defaultOrder,
			Personalized: def.personalized,
		}
	}
	return out
}

func normalizeMatch(m string) string {
	n := strings.ToLower(strings.TrimSpace(m))
	if n == "" {
		return "all"
	}
	return n
}

func normalizeLibraryIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			out = append(out, id)
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

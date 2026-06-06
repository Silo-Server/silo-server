package catalog

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEpisodeCatalogSelectBodyExposesQualifiedColumns guards the invariant that
// the hand-written episode subquery (episodeCatalogSelectBody) exposes every
// column the shared projection reads off the derived "mi" relation.
//
// hydrateEpisodeCatalogEntryPage and the episode base relation both render
//
//	SELECT qualifiedListItemColumns("mi") FROM (episodeCatalogSelectBody) mi
//
// so any column qualifiedListItemColumns references as mi.<x> must be projected
// by the subquery. When a new media_items column is added to the shared lists
// (itemColumns / qualifiedListItemColumns) but not to this subquery, the query
// fails at runtime with `column mi.<x> does not exist` (SQLSTATE 42703) — which
// is NOT caught by episodeCatalogEntriesUnavailable, so it surfaces as a 500
// instead of degrading. This happened with air_timezone (added in the local
// episode airtimes feature). This test fails fast at build/test time instead.
func TestEpisodeCatalogSelectBodyExposesQualifiedColumns(t *testing.T) {
	required := columnsReferencedOnAlias(qualifiedListItemColumns("mi"), "mi")
	if len(required) == 0 {
		t.Fatal("expected qualifiedListItemColumns(\"mi\") to reference columns on mi")
	}

	exposed := episodeCatalogSelectBodyOutputColumns(t)

	var missing []string
	for col := range required {
		if !exposed[col] {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("episodeCatalogSelectBody does not expose columns required by "+
			"qualifiedListItemColumns(\"mi\"): %v\nadd them to the subquery in "+
			"episode_catalog_source.go, or episode catalog hydration fails with "+
			"SQLSTATE 42703 (column mi.<x> does not exist)", missing)
	}
}

// columnsReferencedOnAlias returns every distinct column referenced as
// "<alias>.<column>" within projection (e.g. mi.air_timezone -> air_timezone).
func columnsReferencedOnAlias(projection, alias string) map[string]bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.([a-zA-Z_][a-zA-Z0-9_]*)`)
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(projection, -1) {
		out[m[1]] = true
	}
	return out
}

// episodeCatalogSelectBodyOutputColumns parses the SELECT list of the episode
// catalog subquery and returns the set of output column names it exposes (the
// identifier after AS, or the trailing identifier for bare "tbl.col" columns).
func episodeCatalogSelectBodyOutputColumns(t *testing.T) map[string]bool {
	t.Helper()
	selectList := episodeCatalogSelectListSQL(t)
	out := map[string]bool{}
	for _, part := range splitTopLevelSQLCommas(selectList) {
		if name := sqlOutputColumnName(part); name != "" {
			out[name] = true
		}
	}
	return out
}

// episodeCatalogSelectListSQL extracts the projection list (between SELECT and
// its matching top-level FROM) from the rendered episode base relation.
func episodeCatalogSelectListSQL(t *testing.T) string {
	t.Helper()
	body := episodeCatalogBaseRelation
	upper := strings.ToUpper(body)

	selIdx := strings.Index(upper, "SELECT")
	if selIdx < 0 {
		t.Fatal("episodeCatalogSelectBody has no SELECT")
	}

	// The body is wrapped in an outer "( ... ) mi", so the SELECT list lives at
	// the paren depth present at the SELECT keyword. Nested parens (COALESCE,
	// EXTRACT(... FROM ...), the WHERE EXISTS subquery) sit deeper and are
	// skipped, so the first FROM at the base depth is the real one.
	base := strings.Count(body[:selIdx], "(") - strings.Count(body[:selIdx], ")")

	start := selIdx + len("SELECT")
	depth := base
	for j := start; j < len(body); j++ {
		switch body[j] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == base && isSQLWordAt(upper, j, "FROM") {
			return body[start:j]
		}
	}
	t.Fatal("episodeCatalogSelectBody has no top-level FROM")
	return ""
}

// splitTopLevelSQLCommas splits a SELECT list on commas that sit outside any
// parentheses, leaving commas inside COALESCE(...)/EXTRACT(...) intact.
func splitTopLevelSQLCommas(s string) []string {
	var parts []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[last:i])
				last = i + 1
			}
		}
	}
	return append(parts, s[last:])
}

// sqlOutputColumnName returns the output column name of a single projection:
// the identifier after a top-level AS, otherwise the trailing dotted identifier.
func sqlOutputColumnName(part string) string {
	part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ","))
	if part == "" {
		return ""
	}
	if idx := strings.LastIndex(strings.ToUpper(part), " AS "); idx >= 0 {
		return trailingSQLIdent(part[idx+len(" AS "):])
	}
	return trailingSQLIdent(part)
}

// trailingSQLIdent returns the last dotted identifier segment of s, e.g.
// "si.air_time" -> "air_time", "type" -> "type".
func trailingSQLIdent(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	begin := 0
	for begin < len(s) && !isSQLIdentByte(s[begin]) {
		begin++
	}
	end := begin
	for end < len(s) && isSQLIdentByte(s[end]) {
		end++
	}
	return s[begin:end]
}

func isSQLWordAt(s string, i int, word string) bool {
	if i+len(word) > len(s) || s[i:i+len(word)] != word {
		return false
	}
	if i > 0 && isSQLIdentByte(s[i-1]) {
		return false
	}
	if i+len(word) < len(s) && isSQLIdentByte(s[i+len(word)]) {
		return false
	}
	return true
}

func isSQLIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

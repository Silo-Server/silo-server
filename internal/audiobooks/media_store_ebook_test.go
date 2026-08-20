package audiobooks

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

func TestEbookFilterConditionsUseEbookMetadata(t *testing.T) {
	tests := []struct {
		name   string
		filter abs.Filter
		want   string
	}{
		{name: "series", filter: abs.Filter{Kind: abs.FilterSeries, Value: "Saga"}, want: "FROM ebook_series"},
		{name: "genre", filter: abs.Filter{Kind: abs.FilterGenres, Value: "Fantasy"}, want: "LOWER(genre) = LOWER($1)"},
		{name: "language", filter: abs.Filter{Kind: abs.FilterLanguages, Value: "jpn"}, want: "LOWER(COALESCE"},
		{name: "publisher", filter: abs.Filter{Kind: abs.FilterPublishers, Value: "Tor Books"}, want: "LOWER(publisher) = LOWER($1)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conditions := []string{}
			args := []any{}
			argIdx := 1
			appendAudiobookFilterConditions(absMediaTypeEbook, test.filter, &conditions, &args, &argIdx)
			if len(conditions) != 1 || !strings.Contains(conditions[0], test.want) {
				t.Fatalf("conditions = %v, want fragment %q", conditions, test.want)
			}
		})
	}
}

func TestBookSeriesTableUsesEbookRelation(t *testing.T) {
	if got := bookSeriesTable(absMediaTypeEbook); got != "ebook_series" {
		t.Fatalf("ebook series table = %q", got)
	}
	if got := bookSeriesTable(absMediaTypeAudiobook); got != "audiobook_series" {
		t.Fatalf("audiobook series table = %q", got)
	}
}

func TestABSSearchSQLUsesResolvedLibraryItemTypeInBothArms(t *testing.T) {
	sql := buildABSSearchSQL(absMediaTypeEbook, "", 3)
	if got := strings.Count(sql, "mi.type = 'ebook'"); got != 2 {
		t.Fatalf("ebook type predicates = %d, want 2; SQL=%s", got, sql)
	}
	if strings.Contains(sql, "mi.type = 'audiobook'") {
		t.Fatalf("ebook search retained audiobook-only predicate: %s", sql)
	}
}

func TestLibraryStringAggregationUsesPostgresSafeOrdering(t *testing.T) {
	sql := buildLibraryStringValuesSQL("publisher", `CROSS JOIN LATERAL unnest(COALESCE(mi.studios, ARRAY[]::text[])) AS publisher`, []string{"mi.type = 'ebook'"})
	if strings.Contains(sql, "SELECT DISTINCT") {
		t.Fatalf("query uses DISTINCT with an expression ORDER BY: %s", sql)
	}
	if !strings.Contains(sql, "GROUP BY publisher ORDER BY LOWER(publisher), publisher") {
		t.Fatalf("query does not group and deterministically order publisher values: %s", sql)
	}
	// The filter sheet is a dropdown, not a paginated surface: an unbounded
	// distinct set on a large library is exactly what the paged authors and
	// series listers already refuse to return.
	if !strings.Contains(sql, "LIMIT "+strconv.Itoa(abs.FilterDataFetchCap)) {
		t.Fatalf("query is unbounded; want LIMIT %d: %s", abs.FilterDataFetchCap, sql)
	}
}

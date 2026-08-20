package audiobooks

import (
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

package catalog

import "testing"

func TestNormalizeQuerySort_LegacyAliases(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
		order string
	}{
		{name: "sort_title", field: "sort_title", want: "title", order: "asc"},
		{name: "recently_added", field: "recently_added", want: "added_at", order: "desc"},
		{name: "rating", field: "rating", want: "rating_imdb", order: "desc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeQuerySort(QuerySort{Field: tt.field})
			if got.Field != tt.want {
				t.Fatalf("expected %q to normalize to %q, got %q", tt.field, tt.want, got.Field)
			}
			if got.Order != tt.order {
				t.Fatalf("expected default order %q, got %q", tt.order, got.Order)
			}
		})
	}
}

func TestQueryDefinitionValidationRejects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		def      QueryDefinition
		validate func(QueryDefinition) error
	}{
		{
			name: "ebook media scope narrator rule",
			def: QueryDefinition{
				MediaScope: "ebook",
				Match:      "all",
				Groups: []QueryGroup{{
					Match: "all",
					Rules: []QueryRule{{Field: "narrator", Op: "is", Value: "Narrator"}},
				}},
				Sort: QuerySort{Field: "title", Order: "asc"},
			},
			validate: QueryDefinition.Validate,
		},
		{
			name:     "ebook media scope narrator sort",
			def:      QueryDefinition{MediaScope: "ebook", Match: "all", Groups: []QueryGroup{}, Sort: QuerySort{Field: "narrator", Order: "asc"}},
			validate: QueryDefinition.Validate,
		},
		{
			name: "personalized sort without profile scope",
			def:  QueryDefinition{Match: "all", Groups: []QueryGroup{}, Sort: QuerySort{Field: "progress", Order: "desc"}},
			validate: func(qd QueryDefinition) error {
				return qd.ValidateWithSortScope(false)
			},
		},
		{
			name: "personalized rule without profile scope",
			def: QueryDefinition{
				Match: "all",
				Groups: []QueryGroup{{
					Match: "all",
					Rules: []QueryRule{{Field: "watched", Op: "is", Value: true}},
				}},
				Sort: QuerySort{Field: "added_at", Order: "desc"},
			},
			validate: func(qd QueryDefinition) error {
				return qd.ValidateWithOptions(true, false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.validate(tc.def); err == nil {
				t.Fatal("expected the definition to be rejected")
			}
		})
	}
}

// A smart collection needs a rule complete enough to filter; with none its
// query matches every title. Values arrive as decoded JSON.
func TestQueryDefinitionHasCompleteRule(t *testing.T) {
	rule := func(field, op string, value any) QueryDefinition {
		return QueryDefinition{Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: field, Op: op, Value: value}}}}}
	}
	for name, tc := range map[string]struct {
		definition QueryDefinition
		want       bool
	}{
		"no groups":             {definition: QueryDefinition{}, want: false},
		"a group with no rules": {definition: QueryDefinition{Groups: []QueryGroup{{Match: "all"}}}, want: false},
		"blank value":           {definition: rule("genre", "is", "  "), want: false},
		"missing value":         {definition: rule("genre", "is", nil), want: false},
		"open range":            {definition: rule("year", "between", []any{float64(1990), ""}), want: false},
		"empty range":           {definition: rule("year", "between", []any{}), want: false},
		"no field":              {definition: rule("", "is", "Comedy"), want: false},
		"no operator":           {definition: rule("genre", "", "Comedy"), want: false},
		"text":                  {definition: rule("genre", "is", "Comedy"), want: true},
		"zero":                  {definition: rule("year", "is", float64(0)), want: true},
		"false":                 {definition: rule("watched", "is", false), want: true},
		"range":                 {definition: rule("year", "between", []any{float64(1990), float64(1999)}), want: true},
		"one complete rule among blanks": {definition: QueryDefinition{Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "genre", Op: "is", Value: ""}}},
			{Match: "all", Rules: []QueryRule{{Field: "genre", Op: "is", Value: "Drama"}}},
		}}, want: true},
	} {
		if got := tc.definition.HasCompleteRule(); got != tc.want {
			t.Errorf("%s: HasCompleteRule() = %t, want %t", name, got, tc.want)
		}
	}
}

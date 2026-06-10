package catalog

import "testing"

func TestNormalizeQuerySort_LastAirDate(t *testing.T) {
	got := NormalizeQuerySort(QuerySort{Field: "last_air_date"})
	if got.Field != "last_air_date" {
		t.Fatalf("expected field last_air_date, got %q", got.Field)
	}
	if got.Order != "desc" {
		t.Fatalf("expected default order desc, got %q", got.Order)
	}
}

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

func TestValidate_LastAirDateSort(t *testing.T) {
	qd := QueryDefinition{
		Match:  "all",
		Groups: []QueryGroup{},
		Sort:   QuerySort{Field: "last_air_date", Order: "desc"},
	}
	if err := qd.Validate(); err != nil {
		t.Fatalf("expected last_air_date sort to be valid, got %v", err)
	}
}

func TestValidate_EpisodeMediaScope(t *testing.T) {
	qd := QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups:     []QueryGroup{},
		Sort:       QuerySort{Field: "title", Order: "asc"},
	}
	if err := qd.Validate(); err != nil {
		t.Fatalf("expected episode media scope to be valid, got %v", err)
	}
}

func TestValidate_AudiobookMediaScope(t *testing.T) {
	qd := QueryDefinition{
		MediaScope: "audiobook",
		Match:      "all",
		Groups:     []QueryGroup{},
		Sort:       QuerySort{Field: "title", Order: "asc"},
	}
	if err := qd.Validate(); err != nil {
		t.Fatalf("expected audiobook media scope to be valid, got %v", err)
	}
}

func TestValidate_EbookMediaScope(t *testing.T) {
	qd := QueryDefinition{
		MediaScope: "ebook",
		Match:      "all",
		Groups:     []QueryGroup{},
		Sort:       QuerySort{Field: "title", Order: "asc"},
	}
	if err := qd.Validate(); err != nil {
		t.Fatalf("expected ebook media scope to be valid, got %v", err)
	}
}

func TestValidate_EbookMediaScopeRejectsNarratorRule(t *testing.T) {
	qd := QueryDefinition{
		MediaScope: "ebook",
		Match:      "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{{Field: "narrator", Op: "is", Value: "Narrator"}},
		}},
		Sort: QuerySort{Field: "title", Order: "asc"},
	}
	if err := qd.Validate(); err == nil {
		t.Fatal("expected ebook narrator rule to be rejected")
	}
}

func TestValidate_EbookMediaScopeRejectsNarratorSort(t *testing.T) {
	qd := QueryDefinition{
		MediaScope: "ebook",
		Match:      "all",
		Groups:     []QueryGroup{},
		Sort:       QuerySort{Field: "narrator", Order: "asc"},
	}
	if err := qd.Validate(); err == nil {
		t.Fatal("expected ebook narrator sort to be rejected")
	}
}

func TestValidateWithSortScope_RejectsPersonalizedSortWithoutProfileScope(t *testing.T) {
	qd := QueryDefinition{
		Match:  "all",
		Groups: []QueryGroup{},
		Sort:   QuerySort{Field: "progress", Order: "desc"},
	}
	if err := qd.ValidateWithSortScope(false); err == nil {
		t.Fatal("expected personalized sort to be rejected without profile scope")
	}
}

func TestValidateWithOptions_RejectsPersonalizedRuleWithoutProfileScope(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{
				Match: "all",
				Rules: []QueryRule{
					{Field: "watched", Op: "is", Value: true},
				},
			},
		},
		Sort: QuerySort{Field: "added_at", Order: "desc"},
	}
	if err := qd.ValidateWithOptions(true, false); err == nil {
		t.Fatal("expected personalized field to be rejected without profile scope")
	}
}

func TestValidateWithOptions_AllowsPersonalizedRuleWithProfileScope(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{
				Match: "all",
				Rules: []QueryRule{
					{Field: "in_progress", Op: "is", Value: true},
				},
			},
		},
		Sort: QuerySort{Field: "progress", Order: "desc"},
	}
	if err := qd.ValidateWithOptions(true, true); err != nil {
		t.Fatalf("expected personalized field to be valid with profile scope, got %v", err)
	}
}

func TestQueryFieldRequiresProfile(t *testing.T) {
	if !QueryFieldRequiresProfile("watched") {
		t.Fatal("expected watched to require profile scope")
	}
	if QueryFieldRequiresProfile("actor") {
		t.Fatal("expected actor not to require profile scope")
	}
}

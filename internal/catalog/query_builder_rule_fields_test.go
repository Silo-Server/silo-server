package catalog

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuild_RelativeDateRulesRejectMalformedSpans(t *testing.T) {
	for _, field := range []string{"added_at", "release_date", "latest_episode_added", "last_air_date"} {
		_, _, err := NewQueryBuilder("mi").Build(QueryDefinition{
			Match:  "all",
			Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: field, Op: "not_in_last", Value: "1'; SELECT 1; --d"}}}},
		})
		if err == nil {
			t.Fatalf("%s: expected malformed span to be rejected", field)
		}
	}
}

func TestBuild_TitleRejectsNonStringValue(t *testing.T) {
	_, _, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match:  "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "title", Op: "contains", Value: 7.0}}}},
	})
	if err == nil {
		t.Fatal("expected a non-string title value to be rejected")
	}
}

func TestValidate_NewRuleOperatorsPerField(t *testing.T) {
	valid := []QueryRule{
		{Field: "title", Op: "begins_with", Value: "The"},
		{Field: "show_title", Op: "contains", Value: "Show"},
		{Field: "year", Op: "is_not", Value: 2020.0},
		{Field: "added_at", Op: "not_in_last", Value: "30d"},
		{Field: "last_watched", Op: "not_in_last", Value: "30d"},
		{Field: "runtime", Op: "between", Value: []any{80.0, 100.0}},
	}
	for _, rule := range valid {
		def := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{rule}}}}
		if err := def.Validate(); err != nil {
			t.Errorf("%s %s: unexpected error %v", rule.Field, rule.Op, err)
		}
	}
	invalid := []QueryRule{
		{Field: "genre", Op: "begins_with", Value: "Dra"},
		{Field: "title", Op: "in_last", Value: "30d"},
		{Field: "decade", Op: "gt", Value: 1990.0},
		{Field: "runtime", Op: "is", Value: 90.0},
		{Field: "watched", Op: "not_in_last", Value: "30d"},
	}
	for _, rule := range invalid {
		def := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{rule}}}}
		if err := def.Validate(); err == nil {
			t.Errorf("%s %s: expected the operator to be rejected", rule.Field, rule.Op)
		}
	}
}

func TestMalformedRuleValuesFailValidation(t *testing.T) {
	// A malformed value fails validation, so a catalog request answers 400
	// (422 on v2) and a save is refused, instead of the query failing with 500.
	for _, rule := range []QueryRule{
		{Field: "decade", Op: "is", Value: "NaN"},
		{Field: "runtime", Op: "lt", Value: ""},
		{Field: "rating_tmdb", Op: "between", Value: []any{"", ""}},
		{Field: "rating_rt_critic", Op: "gte", Value: "high"},
		{Field: "title", Op: "contains", Value: 3.0},
		{Field: "title", Op: "contains", Value: "  "},
		{Field: "show_title", Op: "contains", Value: "  "},
		{Field: "show_title", Op: "is", Value: 24.0},
		{Field: "decade", Op: "is", Value: 0.0},
		{Field: "decade", Op: "is", Value: 5.0},
		{Field: "release_date", Op: "not_in_last", Value: "soon"},
		{Field: "last_air_date", Op: "gt", Value: "not-a-date"},
		{Field: "last_air_date", Op: "lt", Value: "0000-01-01"},
		{Field: "release_date", Op: "not_in_last", Value: "2147483648d"},
		{Field: "added_at", Op: "in_last", Value: "7000y"},
		{Field: "added_at", Op: "in_last", Value: "70000000h"},
		{Field: "rating_imdb", Op: "gt", Value: "high"},
		{Field: "latest_episode_added", Op: "between", Value: []any{"2024-01-01", "soon"}},
		{Field: "watched", Op: "is", Value: "True"},
		{Field: "in_progress", Op: "is", Value: "1"},
		{Field: "favorited", Op: "is", Value: "yes"},
		{Field: "in_watchlist", Op: "is", Value: 1.0},
		{Field: "hdr", Op: "is", Value: " true"},
		{Field: "dolby_vision", Op: "is", Value: nil},
	} {
		def := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{rule}}}}
		if _, _, err := NewQueryBuilder("mi").Build(def); err == nil {
			t.Errorf("%s %s %v: expected the builder to reject the value before it reaches SQL", rule.Field, rule.Op, rule.Value)
		}
		if err := def.Validate(); err == nil {
			t.Errorf("%s %s %v: expected validation to reject the value", rule.Field, rule.Op, rule.Value)
		}
		if err := validateCatalogOverlayQuery(CatalogRequest{Query: def}, catalogQueryRuleFields, catalogQuerySortFields(), false); !errors.Is(err, ErrInvalidCatalogRequest) {
			t.Errorf("%s %s %v: expected an invalid-request error, got %v", rule.Field, rule.Op, rule.Value, err)
		}
	}

	valid := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{
		{Field: "runtime", Op: "between", Value: []any{90.0, 120.5}},
		{Field: "title", Op: "begins_with", Value: "the "},
		{Field: "decade", Op: "is", Value: 1990.0},
		{Field: "release_date", Op: "not_in_last", Value: "1y"},
		{Field: "added_at", Op: "in_last", Value: "6700y"},
		{Field: "added_at", Op: "in_last", Value: "3000000h"},
		{Field: "rating_imdb", Op: "between", Value: []any{"7", 9.5}},
		{Field: "last_air_date", Op: "between", Value: []any{"2024-01-01", "2024-12-31"}},
		{Field: "latest_episode_added", Op: "gt", Value: "2024-01-01T00:00:00Z"},
	}}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected well-formed values to validate, got %v", err)
	}
}

func TestBooleanRuleStringValuesBuildAsBooleans(t *testing.T) {
	// Released clients (Android TV v1.0.0-beta.27) send "true" and "false" as
	// JSON strings. They keep working, and build exactly as the real booleans.
	for _, field := range []string{"watched", "in_progress", "favorited", "in_watchlist", "hdr", "dolby_vision"} {
		for _, value := range []bool{true, false} {
			text := map[bool]string{true: "true", false: "false"}[value]
			for _, match := range []string{"all", "any"} {
				def := func(v any) QueryDefinition {
					return QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: match, Rules: []QueryRule{{Field: field, Op: "is", Value: v}}}}}
				}
				if err := def(text).Validate(); err != nil {
					t.Errorf("%s %q (%s): expected validation to accept it, got %v", field, text, match, err)
				}
				if err := validateCatalogOverlayQuery(CatalogRequest{Query: def(text)}, catalogPersonalRuleFields, catalogQuerySortFields(), false); err != nil {
					t.Errorf("%s %q (%s): expected the catalog request to validate, got %v", field, text, match, err)
				}
				gotSQL, gotArgs, err := NewQueryBuilder("mi").WithUserScope(7, "profile-1").Build(def(text))
				if err != nil {
					t.Errorf("%s %q (%s): expected the builder to accept it, got %v", field, text, match, err)
					continue
				}
				wantSQL, wantArgs, err := NewQueryBuilder("mi").WithUserScope(7, "profile-1").Build(def(value))
				if err != nil {
					t.Fatalf("%s %v (%s): %v", field, value, match, err)
				}
				if gotSQL != wantSQL || fmt.Sprintf("%#v", gotArgs) != fmt.Sprintf("%#v", wantArgs) {
					t.Errorf("%s %q (%s): built %q %#v, want the boolean form %q %#v", field, text, match, gotSQL, gotArgs, wantSQL, wantArgs)
				}
			}
		}
	}
}

func TestRelativeDurationCutoff_LongHourSpanDoesNotOverflow(t *testing.T) {
	// 3,000,000 hours is past what a time.Duration holds (about 292 years).
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	got := relativeDuration{amount: 3_000_000, unit: 'h'}.cutoffTime(now)
	if want := now.AddDate(0, 0, -125_000); !got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", got, want)
	}
}

func TestParseCatalogRequest_TitleValuesStayText(t *testing.T) {
	// A title that reads as a number or a bool is still text; other fields'
	// values are read as the scalars they look like.
	for _, field := range []string{"title", "show_title"} {
		for _, title := range []string{"1917", "true", "007"} {
			req, err := ParseCatalogRequestWithOptions(url.Values{
				"groups[0][rules][0][field]": {field},
				"groups[0][rules][0][op]":    {"is"},
				"groups[0][rules][0][value]": {title},
				"groups[0][rules][1][field]": {"year"},
				"groups[0][rules][1][op]":    {"is"},
				"groups[0][rules][1][value]": {"1917"},
			}, CatalogRequestOptions{ExtendedRules: true})
			if err != nil {
				t.Fatalf("ParseCatalogRequestWithOptions: %v", err)
			}
			rules := req.Query.Groups[0].Rules
			if rules[0].Value != title || rules[1].Value != 1917 {
				t.Fatalf("title %q: values = %#v, %#v", title, rules[0].Value, rules[1].Value)
			}
			if err := validateCatalogQueryRequest(req, false); err != nil {
				t.Fatalf("title %q: expected the rule to validate, got %v", title, err)
			}
		}
	}
}

func TestUserHistoryCTESQLRollsEpisodesUpToSeries(t *testing.T) {
	sql := UserHistoryCTESQL(1)
	for _, fragment := range []string{
		"WHERE uwh.user_id = $1 AND uwh.profile_id = $2 AND uwh.completed = TRUE",
		"LEFT JOIN episodes ep ON ep.content_id = src.media_item_id",
		"(VALUES (src.media_item_id), (ep.series_id)) AS watched(media_item_id)",
		"WHERE watched.media_item_id IS NOT NULL",
		// History removals name the episode, so hiding is checked before the rollup.
		"AND hhi.media_item_id = src.media_item_id",
		"GROUP BY watched.media_item_id",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected CTE to contain %q, got:\n%s", fragment, sql)
		}
	}
}

func TestBuildEpisodeCatalogEntryRuleWhere_NewFieldsFallBack(t *testing.T) {
	for _, field := range []string{"title", "decade", "runtime", "rating_tmdb", "last_air_date"} {
		_, _, _, ok, err := buildEpisodeCatalogEntryRuleWhere(QueryRule{Field: field, Op: "is", Value: "x"}, 1)
		if err != nil || ok {
			t.Fatalf("%s: expected the generic executor fallback (ok=false), got ok=%v err=%v", field, ok, err)
		}
	}
}

func TestCatalogRuleMatchesItem_NewFieldsAndOperators(t *testing.T) {
	rating := 7.8
	critic := 91
	oldRelease := time.Now().UTC().AddDate(-3, 0, 0).Format("2006-01-02")
	item := &models.MediaItem{
		Title:          "The Empire Strikes Back",
		Year:           1980,
		Runtime:        124,
		RatingTMDB:     &rating,
		RatingRTCritic: &critic,
		ReleaseDate:    &oldRelease,
		CreatedAt:      time.Now().AddDate(-2, 0, 0),
	}

	matches := []QueryRule{
		{Field: "title", Op: "contains", Value: "empire"},
		{Field: "title", Op: "not_contains", Value: "jedi"},
		{Field: "title", Op: "begins_with", Value: "the "},
		{Field: "title", Op: "ends_with", Value: "BACK"},
		{Field: "title", Op: "is", Value: " the empire strikes back "},
		{Field: "decade", Op: "is", Value: 1980.0},
		{Field: "decade", Op: "is_not", Value: 1990.0},
		{Field: "runtime", Op: "gt", Value: 120.0},
		{Field: "rating_tmdb", Op: "gte", Value: 7.5},
		{Field: "rating_rt_critic", Op: "between", Value: []any{90.0, 100.0}},
		{Field: "release_date", Op: "not_in_last", Value: "1y"},
		{Field: "added_at", Op: "not_in_last", Value: "1y"},
	}
	for _, rule := range matches {
		if !catalogRuleMatchesItem(item, rule) {
			t.Errorf("expected %s %s %v to match", rule.Field, rule.Op, rule.Value)
		}
	}

	misses := []QueryRule{
		{Field: "title", Op: "is_not", Value: "the empire strikes back"},
		{Field: "title", Op: "begins_with", Value: "empire"},
		{Field: "decade", Op: "is", Value: 1970.0},
		{Field: "runtime", Op: "lt", Value: 90.0},
		{Field: "rating_rt_audience", Op: "gt", Value: 0.0},
		{Field: "release_date", Op: "in_last", Value: "1y"},
		{Field: "added_at", Op: "in_last", Value: "1y"},
	}
	for _, rule := range misses {
		if catalogRuleMatchesItem(item, rule) {
			t.Errorf("expected %s %s %v not to match", rule.Field, rule.Op, rule.Value)
		}
	}

	// A title without the date matches neither relative operator.
	undated := &models.MediaItem{Title: "Undated"}
	for _, op := range []string{"in_last", "not_in_last"} {
		if catalogRuleMatchesItem(undated, QueryRule{Field: "release_date", Op: op, Value: "1y"}) {
			t.Errorf("expected undated release_date not to match %s", op)
		}
	}
	// An unknown runtime (0) matches no bound.
	if catalogRuleMatchesItem(undated, QueryRule{Field: "runtime", Op: "lt", Value: 90.0}) {
		t.Error("expected an unknown runtime not to match")
	}
}

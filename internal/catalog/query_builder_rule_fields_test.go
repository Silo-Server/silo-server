package catalog

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func buildSingleRule(t *testing.T, qb *QueryBuilder, rule QueryRule) (string, []any) {
	t.Helper()
	clause, args, err := qb.Build(QueryDefinition{
		Match:  "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{rule}}},
	})
	if err != nil {
		t.Fatalf("Build(%+v) returned error: %v", rule, err)
	}
	return clause, args
}

func TestBuild_DateRulesNotInLastComplementsInLast(t *testing.T) {
	tests := []struct {
		rule QueryRule
		want string
	}{
		{QueryRule{Field: "added_at", Op: "not_in_last", Value: "30d"}, "mi.created_at < NOW() - INTERVAL '30 days'"},
		{QueryRule{Field: "release_date", Op: "not_in_last", Value: "1y"}, "mi.release_date < (CURRENT_DATE - INTERVAL '1 years')::date"},
		{QueryRule{Field: "latest_episode_added", Op: "in_last", Value: "7d"}, "mi.latest_episode_added_at >= NOW() - INTERVAL '7 days'"},
		{QueryRule{Field: "latest_episode_added", Op: "not_in_last", Value: "7d"}, "mi.latest_episode_added_at < NOW() - INTERVAL '7 days'"},
		{QueryRule{Field: "last_air_date", Op: "in_last", Value: "2w"}, "mi.last_air_date_at >= (CURRENT_DATE - INTERVAL '2 weeks')::date"},
		{QueryRule{Field: "last_air_date", Op: "not_in_last", Value: "6m"}, "mi.last_air_date_at < (CURRENT_DATE - INTERVAL '6 months')::date"},
	}
	for _, tt := range tests {
		t.Run(tt.rule.Field+"_"+tt.rule.Op, func(t *testing.T) {
			clause, args := buildSingleRule(t, NewQueryBuilder("mi"), tt.rule)
			if len(args) != 0 {
				t.Fatalf("expected no args, got %v", args)
			}
			if !strings.Contains(clause, tt.want) {
				t.Fatalf("expected clause to contain %q, got %q", tt.want, clause)
			}
		})
	}
}

func TestBuild_DateRulesBindAbsoluteBounds(t *testing.T) {
	clause, args := buildSingleRule(t, NewQueryBuilder("mi"), QueryRule{Field: "latest_episode_added", Op: "lt", Value: "2026-01-01"})
	if clause != "(mi.latest_episode_added_at < $1::timestamptz)" {
		t.Fatalf("unexpected clause %q", clause)
	}
	if !reflect.DeepEqual(args, []any{"2026-01-01"}) {
		t.Fatalf("unexpected args %v", args)
	}

	clause, args = buildSingleRule(t, NewQueryBuilder("mi"), QueryRule{Field: "last_air_date", Op: "between", Value: []any{"2024-01-01", "2024-12-31"}})
	if clause != "(mi.last_air_date_at >= $1::date AND mi.last_air_date_at <= $2::date)" {
		t.Fatalf("unexpected clause %q", clause)
	}
	if len(args) != 2 {
		t.Fatalf("expected two args, got %v", args)
	}
}

func TestBuild_LastWatchedNotInLastKeepsNeverWatched(t *testing.T) {
	qb := NewQueryBuilder("mi").WithUserScope(1, "p1")
	clause, _ := buildSingleRule(t, qb, QueryRule{Field: "last_watched", Op: "not_in_last", Value: "30d"})
	want := "COALESCE(uhist.last_watched, '-infinity'::timestamptz) < NOW() - INTERVAL '30 days'"
	if !strings.Contains(clause, want) {
		t.Fatalf("expected %q, got %q", want, clause)
	}
	if !qb.RequiresUserHistoryCTE() {
		t.Fatal("last_watched must require the user history CTE")
	}
}

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

func TestBuild_TitleOperators(t *testing.T) {
	tests := []struct {
		op      string
		value   string
		want    string
		wantArg string
	}{
		{"is", " Alien ", "(LOWER(BTRIM(mi.title)) = LOWER($1))", "Alien"},
		{"is_not", "Alien", "(NOT (LOWER(BTRIM(mi.title)) = LOWER($1)))", "Alien"},
		{"contains", "star", `(mi.title ILIKE $1 ESCAPE '\')`, "%star%"},
		{"not_contains", "star", `(NOT (mi.title ILIKE $1 ESCAPE '\'))`, "%star%"},
		{"begins_with", "The", `(BTRIM(mi.title) ILIKE $1 ESCAPE '\')`, "The%"},
		{"ends_with", "II", `(BTRIM(mi.title) ILIKE $1 ESCAPE '\')`, "%II"},
		// LIKE wildcards in the value match literally.
		{"contains", `100%_off\`, `(mi.title ILIKE $1 ESCAPE '\')`, `%100\%\_off\\%`},
	}
	for _, tt := range tests {
		t.Run(tt.op+"_"+tt.value, func(t *testing.T) {
			clause, args := buildSingleRule(t, NewQueryBuilder("mi"), QueryRule{Field: "title", Op: tt.op, Value: tt.value})
			if clause != tt.want {
				t.Fatalf("clause = %q, want %q", clause, tt.want)
			}
			if !reflect.DeepEqual(args, []any{tt.wantArg}) {
				t.Fatalf("args = %v, want [%q]", args, tt.wantArg)
			}
		})
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

func TestBuild_DecadeMatchesTenYears(t *testing.T) {
	clause, args := buildSingleRule(t, NewQueryBuilder("mi"), QueryRule{Field: "decade", Op: "is", Value: 1995.0})
	if clause != "(mi.year >= $1 AND mi.year <= $2)" {
		t.Fatalf("unexpected clause %q", clause)
	}
	if !reflect.DeepEqual(args, []any{1990, 1999}) {
		t.Fatalf("args = %v, want [1990 1999]", args)
	}

	clause, _ = buildSingleRule(t, NewQueryBuilder("mi"), QueryRule{Field: "decade", Op: "is_not", Value: "1980"})
	if clause != "(NOT (mi.year >= $1 AND mi.year <= $2))" {
		t.Fatalf("unexpected is_not clause %q", clause)
	}

	_, _, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match:  "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "decade", Op: "is", Value: "eighties"}}}},
	})
	if err == nil {
		t.Fatal("expected a non-numeric decade to be rejected")
	}
}

func TestBuild_NumericRuleFieldsCastBounds(t *testing.T) {
	tests := []struct {
		rule QueryRule
		want string
	}{
		{QueryRule{Field: "runtime", Op: "lt", Value: 90.0}, "(NULLIF(mi.runtime, 0) < $1::numeric)"},
		{QueryRule{Field: "rating_tmdb", Op: "gte", Value: 7.5}, "(mi.rating_tmdb >= $1::numeric)"},
		{QueryRule{Field: "rating_rt_critic", Op: "gt", Value: 85.5}, "(mi.rating_rt_critic > $1::numeric)"},
		{QueryRule{Field: "rating_rt_audience", Op: "between", Value: []any{60.0, 90.0}}, "(mi.rating_rt_audience >= $1::numeric AND mi.rating_rt_audience <= $2::numeric)"},
	}
	for _, tt := range tests {
		t.Run(tt.rule.Field, func(t *testing.T) {
			clause, _ := buildSingleRule(t, NewQueryBuilder("mi"), tt.rule)
			if clause != tt.want {
				t.Fatalf("clause = %q, want %q", clause, tt.want)
			}
		})
	}
}

func TestValidate_NewRuleOperatorsPerField(t *testing.T) {
	valid := []QueryRule{
		{Field: "title", Op: "begins_with", Value: "The"},
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

func TestBuild_LatestEpisodeAddedMatchesNoEpisode(t *testing.T) {
	// The cross-type search evaluates an unscoped definition's rules against
	// episode rows, so the episode scope must accept the field and match nothing.
	qb := NewQueryBuilder("mi").WithMediaScope("episode")
	clause, _ := buildSingleRule(t, qb, QueryRule{Field: "latest_episode_added", Op: "in_last", Value: "7d"})
	if clause != "(NULL::timestamptz >= NOW() - INTERVAL '7 days')" {
		t.Fatalf("unexpected clause %q", clause)
	}
	_, _, err := NewQueryBuilder("mi").WithMediaScope("episode").Build(QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups:     []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "latest_episode_added", Op: "not_in_last", Value: "soon"}}}},
	})
	if err == nil {
		t.Fatal("expected a malformed span to be rejected in the episode scope too")
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
	for _, title := range []string{"1917", "true", "007"} {
		req, err := ParseCatalogRequestWithOptions(url.Values{
			"groups[0][rules][0][field]": {"title"},
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

func TestV1RequestsKeepTheFrozenRuleVocabulary(t *testing.T) {
	// /api/v1 refuses the rules /api/v2 added, with the messages it gave
	// before they existed; /api/v2 opts in and accepts them.
	for _, tc := range []struct {
		field, op, value string
		v1Error          string
		postError        string
	}{
		{"title", "contains", "x", `groups[0].rules[0].field "title" is not supported`, `groups[0].rules[0].field "title" is not supported`},
		{"release_date", "not_in_last", "1y", `groups[0].rules[0] is invalid`, `groups[0].rules[0].op "not_in_last" is not supported for field "release_date"`},
	} {
		values := url.Values{
			"q":                          {"dune"},
			"groups[0][rules][0][field]": {tc.field},
			"groups[0][rules][0][op]":    {tc.op},
			"groups[0][rules][0][value]": {tc.value},
		}
		v1, err := ParseCatalogRequest(values)
		if err != nil {
			t.Fatalf("ParseCatalogRequest: %v", err)
		}
		if err := validateCatalogQueryRequest(v1, false); !errors.Is(err, ErrInvalidCatalogRequest) || !strings.Contains(err.Error(), tc.v1Error) {
			t.Errorf("v1 %s %s: got %v, want an invalid request naming %q", tc.field, tc.op, err, tc.v1Error)
		}
		v2, err := ParseCatalogRequestWithOptions(values, CatalogRequestOptions{ExtendedRules: true})
		if err != nil {
			t.Fatalf("ParseCatalogRequestWithOptions: %v", err)
		}
		if err := validateCatalogQueryRequest(v2, false); err != nil {
			t.Errorf("v2 %s %s: expected the rule to be accepted, got %v", tc.field, tc.op, err)
		}
		// POST /api/v1/catalog/query validates its body with ValidateV1Rules.
		if err := ValidateV1Rules(v1.Query); err == nil || err.Error() != tc.postError {
			t.Errorf("ValidateV1Rules %s %s: got %v, want %q", tc.field, tc.op, err, tc.postError)
		}
	}
	if err := ValidateV1Rules(QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "release_date", Op: "in_last", Value: "1y"}}}}}); err != nil {
		t.Errorf("expected a v1 rule to pass, got %v", err)
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

func TestBuildEpisodeCatalogComparisonClause_NotInLast(t *testing.T) {
	clause, args, next, ok, err := buildEpisodeCatalogComparisonClause("ece.episode_air_date", QueryRule{Field: "release_date", Op: "not_in_last", Value: "1y"}, 3, "date")
	if err != nil || !ok {
		t.Fatalf("unexpected result ok=%v err=%v", ok, err)
	}
	if clause != "ece.episode_air_date < (CURRENT_DATE - INTERVAL '1 years')::date" {
		t.Fatalf("unexpected clause %q", clause)
	}
	if len(args) != 0 || next != 3 {
		t.Fatalf("expected no args and an unchanged index, got %v, %d", args, next)
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

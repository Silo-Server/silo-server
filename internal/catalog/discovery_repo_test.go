package catalog

// discovery_repo_test.go exercises the SQL generation helpers in
// discovery_repo.go using the same pure-unit approach used throughout this
// package: build a query string and assert that key fragments are present,
// without needing a live database.

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// The viewer's allowed and disabled libraries reach every discovery query as
// independent EXISTS / NOT EXISTS predicates, bound after the query's own args.
func TestDiscoveryQueries_LibraryAccessPredicates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		build     func() (string, []any)
		predicate string
		anyClause string
		wantArgs  int
		ids       []int
	}{
		{
			name: "rating threshold allowed",
			build: func() (string, []any) {
				return buildRatingThresholdQuery(RatingFilter{Min: 7.0, Filter: AccessFilter{AllowedLibraryIDs: []int{1, 2}}})
			},
			predicate: "EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in",
			anyClause: "mil_scope_in.media_folder_id = ANY($3)",
			wantArgs:  3,
			ids:       []int{1, 2},
		},
		{
			name: "rating threshold disabled uses item-level exclusion",
			build: func() (string, []any) {
				return buildRatingThresholdQuery(RatingFilter{Min: 7.0, Filter: AccessFilter{DisabledLibraryIDs: []int{99}}})
			},
			predicate: "NOT EXISTS (SELECT 1 FROM media_item_libraries mil_scope_out",
			anyClause: "mil_scope_out.media_folder_id = ANY($3)",
			wantArgs:  3,
			ids:       []int{99},
		},
		{
			name: "unplayed high rated allowed",
			build: func() (string, []any) {
				return buildUnplayedHighRatedQuery(UnplayedFilter{MinRating: 7.0, UserID: 3, ProfileID: "p",
					Filter: AccessFilter{AllowedLibraryIDs: []int{10, 11}}})
			},
			predicate: "EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in",
			anyClause: "mil_scope_in.media_folder_id = ANY($5)",
			wantArgs:  5,
			ids:       []int{10, 11},
		},
		{
			name: "forgotten favorites allowed",
			build: func() (string, []any) {
				return buildForgottenFavoritesQuery(ForgottenFavoritesFilter{LookbackDays: 180, UserID: 3, ProfileID: "p",
					Filter: AccessFilter{AllowedLibraryIDs: []int{10, 11}}})
			},
			predicate: "EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in",
			anyClause: "mil_scope_in.media_folder_id = ANY($6)",
			wantArgs:  6,
			ids:       []int{10, 11},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, args := tc.build()
			assertNoLibraryJoin(t, query)
			if !strings.Contains(query, tc.predicate) {
				t.Fatalf("expected library predicate %q, got:\n%s", tc.predicate, query)
			}
			if !strings.Contains(query, tc.anyClause) {
				t.Fatalf("expected library ANY clause %q, got:\n%s", tc.anyClause, query)
			}
			if len(args) != tc.wantArgs {
				t.Fatalf("expected %d args, got %d: %v", tc.wantArgs, len(args), args)
			}
			assertIntSliceArg(t, args, tc.wantArgs-1, tc.ids)
		})
	}
}

// A viewer with no reachable library gets no query at all.
func TestDiscoveryQueries_EmptyLibraryAccessReturnsEmptyQuery(t *testing.T) {
	lib := 5
	for name, build := range map[string]func() (string, []any){
		"rating threshold": func() (string, []any) {
			return buildRatingThresholdQuery(RatingFilter{Min: 7.0, Filter: AccessFilter{AllowedLibraryIDs: []int{}}})
		},
		"unplayed high rated": func() (string, []any) {
			return buildUnplayedHighRatedQuery(UnplayedFilter{MinRating: 7.0, UserID: 1, ProfileID: "p",
				Filter: AccessFilter{AllowedLibraryIDs: []int{}}})
		},
		"forgotten favorites": func() (string, []any) {
			return buildForgottenFavoritesQuery(ForgottenFavoritesFilter{LookbackDays: 365, UserID: 1, ProfileID: "p",
				Filter: AccessFilter{AllowedLibraryIDs: []int{}}})
		},
		// Section scope {5} ∩ viewer-allowed {7,9} is empty.
		"unplayed high rated section scope outside viewer access": func() (string, []any) {
			return buildUnplayedHighRatedQuery(UnplayedFilter{MinRating: 7.5, UserID: 1, ProfileID: "p1", LibraryID: &lib,
				Filter: AccessFilter{AllowedLibraryIDs: []int{7, 9}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if query, _ := build(); query != "" {
				t.Fatalf("expected empty query, got %q", query)
			}
		})
	}
}

// Every discovery query honors the filter's content allow-list and name
// prefix, as the query executor does.
func TestDiscoveryQueries_HonorContentScope(t *testing.T) {
	filter := AccessFilter{AllowedContentIDs: []string{"movie:1", "series:2"}, NamePrefix: " The_ "}
	builders := map[string]func() (string, []any){
		"rating threshold": func() (string, []any) {
			return buildRatingThresholdQuery(RatingFilter{Min: 7, Filter: filter})
		},
		"unplayed high rated": func() (string, []any) {
			return buildUnplayedHighRatedQuery(UnplayedFilter{MinRating: 7, UserID: 1, ProfileID: "p", Filter: filter})
		},
		"forgotten favorites": func() (string, []any) {
			return buildForgottenFavoritesQuery(ForgottenFavoritesFilter{LookbackDays: 365, UserID: 1, ProfileID: "p", Filter: filter})
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			query, args := build()
			if !strings.Contains(query, "mi.content_id = ANY($") {
				t.Fatalf("expected the content allow-list, got:\n%s", query)
			}
			if !strings.Contains(query, sortTitleKeyExpr+" LIKE $") {
				t.Fatalf("expected the sort-title prefix, got:\n%s", query)
			}
			var sawIDs, sawPrefix bool
			for _, arg := range args {
				switch v := arg.(type) {
				case []string:
					sawIDs = len(v) == 2 && v[0] == "movie:1" && v[1] == "series:2"
				case string:
					sawPrefix = sawPrefix || v == `the\_%`
				}
			}
			if !sawIDs || !sawPrefix {
				t.Fatalf("args = %v, want the allowed IDs and the escaped, lowercased prefix", args)
			}
		})
	}
}

func TestRatingThreshold_EmptyContentAllowListMatchesNothing(t *testing.T) {
	query, _ := buildRatingThresholdQuery(RatingFilter{
		Min:    7.0,
		Filter: AccessFilter{AllowedContentIDs: []string{}},
	})
	if !strings.Contains(query, "1 = 0") {
		t.Fatalf("expected an empty allow-list to match nothing, got:\n%s", query)
	}
}

// ---------------------------------------------------------------------------
// ListUnplayedHighRated SQL generation tests
// ---------------------------------------------------------------------------

func TestUnplayedHighRated_BasicQuery(t *testing.T) {
	query, args := buildUnplayedHighRatedQuery(UnplayedFilter{
		MinRating: 7.5,
		Limit:     25,
		UserID:    7,
		ProfileID: "profile-abc",
	})

	assertQualifiedItemSelect(t, query)
	if !strings.Contains(query, "mi.tmdb_vote_average >= $1") {
		t.Fatalf("expected rating threshold predicate, got:\n%s", query)
	}
	if !strings.Contains(query, "NOT EXISTS") {
		t.Fatalf("expected NOT EXISTS subquery, got:\n%s", query)
	}
	if !strings.Contains(query, "user_watch_history uwh") {
		t.Fatalf("expected user_watch_history in subquery, got:\n%s", query)
	}
	if !strings.Contains(query, "uwh.user_id = $3") {
		t.Fatalf("expected user_id predicate at $3, got:\n%s", query)
	}
	if !strings.Contains(query, "uwh.profile_id = $4") {
		t.Fatalf("expected profile_id predicate at $4, got:\n%s", query)
	}
	if !strings.Contains(query, "uwh.media_item_id = mi.content_id") {
		t.Fatalf("expected media_item_id = mi.content_id correlation, got:\n%s", query)
	}
	assertWeightedRatingOrder(t, query)
	if !strings.Contains(query, "LIMIT $5") {
		t.Fatalf("expected LIMIT at $5, got:\n%s", query)
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 args (minRating, minVotes, userID, profileID, limit), got %d: %v", len(args), args)
	}
}

func TestUnplayedHighRated_ContentRatingFilter(t *testing.T) {
	query, args := buildUnplayedHighRatedQuery(UnplayedFilter{
		MinRating: 6.0,
		UserID:    2,
		ProfileID: "p",
		Filter:    AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}},
	})

	if !strings.Contains(query, "mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $") {
		t.Fatalf("expected stored-age ceiling filter, got:\n%s", query)
	}
	// args: minRating, minVotes, userID, profileID, then the ceiling age (one arg).
	if len(args) != 5 {
		t.Fatalf("expected exactly 5 args (minRating, minVotes, userID, profileID, ceiling age); got %d: %v", len(args), args)
	}
}

// ---------------------------------------------------------------------------
// ListForgottenFavorites SQL generation tests
// ---------------------------------------------------------------------------

func TestForgottenFavorites_BasicQuery(t *testing.T) {
	query, args := buildForgottenFavoritesQuery(ForgottenFavoritesFilter{
		LookbackDays: 365,
		Limit:        20,
		UserID:       5,
		ProfileID:    "profile-xyz",
	})

	assertQualifiedItemSelect(t, query)
	if !strings.Contains(query, "mi.tmdb_vote_average >= $1 AND mi.tmdb_vote_count >= $2") || args[0] != 7.0 {
		t.Fatalf("expected 7.0 rating floor and vote minimum, got:\n%s\nargs %v", query, args)
	}
	if !strings.Contains(query, "NOT EXISTS") {
		t.Fatalf("expected NOT EXISTS subquery, got:\n%s", query)
	}
	if !strings.Contains(query, "user_watch_history uwh") {
		t.Fatalf("expected user_watch_history in subquery, got:\n%s", query)
	}
	if !strings.Contains(query, "uwh.user_id = $3") {
		t.Fatalf("expected user_id predicate at $3, got:\n%s", query)
	}
	if !strings.Contains(query, "uwh.profile_id = $4") {
		t.Fatalf("expected profile_id predicate at $4, got:\n%s", query)
	}
	if !strings.Contains(query, "uwh.watched_at >= NOW()") {
		t.Fatalf("expected watched_at recency filter, got:\n%s", query)
	}
	if !strings.Contains(query, "make_interval(days => $5)") {
		t.Fatalf("expected lookback_days at $5, got:\n%s", query)
	}
	assertWeightedRatingOrder(t, query)
	if !strings.Contains(query, "LIMIT $6") {
		t.Fatalf("expected LIMIT at $6, got:\n%s", query)
	}
	if len(args) != 6 {
		t.Fatalf("expected 6 args (minRating, minVotes, userID, profileID, lookbackDays, limit), got %d: %v", len(args), args)
	}
	// The lookback must reach Postgres as an integer. This assertion is the
	// one that was missing: the query text was checked but never executed, so
	// `($3 || ' days')::interval` -- which makes Postgres infer $3 as text and
	// then fails to encode the int at runtime -- passed review and shipped.
	if _, ok := args[4].(int); !ok {
		t.Fatalf("expected lookback_days arg to be an int, got %T (%v)", args[4], args[4])
	}
}

func TestDiscoveryQueries_DisabledLibrariesUseNotExists(t *testing.T) {
	filter := AccessFilter{DisabledLibraryIDs: []int{9}}

	cases := []struct {
		name  string
		query string
	}{
		{"rating_threshold", mustQuery(buildRatingThresholdQuery(RatingFilter{Min: 7, Filter: filter}))},
		{"unplayed_high_rated", mustQuery(buildUnplayedHighRatedQuery(UnplayedFilter{MinRating: 7, UserID: 1, ProfileID: "p", Filter: filter}))},
		{"forgotten_favorites", mustQuery(buildForgottenFavoritesQuery(ForgottenFavoritesFilter{UserID: 1, ProfileID: "p", LookbackDays: 365, Filter: filter}))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertNoLibraryJoin(t, tc.query)
			assertDenyOnlyRequiresLibraryMembership(t, tc.query)
			if !strings.Contains(tc.query, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil_scope_out") {
				t.Fatalf("expected disabled library NOT EXISTS predicate, got:\n%s", tc.query)
			}
		})
	}
}

func assertWeightedRatingOrder(t *testing.T, query string) {
	t.Helper()
	if !strings.Contains(query, "ORDER BY "+TMDBWeightedRatingSQL("mi")+" DESC NULLS LAST, mi.content_id ASC") {
		t.Fatalf("expected vote-weighted TMDB order, got:\n%s", query)
	}
}

func assertQualifiedItemSelect(t *testing.T, query string) {
	t.Helper()
	if !strings.HasPrefix(query, "SELECT mi.content_id,") {
		t.Fatalf("SELECT list is not qualified with the mi alias, got:\n%s", query)
	}
}

func assertNoLibraryJoin(t *testing.T, query string) {
	t.Helper()
	if strings.Contains(query, "JOIN media_item_libraries") {
		t.Fatalf("plain library JOIN can fan out discovery rows, got:\n%s", query)
	}
}

func assertDenyOnlyRequiresLibraryMembership(t *testing.T, query string) {
	t.Helper()
	membershipPredicate := "EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in WHERE mil_scope_in.content_id = mi.content_id)"
	if !strings.Contains(query, membershipPredicate) {
		t.Fatalf("deny-only library filters must require positive library membership, got:\n%s", query)
	}
	if strings.Contains(query, "mil_scope_in.media_folder_id") {
		t.Fatalf("positive membership predicate should not bind a specific library, got:\n%s", query)
	}
}

func assertIntSliceArg(t *testing.T, args []any, idx int, want []int) {
	t.Helper()
	if idx >= len(args) {
		t.Fatalf("missing arg %d in %v", idx, args)
	}
	got, ok := args[idx].([]int)
	if !ok {
		t.Fatalf("arg %d has type %T, want []int", idx, args[idx])
	}
	if len(got) != len(want) {
		t.Fatalf("arg %d = %v, want %v", idx, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %v, want %v", idx, got, want)
		}
	}
}

func mustQuery(query string, _ []any) string { return query }

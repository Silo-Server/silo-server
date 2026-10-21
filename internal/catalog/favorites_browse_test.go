package catalog

import (
	"errors"
	"strings"
	"testing"
)

// TestBuildBrowseFavoritesSQL_JoinsUserFavorites pins the SQL shape emitted by
// buildBrowseFavoritesPlan. The plan must JOIN user_favorites with media_items
// so type/library/sort filters and pagination push into a single round trip
// (audit 2026-05-01 §3.6 / catalog SQL plan task 4.2).
func TestBuildBrowseFavoritesSQL_JoinsUserFavorites(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:            7,
		ProfileID:         "p1",
		ItemType:          "movie",
		AllowedLibraryIDs: []int{1, 2},
		Limit:             20,
		Offset:            0,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan: %v", err)
	}

	sql, _ := plan.pagedSQL()

	if !strings.Contains(sql, "FROM user_favorites uf") {
		t.Fatalf("expected user_favorites in FROM; got %s", sql)
	}
	if !strings.Contains(sql, "JOIN media_items mi ON mi.content_id = uf.media_item_id") {
		t.Fatalf("expected JOIN to media_items; got %s", sql)
	}
	if !strings.Contains(sql, "uf.user_id = $") {
		t.Fatalf("expected user_id filter as bound param; got %s", sql)
	}
	if !strings.Contains(sql, "uf.profile_id = $") {
		t.Fatalf("expected profile_id filter as bound param; got %s", sql)
	}
	if !strings.Contains(sql, "mi.type = $") {
		t.Fatalf("expected single-type filter as bound param; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($") {
		t.Fatalf("expected library filter via ANY; got %s", sql)
	}
	if !strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("expected window count; got %s", sql)
	}
	if !strings.Contains(sql, "ORDER BY uf.added_at") {
		t.Fatalf("expected default sort by uf.added_at; got %s", sql)
	}
}

func TestBuildBrowseFavoritesSQL_MultiTypeUsesIN(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:    1,
		ProfileID: "p1",
		ItemType:  "movie,series",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan: %v", err)
	}
	sql, _ := plan.pagedSQL()
	if !strings.Contains(sql, "mi.type = ANY($") {
		t.Fatalf("expected multi-type = ANY clause; got %s", sql)
	}
}

func TestBuildBrowseFavoritesSQL_TitleSortUsesMediaItemsColumns(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:    1,
		ProfileID: "p1",
		SortField: "sort_title",
		SortOrder: "asc",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan: %v", err)
	}
	sql, _ := plan.pagedSQL()
	if !strings.Contains(sql, "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title))") {
		t.Fatalf("expected sort_title to fall back to mi.title; got %s", sql)
	}
	if !strings.Contains(sql, "ASC") {
		t.Fatalf("expected ASC direction; got %s", sql)
	}
}

func TestBuildBrowseFavoritesSQL_DisabledLibrariesNotExists(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:             1,
		ProfileID:          "p1",
		DisabledLibraryIDs: []int{99},
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan: %v", err)
	}
	sql, _ := plan.pagedSQL()
	if !strings.Contains(sql, "NOT EXISTS (") {
		t.Fatalf("expected NOT EXISTS for disabled libraries; got %s", sql)
	}
}

func TestBuildBrowseFavoritesSQL_EmptyAllowedLibrariesEarlyEmpty(t *testing.T) {
	_, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:            1,
		ProfileID:         "p1",
		AllowedLibraryIDs: []int{},
		Limit:             10,
	})
	if err == nil {
		t.Fatalf("expected error or sentinel for empty allowed libraries; got nil")
	}
	if !errors.Is(err, errBrowseFavoritesEmpty) {
		t.Fatalf("expected errBrowseFavoritesEmpty; got %v", err)
	}
}

// TestBuildBrowseFavoritesPlan_LibraryIDIntersectsAllowedLibraries pins that a
// client request for ParentId=99 against an allowlist that does NOT contain 99
// emits BOTH predicates (LibraryID + AllowedLibraryIDs). The AND combination
// is what enforces the intersection: if a future refactor drops one
// predicate, the request would leak data from libraries the viewer cannot
// reach. (code review on 614e45ac, security-adjacent gap.)
func TestBuildBrowseFavoritesPlan_LibraryIDIntersectsAllowedLibraries(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:            1,
		ProfileID:         "p1",
		LibraryID:         99,          // user is asking for a library...
		AllowedLibraryIDs: []int{1, 2}, // ...that's not in their allowlist
		Limit:             20,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan error: %v", err)
	}
	sql, args := plan.pagedSQL()

	// The LibraryID predicate goes through media_folder_id = $N (bound).
	if !strings.Contains(sql, "mil_one.media_folder_id = $") {
		t.Fatalf("expected LibraryID predicate (mil_one.media_folder_id = $N); got %s", sql)
	}
	// The AllowedLibraryIDs predicate goes through media_folder_id = ANY($N).
	if !strings.Contains(sql, "mil.media_folder_id = ANY($") {
		t.Fatalf("expected AllowedLibraryIDs predicate (mil.media_folder_id = ANY($N)); got %s", sql)
	}
	// Belt-and-suspenders: both predicates must be present, so the unqualified
	// substring should appear at least twice (once per EXISTS).
	occurrences := strings.Count(sql, "media_folder_id")
	if occurrences < 2 {
		t.Fatalf("expected >= 2 media_folder_id predicates (LibraryID + AllowedLibraryIDs); got %d in:\n%s", occurrences, sql)
	}
	// Both args must be bound. LibraryID is int=99; AllowedLibraryIDs is []int{1,2}.
	foundLibraryID, foundAllowed := false, false
	for _, a := range args {
		if v, ok := a.(int); ok && v == 99 {
			foundLibraryID = true
		}
		if v, ok := a.([]int); ok && len(v) == 2 && v[0] == 1 && v[1] == 2 {
			foundAllowed = true
		}
	}
	if !foundLibraryID {
		t.Fatalf("expected LibraryID=99 in bound args; got %v", args)
	}
	if !foundAllowed {
		t.Fatalf("expected AllowedLibraryIDs=[1 2] in bound args; got %v", args)
	}
}

// TestBuildBrowseFavoritesPlan_NamePrefixSortKeyLike pins the sort-key
// LIKE shape that powers /Items?Filters=IsFavorite&NameStartsWith=Star. The
// pattern must be anchored to the prefix only (no leading wildcard). (code
// review on 614e45ac, formerly uncovered.)
func TestBuildBrowseFavoritesPlan_NamePrefixSortKeyLike(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:     1,
		ProfileID:  "p1",
		NamePrefix: "Star",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan error: %v", err)
	}
	sql, args := plan.pagedSQL()
	if !strings.Contains(sql, "LIKE") {
		t.Fatalf("expected LIKE for NamePrefix; got %s", sql)
	}
	// The sort-key expression falls back to title when sort_title is empty.
	// Matching LOWER(title) too would list "The Hobbit" under T as well as H.
	if !strings.Contains(sql, "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE") {
		t.Fatalf("expected LIKE on the title sort key; got %s", sql)
	}
	if strings.Contains(sql, "LOWER(mi.title) LIKE") {
		t.Fatalf("prefix must not match the raw title alongside the sort key; got %s", sql)
	}
	// Pattern must escape backslash to defuse user-supplied %/_ wildcards.
	if !strings.Contains(sql, "ESCAPE '\\'") {
		t.Fatalf("expected ESCAPE '\\\\' clause; got %s", sql)
	}
	// Verify the bound pattern is "star%" — anchored prefix, lowercased, no
	// leading wildcard. A leading "%" would let users substring-search the
	// whole catalog.
	foundPrefixArg := false
	for _, a := range args {
		s, ok := a.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, "%") {
			t.Fatalf("NamePrefix arg must not have leading wildcard; got %q", s)
		}
		if strings.HasPrefix(strings.ToLower(s), "star") && strings.HasSuffix(s, "%") {
			foundPrefixArg = true
		}
	}
	if !foundPrefixArg {
		t.Fatalf("expected 'star%%'-style arg in args slice; got %v", args)
	}
}

// TestBuildBrowseFavoritesPlan_NormalizesNegativeOffset pins that
// buildBrowseFavoritesPlan floors a caller-supplied negative Offset to 0
// in the returned plan. BrowseFavorites computes HasMore from plan.offset
// (not the caller-supplied filters.Offset) so a negative offset cannot
// poison the HasMore arithmetic — without this normalization a caller
// passing Offset=-5 would see HasMore = total > -5 + len(items), which
// is true essentially always and would lie to clients about there being
// more pages when the entire result set was already returned.
//
// Regression guard for the post-perf-overhaul code review (Cursor low).
func TestBuildBrowseFavoritesPlan_NormalizesNegativeOffset(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:    1,
		ProfileID: "p1",
		Limit:     20,
		Offset:    -5,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan error: %v", err)
	}
	if plan.offset != 0 {
		t.Fatalf("expected negative offset to normalize to 0 in plan; got %d", plan.offset)
	}
	// pagedSQL binds plan.offset as the OFFSET argument.
	_, args := plan.pagedSQL()
	if len(args) < 2 {
		t.Fatalf("expected at least limit + offset args; got %v", args)
	}
	offsetArg := args[len(args)-1]
	if offsetArg != 0 {
		t.Fatalf("expected OFFSET arg to be normalized 0; got %v", offsetArg)
	}
}

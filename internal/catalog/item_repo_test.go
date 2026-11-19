package catalog

import (
	"context"
	"errors"

	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestItemRepo_GetByIDsWithAccess_SingleQueryWithLibraryFilter verifies that
// the SQL emitted by buildGetByIDsWithAccessSQL pushes the library access
// constraint into the same query as the content_id batch lookup, replacing
// the per-item EnsureAccessible fan-out called out in the catalog SQL audit
// (2026-05-01 §3.3).
func TestItemRepo_GetByIDsWithAccess_SingleQueryWithLibraryFilter(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a", "b"}, AccessFilter{
		AllowedLibraryIDs: []int{1, 2},
	}, itemAccessConditions)
	if !strings.Contains(sql, "content_id = ANY($1)") {
		t.Fatalf("expected ANY for content IDs; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected library access pushed into JOIN/WHERE; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args; got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_DisabledOnlyRequiresLibraryMembership pins
// the fix for the disabled-only access path: when AllowedLibraryIDs is nil
// and only DisabledLibraryIDs is set, the SQL must additionally require
// positive library membership (an EXISTS over media_item_libraries with
// no media_folder_id filter). Otherwise orphan items (rows in media_items
// with no media_item_libraries link — mid-scan, stale rows from a removed
// library, or metadata-refresh inserts not yet linked) would pass the
// NOT EXISTS — which is true over an empty subquery — and become visible
// to users restricted by DisabledLibraryIDs. EnsureAccessible's prior
// INNER JOIN on media_item_libraries enforced this implicitly.
//
// Regression guard for Codex P2 follow-up to PR #42.
func TestItemRepo_GetByIDsWithAccess_DisabledOnlyRequiresLibraryMembership(t *testing.T) {
	repo := &ItemRepository{}
	sql, _ := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		DisabledLibraryIDs: []int{9},
	}, itemAccessConditions)
	// Positive-membership EXISTS must be present. It has no media_folder_id
	// filter so the NOT EXISTS that follows is what enforces the disabled-list.
	if strings.Count(sql, "EXISTS (") < 2 {
		t.Fatalf("expected both a membership EXISTS and a disabled NOT EXISTS clause; got %s", sql)
	}
	// Pin the membership predicate's specific shape: an EXISTS against
	// media_item_libraries that does NOT bind a media_folder_id ANY(...) arg.
	// (The disabled NOT EXISTS does bind one — we want the membership EXISTS
	// to be argument-free so it doesn't reorder placeholder indices.)
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
		t.Fatalf("expected argument-free membership EXISTS over media_item_libraries; got %s", sql)
	}
}

// TestItemRepo_EnsureAccessibleSQL_DisabledOnlyRequiresMembership mirrors the
// GetByIDsWithAccess orphan-item guard for the per-item path: disabled-only
// scopes still require positive library membership.
func TestItemRepo_EnsureAccessibleSQL_DisabledOnlyRequiresMembership(t *testing.T) {
	sql, args := buildEnsureAccessibleSQL("item-1", AccessFilter{
		DisabledLibraryIDs: []int{9},
	})
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
		t.Fatalf("expected argument-free membership EXISTS; got %s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($2))") {
		t.Fatalf("expected disabled-library NOT EXISTS bound at $2; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args (id, disabled); got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_MaxContentRatingProducesINClause pins the
// maturity-ceiling branch: a comparison against the stored minimum age, with
// the ceiling's own age bound as a single arg.
func TestItemRepo_GetByIDsWithAccess_MaxContentRatingProducesINClause(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"},
	}, itemAccessConditions)
	if !strings.Contains(sql, "mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $") {
		t.Fatalf("expected stored-age ceiling clause; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids + ceiling age); got %v", args)
	}
	if args[1] != 14 {
		t.Fatalf("expected the PG-13 ceiling to bind age 14, the top of its US tier; got %v", args[1])
	}
}

// TestItemRepo_GetByIDsWithAccess_CombinedClausesIndexCorrectly verifies that
// when AllowedLibraryIDs, DisabledLibraryIDs, and MaxContentRating are all
// set, placeholder indices advance in the documented order: $1 = ids,
// $2 = allowed libs, $3 = disabled libs, $4 = rating ladder.
func TestItemRepo_GetByIDsWithAccess_CombinedClausesIndexCorrectly(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		AllowedLibraryIDs:  []int{1, 2},
		DisabledLibraryIDs: []int{9},
		MaturityLimits:     access.MaturityLimits{MaxContentRating: "PG-13"},
	}, itemAccessConditions)
	// Expect: $1 = ids, $2 = allowed libs, $3 = disabled libs, $4 = ceiling age.
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected AllowedLibraryIDs at $2; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($3)") {
		t.Fatalf("expected DisabledLibraryIDs at $3; got %s", sql)
	}
	if !strings.Contains(sql, "mi.content_rating_age <= $4") {
		t.Fatalf("expected the ceiling age at $4; got %s", sql)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args (ids, allowed, disabled, ceiling age); got %v", args)
	}
}

// TestItemRepo_Search_LibraryScopeUsesIndependentExistsPredicates pins the
// leak-safe library scoping shared by the FTS search and the fuzzy fallback via
// appendSearchScopeFilters. The prior JOIN + NOT(mil.media_folder_id = ANY(...))
// form let an item linked to BOTH a disabled and a non-disabled library survive
// the deny check on the passing membership row, so a typo (fuzzy) or exact
// search could surface items from a disabled library. Both builders must instead
// emit independent EXISTS/NOT EXISTS subqueries and no membership JOIN.
func TestItemRepo_Search_LibraryScopeUsesIndependentExistsPredicates(t *testing.T) {
	repo := &ItemRepository{}
	filter := AccessFilter{DisabledLibraryIDs: []int{9}}

	ftsSQL, _, _ := repo.buildSearchSQL("avatar", []string{"movie"}, 20, 0, filter)
	fuzzySQL, _, _ := repo.buildFuzzySearchSQL("avatar", []string{"movie"}, 20, 0, filter, true, nil, 0)

	for name, sql := range map[string]string{"fts": ftsSQL, "fuzzy": fuzzySQL} {
		if strings.Contains(sql, "JOIN media_item_libraries") {
			t.Fatalf("%s query must not JOIN media_item_libraries for scoping; got:\n%s", name, sql)
		}
		// Disabled-only path requires positive membership (argument-free EXISTS)
		// so orphan items don't slip through the vacuous NOT EXISTS...
		if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
			t.Fatalf("%s query must require library membership via an argument-free EXISTS; got:\n%s", name, sql)
		}
		// ...plus the disabled NOT EXISTS.
		if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($") {
			t.Fatalf("%s query must exclude disabled libraries via NOT EXISTS; got:\n%s", name, sql)
		}
	}
}

func TestItemRepo_Search_EpisodeAccessUsesIndependentMembershipPredicates(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("Who Are You?", []string{"episode"}, 20, 0, AccessFilter{
		AllowedLibraryIDs:  []int{1},
		DisabledLibraryIDs: []int{2},
	})
	if !strings.Contains(sql, "ece.media_folder_id = ANY($") {
		t.Fatalf("episode search missing direct allowed-library catalog filter:\n%s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM episode_catalog_entries disabled_ece") {
		t.Fatalf("episode search missing independent disabled-library NOT EXISTS:\n%s", sql)
	}
	assertEpisodeParentDisabledAccess(t, sql, "ece.series_id")
	if strings.Contains(sql, "WHERE e_parent.content_id = ece.episode_id") {
		t.Fatalf("episode candidate branch must use ece.series_id without an episode lookup:\n%s", sql)
	}
}

func TestRerankFuzzyItemsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items := []*models.MediaItem{{ContentID: "one", Title: "One"}, {ContentID: "two", Title: "Two"}}
	if _, err := rerankFuzzyItems(ctx, "three", items, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("rerank error = %v, want context.Canceled", err)
	}
}

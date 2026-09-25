package catalog

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// These tests pin the SQL emitted by buildFilterAccessibleContentIDsSQL — the
// query builder behind LibraryItemRepository.FilterAccessibleContentIDs — for
// each viewer scope shape, without needing a database. They guard the
// properties that make the batch filter agree with the per-item access
// predicate the detail/watch path enforces (ItemRepository.EnsureAccessible):
//   - episodes are gated on their parent SERIES (media_item_libraries via
//     series_id), never on episode_libraries;
//   - a rating-only viewer is gated on rating alone, with no membership join;
//   - placeholder numbering tracks the bound args.

func TestBuildFilterAccessibleContentIDsSQL_AllowedLibrariesOnly(t *testing.T) {
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a", "b"}, []int{1, 2}, nil, access.MaturityLimits{},
	)

	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids, allowed libs); got %d (%v)", len(args), args)
	}
	if !strings.Contains(sql, "unnest($1::text[])") {
		t.Errorf("expected content ids bound at $1; got %s", sql)
	}
	if !strings.Contains(sql, "mil.media_folder_id = ANY($2)") {
		t.Errorf("expected allowed libraries bound at $2; got %s", sql)
	}
	// Item branch gates membership on the item's own content_id; episode branch
	// resolves the parent series and gates membership on series_id. Both use
	// independent EXISTS predicates (libraryAccessConditions), not a join.
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($2))") {
		t.Errorf("expected item membership EXISTS on mi.content_id; got %s", sql)
	}
	if !strings.Contains(sql, "episodes e JOIN media_items mi ON mi.content_id = e.series_id") {
		t.Errorf("expected episode branch to resolve parent series; got %s", sql)
	}
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = e.series_id AND mil.media_folder_id = ANY($2))") {
		t.Errorf("expected episode membership EXISTS on series_id; got %s", sql)
	}
	// Episodes must NOT be gated on episode_libraries — that diverges from the
	// detail endpoint's EnsureAccessible(series_id).
	if strings.Contains(sql, "episode_libraries") {
		t.Errorf("episode access must gate on the series, not episode_libraries; got %s", sql)
	}
	if strings.Contains(sql, "content_rating") {
		t.Errorf("no rating ceiling set, expected no content_rating predicate; got %s", sql)
	}
}

func TestBuildFilterAccessibleContentIDsSQL_RatingOnlyRequiresNoMembership(t *testing.T) {
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, nil, nil, access.MaturityLimits{MaxContentRating: "PG-13"},
	)

	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids, ceiling age); got %d (%v)", len(args), args)
	}
	if args[1] != 14 {
		t.Errorf("expected the PG-13 ceiling to bind age 14, the top of its US tier; got %v", args[1])
	}
	// EnsureAccessible only joins media_item_libraries when a library
	// restriction is set; a rating-only viewer is gated on rating alone.
	if strings.Contains(sql, "media_item_libraries") {
		t.Errorf("rating-only scope must not require a membership join; got %s", sql)
	}
	if !strings.Contains(sql, "(mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $2)") {
		t.Errorf("expected stored-age ceiling predicate bound at $2; got %s", sql)
	}
	// The episode branch still resolves the rating from the parent series.
	if !strings.Contains(sql, "episodes e JOIN media_items mi ON mi.content_id = e.series_id") {
		t.Errorf("expected episode branch to resolve parent series for rating; got %s", sql)
	}
}

func TestBuildFilterAccessibleContentIDsSQL_DisabledLibrariesOnly(t *testing.T) {
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, nil, []int{9, 10}, access.MaturityLimits{},
	)

	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids, disabled libs); got %d (%v)", len(args), args)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($2))") {
		t.Errorf("expected disabled libraries as per-item NOT EXISTS; got %s", sql)
	}
	// Disabled-only scopes still require positive membership so orphan items
	// stay hidden from restricted viewers (see libraryAccessConditions).
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
		t.Errorf("expected membership EXISTS for disabled-library restriction; got %s", sql)
	}
}

func TestBuildFilterAccessibleContentIDsSQL_AllowedDisabledAndRatingPlaceholders(t *testing.T) {
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, []int{1}, []int{9}, access.MaturityLimits{MaxContentRating: "R"},
	)

	// $1 ids, $2 allowed, $3 disabled, $4 ceiling age — in append order.
	if len(args) != 4 {
		t.Fatalf("expected 4 args (ids, allowed, disabled, ceiling age); got %d (%v)", len(args), args)
	}
	for _, want := range []string{
		"mil.media_folder_id = ANY($2)",
		"NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($3))",
		"(mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $4)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("expected SQL to contain %q; got %s", want, sql)
		}
	}
	// Both the allowed and disabled predicates apply to both branches, so each
	// folder placeholder appears once per branch (item + episode).
	if got := strings.Count(sql, "mil.media_folder_id = ANY($2)"); got != 2 {
		t.Errorf("expected allowed predicate in both branches; found %d occurrences", got)
	}
}

// The advisory-age limit rides beside the content-rating ceiling: both are
// bound once and ANDed into both branches, so an episode is gated on its
// parent series' advisory age exactly as its item would be.
func TestBuildFilterAccessibleContentIDsSQL_AdvisoryAgeLimit(t *testing.T) {
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, nil, nil, access.MaturityLimits{MaxContentRating: "PG-13", MaxAdvisoryAge: 10},
	)

	// $1 ids, $2 ceiling age, $3 advisory limit.
	if len(args) != 3 || args[1] != 14 || args[2] != 10 {
		t.Fatalf("expected args [ids 14 10]; got %v", args)
	}
	for _, want := range []string{
		"(mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $2)",
		"(mi.advisory_age IS NULL OR mi.advisory_age <= $3)",
	} {
		if got := strings.Count(sql, want); got != 2 {
			t.Errorf("expected %q in both branches; found %d occurrences in %s", want, got, sql)
		}
	}

	// An advisory limit alone needs no ceiling and no membership join.
	sql, args = buildFilterAccessibleContentIDsSQL([]string{"a"}, nil, nil, access.MaturityLimits{MaxAdvisoryAge: 7})
	if len(args) != 2 || args[1] != 7 {
		t.Fatalf("expected args [ids 7]; got %v", args)
	}
	if strings.Contains(sql, "content_rating_age") || strings.Contains(sql, "media_item_libraries") {
		t.Errorf("advisory-only scope must add only the advisory predicate; got %s", sql)
	}
}

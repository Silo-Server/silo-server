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
		[]string{"a", "b"}, []int{1, 2}, nil, nil,
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
	// Item branch joins membership on the item's own content_id; episode branch
	// resolves the parent series and joins membership on series_id.
	if !strings.Contains(sql, "JOIN media_item_libraries mil ON mil.content_id = mi.content_id") {
		t.Errorf("expected item membership join on mi.content_id; got %s", sql)
	}
	if !strings.Contains(sql, "episodes e JOIN media_items mi ON mi.content_id = e.series_id") {
		t.Errorf("expected episode branch to resolve parent series; got %s", sql)
	}
	if !strings.Contains(sql, "JOIN media_item_libraries mil ON mil.content_id = e.series_id") {
		t.Errorf("expected episode membership join on series_id; got %s", sql)
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
	ratings := access.AllowedRatingsUpTo("PG-13")
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, nil, nil, ratings,
	)

	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids, ratings); got %d (%v)", len(args), args)
	}
	// EnsureAccessible only joins media_item_libraries when a library
	// restriction is set; a rating-only viewer is gated on rating alone.
	if strings.Contains(sql, "media_item_libraries") {
		t.Errorf("rating-only scope must not require a membership join; got %s", sql)
	}
	if !strings.Contains(sql, "mi.content_rating = ANY($2)") {
		t.Errorf("expected rating predicate bound at $2; got %s", sql)
	}
	// The episode branch still resolves the rating from the parent series.
	if !strings.Contains(sql, "episodes e JOIN media_items mi ON mi.content_id = e.series_id") {
		t.Errorf("expected episode branch to resolve parent series for rating; got %s", sql)
	}
}

func TestBuildFilterAccessibleContentIDsSQL_DisabledLibrariesOnly(t *testing.T) {
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, nil, []int{9, 10}, nil,
	)

	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids, disabled libs); got %d (%v)", len(args), args)
	}
	if !strings.Contains(sql, "NOT (mil.media_folder_id = ANY($2))") {
		t.Errorf("expected disabled libraries as NOT ANY($2); got %s", sql)
	}
	if !strings.Contains(sql, "JOIN media_item_libraries mil") {
		t.Errorf("expected membership join for disabled-library restriction; got %s", sql)
	}
}

func TestBuildFilterAccessibleContentIDsSQL_AllowedDisabledAndRatingPlaceholders(t *testing.T) {
	ratings := access.AllowedRatingsUpTo("R")
	sql, args := buildFilterAccessibleContentIDsSQL(
		[]string{"a"}, []int{1}, []int{9}, ratings,
	)

	// $1 ids, $2 allowed, $3 disabled, $4 ratings — in append order.
	if len(args) != 4 {
		t.Fatalf("expected 4 args (ids, allowed, disabled, ratings); got %d (%v)", len(args), args)
	}
	for _, want := range []string{
		"mil.media_folder_id = ANY($2)",
		"NOT (mil.media_folder_id = ANY($3))",
		"mi.content_rating = ANY($4)",
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

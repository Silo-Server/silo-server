package catalog

import (
	"strings"
	"testing"
)

func TestBuildOrderBy_SortTitleFallsBackToTitle(t *testing.T) {
	got := buildOrderBy("sort_title", "desc")
	want := "ORDER BY LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) DESC, LOWER(mi.title) DESC, mi.content_id ASC"
	if got != want {
		t.Fatalf("buildOrderBy(sort_title, desc) = %q, want %q", got, want)
	}
}

func TestBuildOrderBy_TitleAscDesc(t *testing.T) {
	gotAsc := buildOrderBy("title", "asc")
	wantAsc := "ORDER BY LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) ASC, LOWER(mi.title) ASC, mi.content_id ASC"
	if gotAsc != wantAsc {
		t.Fatalf("buildOrderBy(title, asc) = %q, want %q", gotAsc, wantAsc)
	}

	gotDesc := buildOrderBy("title", "desc")
	wantDesc := "ORDER BY LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) DESC, LOWER(mi.title) DESC, mi.content_id ASC"
	if gotDesc != wantDesc {
		t.Fatalf("buildOrderBy(title, desc) = %q, want %q", gotDesc, wantDesc)
	}
}

func TestBuildOrderBy_LastAirDateReadsDenormColumn(t *testing.T) {
	// Migration 103 denormalized the aired-episode aggregate onto
	// media_items.last_air_date_at, replacing the per-row correlated
	// MAX(e.air_date) subquery (audit 2026-05-01 §2.1 hot path #1).
	got := buildOrderBy("last_air_date", "desc")
	if strings.Contains(got, "SELECT MAX(") {
		t.Fatalf("last_air_date order by must NOT contain a correlated subquery, got %q", got)
	}
	if !strings.Contains(got, "mi.last_air_date_at") {
		t.Fatalf("expected last_air_date order by to read mi.last_air_date_at, got %q", got)
	}
	if !strings.Contains(got, "NULLS LAST") {
		t.Fatalf("expected sparse last_air_date sort to push missing items last, got %q", got)
	}
}

func TestBrowsePlan_ReleaseDateMovieOnlyUsesIndexedDateColumn(t *testing.T) {
	repo := &BrowseRepository{}
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{
		Type:  "movie",
		Sort:  "release_date",
		Order: "desc",
		Limit: 1000,
	})
	if err != nil {
		t.Fatalf("buildBrowsePlan error: %v", err)
	}
	if earlyEmpty {
		t.Fatal("did not expect early empty result")
	}
	if strings.Contains(plan.orderBy, "COALESCE(") || strings.Contains(plan.orderBy, "::text") {
		t.Fatalf("movie-only release_date sort must use the indexed date column, got %q", plan.orderBy)
	}
	want := "ORDER BY mi.release_date DESC NULLS LAST, mi.content_id ASC"
	if plan.orderBy != want {
		t.Fatalf("orderBy = %q, want %q", plan.orderBy, want)
	}
}

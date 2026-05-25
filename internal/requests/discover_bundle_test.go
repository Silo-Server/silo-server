package requests

import (
	"strings"
	"testing"
)

func TestBundleHasExpectedCounts(t *testing.T) {
	if len(BundledStudios) != 10 {
		t.Errorf("BundledStudios = %d, want 10", len(BundledStudios))
	}
	if len(BundledNetworks) != 10 {
		t.Errorf("BundledNetworks = %d, want 10", len(BundledNetworks))
	}
	if len(BundledGenres) != 8 {
		t.Errorf("BundledGenres = %d, want 8", len(BundledGenres))
	}
}

func TestBundleStudiosHaveRequiredFields(t *testing.T) {
	for _, s := range BundledStudios {
		if s.TMDBID <= 0 {
			t.Errorf("studio %q missing TMDBID", s.Slug)
		}
		if strings.TrimSpace(s.Slug) == "" {
			t.Errorf("studio %+v missing Slug", s)
		}
		if strings.TrimSpace(s.DisplayName) == "" {
			t.Errorf("studio %q missing DisplayName", s.Slug)
		}
		if !strings.HasPrefix(s.LogoPath, "/") || !strings.HasSuffix(s.LogoPath, ".png") {
			t.Errorf("studio %q LogoPath must be a TMDB file path (/...png), got %q", s.Slug, s.LogoPath)
		}
	}
}

func TestBundleNetworksHaveRequiredFields(t *testing.T) {
	for _, n := range BundledNetworks {
		if n.TMDBID <= 0 {
			t.Errorf("network %q missing TMDBID", n.Slug)
		}
		if strings.TrimSpace(n.Slug) == "" {
			t.Errorf("network %+v missing Slug", n)
		}
		if strings.TrimSpace(n.DisplayName) == "" {
			t.Errorf("network %q missing DisplayName", n.Slug)
		}
		if !strings.HasPrefix(n.LogoPath, "/") || !strings.HasSuffix(n.LogoPath, ".png") {
			t.Errorf("network %q LogoPath must be a TMDB file path (/...png), got %q", n.Slug, n.LogoPath)
		}
	}
}

func TestBundleGenresHaveRequiredFields(t *testing.T) {
	for _, g := range BundledGenres {
		if strings.TrimSpace(g.Slug) == "" {
			t.Errorf("genre %+v missing Slug", g)
		}
		if strings.TrimSpace(g.DisplayName) == "" {
			t.Errorf("genre %q missing DisplayName", g.Slug)
		}
		if g.MovieID <= 0 {
			t.Errorf("genre %q must have MovieID > 0 in v1", g.Slug)
		}
		if !strings.HasPrefix(g.GradientFrom, "#") || !strings.HasPrefix(g.GradientTo, "#") {
			t.Errorf("genre %q gradient must use # hex, got from=%q to=%q", g.Slug, g.GradientFrom, g.GradientTo)
		}
	}
}

func TestBundleSlugsAreUniqueWithinKind(t *testing.T) {
	seen := map[string]string{}
	for _, s := range BundledStudios {
		key := "studio:" + s.Slug
		if prior, ok := seen[key]; ok {
			t.Errorf("duplicate studio slug %q (also %q)", s.Slug, prior)
		}
		seen[key] = s.DisplayName
	}
	for _, n := range BundledNetworks {
		key := "network:" + n.Slug
		if prior, ok := seen[key]; ok {
			t.Errorf("duplicate network slug %q (also %q)", n.Slug, prior)
		}
		seen[key] = n.DisplayName
	}
	for _, g := range BundledGenres {
		key := "genre:" + g.Slug
		if prior, ok := seen[key]; ok {
			t.Errorf("duplicate genre slug %q (also %q)", g.Slug, prior)
		}
		seen[key] = g.DisplayName
	}
}

func TestFindStudioBySlug(t *testing.T) {
	got, ok := FindStudioBySlug("marvel-studios")
	if !ok {
		t.Fatal("expected marvel-studios to exist")
	}
	if got.DisplayName != "Marvel Studios" {
		t.Errorf("display = %q, want Marvel Studios", got.DisplayName)
	}

	if _, ok := FindStudioBySlug("not-a-real-studio"); ok {
		t.Error("expected unknown slug to return false")
	}
}

func TestFindNetworkBySlug(t *testing.T) {
	got, ok := FindNetworkBySlug("netflix")
	if !ok {
		t.Fatal("expected netflix to exist")
	}
	if got.DisplayName != "Netflix" {
		t.Errorf("display = %q, want Netflix", got.DisplayName)
	}
}

func TestFindGenreBySlug(t *testing.T) {
	got, ok := FindGenreBySlug("action")
	if !ok {
		t.Fatal("expected action to exist")
	}
	if got.MovieID != 28 {
		t.Errorf("movie id = %d, want 28", got.MovieID)
	}
}

func TestGenresWithoutTVEquivalentHaveZeroSeriesID(t *testing.T) {
	horror, ok := FindGenreBySlug("horror")
	if !ok {
		t.Fatal("expected horror to exist")
	}
	if horror.SeriesID != 0 {
		t.Errorf("horror.SeriesID = %d, want 0", horror.SeriesID)
	}
	romance, ok := FindGenreBySlug("romance")
	if !ok {
		t.Fatal("expected romance to exist")
	}
	if romance.SeriesID != 0 {
		t.Errorf("romance.SeriesID = %d, want 0", romance.SeriesID)
	}
}

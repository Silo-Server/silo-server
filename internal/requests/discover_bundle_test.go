package requests

import (
	"strings"
	"testing"
)

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

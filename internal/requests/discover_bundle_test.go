package requests

import (
	"testing"
)

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

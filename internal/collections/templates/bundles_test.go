package templates

import (
	"slices"
	"testing"
)

// TestBuiltinBundlesWithTemplatesFollowBundleOrder pins that each built-in
// bundle lists its templates in template_ids order, Discover and Franchise
// templates included, so a client can show a bundle without the template
// catalog.
func TestBuiltinBundlesWithTemplatesFollowBundleOrder(t *testing.T) {
	listed := Default.BundlesWithTemplates()
	bundles := ListBundles()
	if len(listed) != len(bundles) {
		t.Fatalf("got %d bundles with templates, want %d", len(listed), len(bundles))
	}
	sources := map[Source]bool{}
	for i, b := range listed {
		if b.ID != bundles[i].ID {
			t.Fatalf("bundle %d = %q, want %q", i, b.ID, bundles[i].ID)
		}
		ids := make([]string, 0, len(b.Templates))
		for _, tmpl := range b.Templates {
			ids = append(ids, tmpl.ID)
			sources[tmpl.Source] = true
			want, _ := Get(tmpl.ID)
			if tmpl.Title != want.Title || tmpl.PosterPath != want.PosterPath || tmpl.Featured != want.Featured {
				t.Errorf("%s/%s: template differs from the registry", b.ID, tmpl.ID)
			}
		}
		if !slices.Equal(ids, b.TemplateIDs) {
			t.Errorf("%s: templates %v, want template_ids %v", b.ID, ids, b.TemplateIDs)
		}
	}
	for _, source := range []Source{SourceTMDBDiscover, SourceTMDBCollection} {
		if !sources[source] {
			t.Errorf("no bundle lists a %s template", source)
		}
	}
}

// TestNeedsSetupOnlyForFranchisePlaceholder pins which built-in template
// creates a collection that cannot sync until an admin sets its source.
func TestNeedsSetupOnlyForFranchisePlaceholder(t *testing.T) {
	for _, tmpl := range List() {
		want := tmpl.ID == "tmdb_franchise_placeholder"
		if got := tmpl.NeedsSetup(); got != want {
			t.Errorf("%s: NeedsSetup() = %v, want %v", tmpl.ID, got, want)
		}
	}
	if (Template{Source: SourceTMDBCollection}).NeedsSetup() != true {
		t.Error("a TMDB collection template without a spec must need setup")
	}
}

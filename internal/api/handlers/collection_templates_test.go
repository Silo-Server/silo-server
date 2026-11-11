package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

// Bundle apply dedupes collections by slugified title per library
// (applyTemplateBundle's slug-match adoption), so two builtin templates with
// the same title slug can never coexist: whichever applies second is silently
// skipped as already_exists.
func TestBuiltinTemplateTitleSlugsAreUnique(t *testing.T) {
	bySlug := make(map[string]string)
	for _, tmpl := range templates.List() {
		slug := slugifyCollectionName(tmpl.Title)
		if other, exists := bySlug[slug]; exists {
			t.Errorf("templates %q and %q share title slug %q; bundle apply would silently skip one", other, tmpl.ID, slug)
			continue
		}
		bySlug[slug] = tmpl.ID
	}
}

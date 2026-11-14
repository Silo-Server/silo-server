package pgstore

import (
	"testing"
)

// TestImportedSectionOverridesAreBounded: an imported layout holds no more
// overrides per page, or in all, than a layout editor saves, and no item
// limit the editor refuses.
func TestImportedSectionOverridesAreBounded(t *testing.T) {
	page := func(n int) []sectionOverrideEntry {
		out := make([]sectionOverrideEntry, n)
		for i := range out {
			out[i] = sectionOverrideEntry{Scope: scopeHome, IsUserAdded: true}
		}
		return out
	}
	over := maxOverrideItemLimit + 1
	for name, c := range map[string]struct {
		entries []sectionOverrideEntry
		ok      bool
	}{
		"a full page":      {page(maxOverridesPerPage), true},
		"too many on page": {page(maxOverridesPerPage + 1), false},
		"too many in all":  {page(maxOverrides + 1), false},
		"item limit":       {[]sectionOverrideEntry{{Scope: scopeHome, ItemLimit: &over}}, false},
	} {
		if err := validateSectionOverrides(c.entries); (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok %v", name, err, c.ok)
		}
	}
}

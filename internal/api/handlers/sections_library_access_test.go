package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestProfileSectionSettingsRejectInaccessibleLibrary(t *testing.T) {
	for name, scope := range map[string]access.Scope{
		"outside allowlist": {AllowedLibraryIDs: []int{2}},
		"empty allowlist":   {AllowedLibraryIDs: []int{}},
		"disabled":          {DisabledLibraryIDs: []int{1}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := access.SetScope(t.Context(), scope)
			// No repositories are wired: rejection must precede any read or write.
			h := &SectionHandler{}
			q := SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "library", LibraryID: "1"}
			_, listErr := h.ListProfileOverrides(ctx, q)
			saveErr := h.SaveProfileOverrides(ctx, q, nil)
			resetErr := h.ResetProfileOverrides(ctx, q)
			_, resolveErr := h.ResolveProfileSectionSettings(ctx, 1, "p1", "library", new(1), catalog.AccessFilter{})
			for operation, err := range map[string]error{"list": listErr, "replace": saveErr, "reset": resetErr, "resolve": resolveErr} {
				apiErr, ok := errors.AsType[*APIError](err)
				if !ok || apiErr.Status != 404 {
					t.Errorf("%s error=%v, want 404", operation, err)
				}
			}
		})
	}
}

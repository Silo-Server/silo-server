package metadata

import (
	"context"
	"testing"
)

// A capability with a blank display_name is named by its capability ID, as
// its provider is, so the admin list still credits a provider for the source.
func TestDeclaredRatingSourcesNamesABlankProviderByItsCapabilityID(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	installationID := insertTestInstallation(t, pool, "plugin", true)
	insertTestCapability(t, pool, installationID, "blank-name-ratings",
		`{"display_name": "", "rating_sources": [{"id": "test_blank_name", "name": "Blank", "scale": 10}]}`)

	sources, err := DeclaredRatingSources(context.Background(), pool)
	if err != nil {
		t.Fatalf("DeclaredRatingSources: %v", err)
	}
	for _, source := range sources {
		if source.Source == "test_blank_name" {
			if source.Provider != "blank-name-ratings" {
				t.Fatalf("provider = %q, want the capability ID", source.Provider)
			}
			return
		}
	}
	t.Fatalf("test_blank_name missing from %+v", sources)
}

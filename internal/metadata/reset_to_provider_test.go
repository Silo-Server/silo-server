package metadata

import (
	"context"
	"testing"
)

// seedEditedMovie stores a matched movie the way Edit Metadata leaves it: an
// admin-typed tagline and overview under the overview lock, and the title
// under the name lock.
func seedEditedMovie(t *testing.T, h *testHarness, contentID string) {
	t.Helper()
	seedMovieIdentity(t, h, contentID, "100", "")
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load seeded item: %v", err)
	}
	item.Title = "Typed Title"
	item.Overview = "Typed overview"
	item.Tagline = "Typed tagline"
	item.LockedFields = []int{int(FieldName), int(FieldOverview)}
	if err := h.itemRepo.Upsert(context.Background(), item); err != nil {
		t.Fatalf("seed edits: %v", err)
	}
}

// Reset to Provider (#3581) promises the provider's values for every field. A
// provider that has no tagline must leave the tagline empty, not keep the one
// an admin typed. TestResetToProvider_ItemRowClearsStoredLocks covers the
// stored locks, which this fake repository doesn't keep the way Postgres does.
func TestProcess_ResetToProviderReplacesEditsWithProviderValues(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	seedEditedMovie(t, h, contentID)
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Provider Title", Year: 2006, Overview: "Provider overview",
		ProviderIDs: map[string]string{"tmdb": "100"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}

	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "Provider Title" || item.Overview != "Provider overview" || item.Tagline != "" {
		t.Errorf("title=%q overview=%q tagline=%q, want the provider's values and no tagline", item.Title, item.Overview, item.Tagline)
	}
}

// A reset can't tell a value a provider lacks from one it failed to send, so
// it only clears fields when the providers answered. A provider that fails, or
// a chain with no metadata at all, fails the reset, and the item keeps its
// values and locks.
func TestProcess_ResetToProviderKeepsTheItemWhenProvidersDoNotAnswer(t *testing.T) {
	answering := func() Provider {
		return &capturingMetadataProvider{response: &MetadataResult{
			HasMetadata: true, Title: "Provider Title", Year: 2006,
			ProviderIDs: map[string]string{"tmdb": "100"},
		}}
	}
	for _, tc := range []struct {
		name      string
		providers []Provider
	}{
		{name: "provider outage", providers: []Provider{answering(), transientRemote{slug: "tvdb"}}},
		{name: "no metadata", providers: []Provider{&capturingMetadataProvider{response: &MetadataResult{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const contentID = "146000000000000100"
			h := newTestHarness()
			seedEditedMovie(t, h, contentID)

			result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
				ContentID: contentID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
			}, tc.providers)
			if err == nil && result != nil && result.Updated {
				t.Fatalf("ProcessWithProviders = %#v, want a failed reset", result)
			}

			item, err := h.itemRepo.GetByID(context.Background(), contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if item.Title != "Typed Title" || item.Overview != "Typed overview" || item.Tagline != "Typed tagline" {
				t.Errorf("title=%q overview=%q tagline=%q, want the admin's values kept", item.Title, item.Overview, item.Tagline)
			}
			if len(item.LockedFields) != 2 {
				t.Errorf("locked_fields = %v, want the stored locks kept", item.LockedFields)
			}
		})
	}
}

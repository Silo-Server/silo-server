package metadata

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/models"
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

// A reset through a library in another language writes that language's text
// to the localization rows, as any refresh does, so the base row's text is the
// item's own language, which the reset did not fetch. Emptying it would lose
// text the provider has.
func TestProcess_ResetToProviderInAnotherLanguageKeepsBaseText(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	seedEditedMovie(t, h, contentID)
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatal(err)
	}
	item.DefaultMetadataLanguage = "en"
	if err := h.itemRepo.Upsert(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Provider Titel", Year: 2006, Overview: "Provider Übersicht",
		ProviderIDs: map[string]string{"tmdb": "100"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID, Language: "de", Mode: ModeManualRefresh, resetToProvider: true,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}

	item, err = h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Typed Title" || item.Overview != "Typed overview" || item.Tagline != "Typed tagline" {
		t.Errorf("base title=%q overview=%q tagline=%q, want the item's own-language text kept", item.Title, item.Overview, item.Tagline)
	}
}

// Credits follow the reset too: when the providers list none, the stored ones
// go (#3581).
func TestProcess_ResetToProviderClearsCreditsTheProvidersLack(t *testing.T) {
	const contentID = "146000000000000100"
	h, repo := newLocalPipelineHarness()
	h.service.personRepo = stubPersonRepo{ids: []int64{1, 2}}
	seedEditedMovie(t, h, contentID)
	repo.people[contentID] = []models.ItemPerson{actor("Typed Actor", "Lead")}
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Provider Title", Year: 2006,
		ProviderIDs: map[string]string{"tmdb": "100"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}

	people, replaced := h.itemRepo.replacedPeople[contentID]
	if !replaced || len(people) != 0 {
		t.Errorf("ReplacePeople called = %v with %v, want the stored credits cleared", replaced, personNames(people))
	}
}

// notFoundRemote answers every lookup the way a plugin says it has no such
// title: a gRPC NotFound status, which PluginProvider passes through as is.
type notFoundRemote struct{ slug string }

func (p notFoundRemote) Slug() string       { return p.slug }
func (p notFoundRemote) Name() string       { return p.slug }
func (p notFoundRemote) ForTypes() []string { return []string{"movie"} }
func (p notFoundRemote) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return nil, status.Error(codes.NotFound, p.slug+": no such title")
}

// A provider that says it doesn't know the title has answered, so the reset
// goes ahead with the other providers' values, whether the answer is a
// built-in provider's HTTP 404 or a plugin's NotFound status.
func TestProcess_ResetToProviderAcceptsANotFoundAnswer(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	seedEditedMovie(t, h, contentID)
	answering := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Provider Title", Year: 2006,
		ProviderIDs: map[string]string{"tmdb": "100"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
	}, []Provider{answering, notFoundRemote{slug: "plugin-metadata"}}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Tagline != "" {
		t.Errorf("tagline = %q, want the reset applied", item.Tagline)
	}
}

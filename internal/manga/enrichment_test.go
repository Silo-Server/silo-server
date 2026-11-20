package manga

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeMangaMetadataProvider struct {
	slug      string
	results   []metadata.SearchResult
	searchErr error
	result    *metadata.MetadataResult
	getErr    error
}

type fakeMangaProviderIDOwner struct {
	ownerByID map[string]string
}

func (f *fakeMangaProviderIDOwner) FindContentIDByProviderIDs(_ context.Context, ids map[string]string, _ string, exclude string) (string, error) {
	for _, providerID := range ids {
		if owner := f.ownerByID[providerID]; owner != "" && owner != exclude {
			return owner, nil
		}
	}
	return "", nil
}

func (f *fakeMangaMetadataProvider) Slug() string       { return f.slug }
func (f *fakeMangaMetadataProvider) Name() string       { return f.slug }
func (f *fakeMangaMetadataProvider) ForTypes() []string { return []string{"manga"} }
func (f *fakeMangaMetadataProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return f.results, f.searchErr
}
func (f *fakeMangaMetadataProvider) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return f.result, f.getErr
}

func TestMangaEnrichmentBackoffPreservesRetryableFailures(t *testing.T) {
	step, ceiling := mangaEnrichmentBackoff(metadata.ProviderErrorTransient, 0)
	if step != 15*time.Minute || ceiling != 6*time.Hour {
		t.Fatalf("transient backoff = (%v, %v), want (15m, 6h)", step, ceiling)
	}

	step, ceiling = mangaEnrichmentBackoff(metadata.ProviderErrorRateLimited, 2*time.Hour)
	if step != 2*time.Hour || ceiling != 24*time.Hour {
		t.Fatalf("rate-limit backoff = (%v, %v), want (2h, 24h)", step, ceiling)
	}

	step, ceiling = mangaEnrichmentBackoff(metadata.ProviderErrorPermanent, 0)
	if step != 0 || ceiling != 0 {
		t.Fatalf("permanent backoff = (%v, %v), want no cooldown before the bounded cap", step, ceiling)
	}
}

func TestCollectMangaMetadataRejectsEachProviderAuthorBeforeMerging(t *testing.T) {
	providers := []metadata.Provider{
		&fakeMangaMetadataProvider{
			slug:    "anilist",
			results: []metadata.SearchResult{{Name: "Shared Title", ProviderIDs: map[string]string{"anilist": "1"}}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				Overview:    "wrong provider overview",
				People: []models.ItemPerson{{
					Person: models.Person{Name: "Wrong Author"}, Kind: models.PersonKindAuthor,
				}},
			},
		},
		&fakeMangaMetadataProvider{
			slug:    "mangadex",
			results: []metadata.SearchResult{{Name: "Shared Title", ProviderIDs: map[string]string{"mangadex": "2"}}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				People: []models.ItemPerson{{
					Person: models.Person{Name: "Right Author"}, Kind: models.PersonKindAuthor,
				}},
			},
		},
	}

	accumulator, _, _, authorMismatch := collectMangaMetadata(context.Background(), enrichmentItemRow{
		ContentID: "shared", Title: "Shared Title", Author: "Right Author",
	}, providers, nil)

	if !authorMismatch {
		t.Fatal("a provider with a contradictory author did not fail the item closed")
	}
	if accumulator.Overview != "" || accumulator.HasMetadata {
		t.Fatalf("contradictory provider metadata was merged before validation: %+v", accumulator)
	}
}

func TestEnrichWithProvidersRetriesProviderErrorBeforeAuthorMismatch(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	providers := []metadata.Provider{
		&fakeMangaMetadataProvider{slug: "broken", searchErr: providerErr, getErr: providerErr},
		&fakeMangaMetadataProvider{
			slug:    "wrong-author",
			results: []metadata.SearchResult{{Name: "Shared Title", ProviderIDs: map[string]string{"wrong": "1"}}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				People: []models.ItemPerson{{
					Person: models.Person{Name: "Wrong Author"}, Kind: models.PersonKindAuthor,
				}},
			},
		},
	}

	e := &Enricher{}
	err := e.enrichWithProviders(context.Background(), enrichmentItemRow{
		ContentID: "shared", FolderID: 7, Title: "Shared Title", Author: "Right Author",
	}, providers)
	if !errors.Is(err, providerErr) {
		t.Fatalf("enrichment error = %v, want provider failure to remain retryable", err)
	}
}

type failingMangaProviderIDRepository struct {
	err error
}

func (f *failingMangaProviderIDRepository) GetByContentIDs(context.Context, []string) (map[string][]*models.MediaItemProviderID, error) {
	return nil, nil
}

func (f *failingMangaProviderIDRepository) ReplaceByContentID(context.Context, string, map[string]string) error {
	return f.err
}

func (f *failingMangaProviderIDRepository) FindContentIDByProviderIDs(context.Context, map[string]string, string, string) (string, error) {
	return "", nil
}

// An item with no author has no author check after GetMetadata, so a partial
// title match alone must not give it another book's metadata (#4154).
func TestMangaWithoutAnAuthorNeedsTheWholeTitle(t *testing.T) {
	for _, tc := range []struct {
		name, title, author, candidate string
		matched                        bool
	}{
		{name: "a one-word prefix", candidate: "Raw"},
		{name: "the same title", candidate: "Raw Stream", matched: true},
		{name: "a one-word prefix with an author", author: "Someone", candidate: "Raw", matched: true},
		{name: "a release tag in the folder name", title: "Naruto [Digital]", candidate: "Naruto", matched: true},
		{name: "a longer title behind a release tag", title: "Naruto [Digital]", candidate: "Naruto Gaiden"},
	} {
		if tc.title == "" {
			tc.title = "Raw Stream"
		}
		t.Run(tc.name, func(t *testing.T) {
			providers := []metadata.Provider{&fakeMangaMetadataProvider{
				slug:    "mangadex",
				results: []metadata.SearchResult{{Name: tc.candidate, ProviderIDs: map[string]string{"mangadex": "1"}}},
			}}
			// The admitted provider IDs are what GetMetadata fetches by.
			_, ids, _, _ := collectMangaMetadata(context.Background(), enrichmentItemRow{
				ContentID: "c1", Title: tc.title, Author: tc.author,
			}, providers, nil)
			if matched := ids["mangadex"] == "1"; matched != tc.matched {
				t.Fatalf("%q for %q (author %q): matched = %v, want %v", tc.candidate, tc.title, tc.author, matched, tc.matched)
			}
		})
	}
}

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

// The scanner's manga_series identity rows must never reach the metadata
// flow: they made the search-skip guard treat every item as already matched.
func TestFilterMangaProviderIDsDropsInternalIdentity(t *testing.T) {
	filtered := filterMangaProviderIDs(map[string]string{
		"manga_series": "abc123",
		"anilist":      "42",
		"asin":         "B000",
	})
	if _, ok := filtered["manga_series"]; ok {
		t.Fatalf("manga_series identity must be filtered, got %v", filtered)
	}
	if filtered["anilist"] != "42" {
		t.Fatalf("anilist id must survive, got %v", filtered)
	}
	if len(filtered) != 1 {
		t.Fatalf("filtered = %v, want only anilist", filtered)
	}
}

func TestNormalizeMangaStatus(t *testing.T) {
	cases := map[string]string{
		// AniList enum
		"RELEASING":        "Ongoing",
		"FINISHED":         "Completed",
		"NOT_YET_RELEASED": "Upcoming",
		"CANCELLED":        "Cancelled",
		"HIATUS":           "Hiatus",
		// MangaDex / lowercase
		"ongoing":   "Ongoing",
		"completed": "Completed",
		"hiatus":    "Hiatus",
		"cancelled": "Cancelled",
		// SDK Continuing/Ended + spacing/casing variants
		"Continuing":  "Ongoing",
		"Ended":       "Completed",
		"on hiatus":   "Hiatus",
		"  Upcoming ": "Upcoming",
		// Empty and unknown pass through (trimmed)
		"":          "",
		"  ":        "",
		"Weird-Val": "Weird-Val",
	}
	for in, want := range cases {
		if got := normalizeMangaStatus(in); got != want {
			t.Fatalf("normalizeMangaStatus(%q) = %q, want %q", in, got, want)
		}
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

func TestCollectMangaMetadataDoesNotReintroduceOwnedCrossID(t *testing.T) {
	providers := []metadata.Provider{
		&fakeMangaMetadataProvider{
			slug: "anilist",
			results: []metadata.SearchResult{{
				Name: "Shared Title",
				ProviderIDs: map[string]string{
					"anilist":  "free-id",
					"mangadex": "owned-id",
				},
			}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				Overview:    "usable metadata",
				ProviderIDs: map[string]string{
					"anilist":  "free-id",
					"mangadex": "owned-id",
				},
			},
		},
	}
	owner := &fakeMangaProviderIDOwner{ownerByID: map[string]string{"owned-id": "other-manga"}}

	accumulator, ids, errs, authorMismatch := collectMangaMetadata(
		context.Background(),
		enrichmentItemRow{ContentID: "this-manga", Title: "Shared Title"},
		providers,
		owner,
	)

	if len(errs) != 0 || authorMismatch {
		t.Fatalf("collect errors = %v, authorMismatch = %v", errs, authorMismatch)
	}
	if accumulator.Overview != "usable metadata" || ids["anilist"] != "free-id" {
		t.Fatalf("usable metadata/identity was lost: accumulator=%+v ids=%v", accumulator, ids)
	}
	if _, exists := ids["mangadex"]; exists {
		t.Fatalf("metadata response reintroduced an owned cross-ID: %v", ids)
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

func TestPersistReturnsProviderIDFailure(t *testing.T) {
	replaceErr := errors.New("provider identity already belongs to another item")
	e := &Enricher{providerIDs: &failingMangaProviderIDRepository{err: replaceErr}}

	err := e.persist(context.Background(), "manga-1", map[string]string{"anilist": "42"}, &metadata.MetadataResult{
		HasMetadata: true,
		Overview:    "remote overview",
	})

	if !errors.Is(err, replaceErr) {
		t.Fatalf("persist error = %v, want provider-ID failure %v", err, replaceErr)
	}
}

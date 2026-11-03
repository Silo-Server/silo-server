package audiobooks

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCacheRemotePosterSkipsAlreadyCachedPath(t *testing.T) {
	cacher := &fakeAudiobookImageCacher{}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "local/audiobooks/content-1/poster/original.webp",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 0 {
		t.Fatalf("CacheImage calls = %d, want 0", cacher.calls)
	}
	if result.PosterPath != "local/audiobooks/content-1/poster/original.webp" {
		t.Fatalf("PosterPath = %q", result.PosterPath)
	}
}

type fakeAudiobookImageCacher struct {
	calls int
	req   metadata.CacheImageRequest
}

func (f *fakeAudiobookImageCacher) CacheAudiobookCover(context.Context, []byte, string) (string, string, error) {
	return "", "", nil
}

func (f *fakeAudiobookImageCacher) CacheImage(_ context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	f.calls++
	f.req = req
	return &metadata.CacheImageResult{
		BasePath:     req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster",
		OriginalPath: req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster/original.test-revision.webp",
		Thumbhash:    "thumb",
		Ext:          ".webp",
	}, nil
}

type fakeAudiobookMetadataProvider struct {
	slug      string
	results   []metadata.SearchResult
	searchErr error
	result    *metadata.MetadataResult
	getErr    error
}

func (f *fakeAudiobookMetadataProvider) Slug() string       { return f.slug }
func (f *fakeAudiobookMetadataProvider) Name() string       { return f.slug }
func (f *fakeAudiobookMetadataProvider) ForTypes() []string { return []string{"audiobook"} }
func (f *fakeAudiobookMetadataProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return f.results, f.searchErr
}
func (f *fakeAudiobookMetadataProvider) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return f.result, f.getErr
}

func TestEnrichItemRetriesProviderErrorBeforeAuthorMismatch(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	providers := []metadata.Provider{
		&fakeAudiobookMetadataProvider{slug: "broken", searchErr: providerErr, getErr: providerErr},
		&fakeAudiobookMetadataProvider{
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
	e := &Enricher{
		resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
			return providers, nil
		},
	}

	err := e.enrichItem(context.Background(), enrichmentItemRow{
		ContentID: "shared", FolderID: 7, Title: "Shared Title", Author: "Right Author",
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("enrichment error = %v, want provider failure to remain retryable", err)
	}
}

type recordingAudiobookItemRepo struct {
	updates []*catalog.MetadataUpdate
}

func (r *recordingAudiobookItemRepo) UpdateMetadata(_ context.Context, _ string, upd *catalog.MetadataUpdate) error {
	r.updates = append(r.updates, upd)
	return nil
}

func (r *recordingAudiobookItemRepo) UpdateMetadataTx(_ context.Context, _ pgx.Tx, _ string, upd *catalog.MetadataUpdate) error {
	r.updates = append(r.updates, upd)
	return nil
}

func (r *recordingAudiobookItemRepo) ReplacePeople(context.Context, string, []models.ItemPerson) error {
	return nil
}

// A provider's runtime describes whichever edition it matched, so it must not
// replace the length the scanner measured from the book's own files.
func TestPersistKeepsScannedRuntimeOverProviderRuntime(t *testing.T) {
	repo := &recordingAudiobookItemRepo{}
	e := &Enricher{itemRepo: repo}

	err := e.persist(context.Background(), enrichmentItemRow{ContentID: "book"}, nil, &metadata.MetadataResult{
		HasMetadata: true, Overview: "About the book", Runtime: 481,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("metadata updates = %d, want 1", len(repo.updates))
	}
	if got := repo.updates[0].Runtime; got != nil {
		t.Fatalf("update runtime = %d, want the scanned runtime left alone", *got)
	}
}

package abs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ebookEnrichmentStore counts both the batch and the single-item ebook reads
// so the enrichment tests can assert that a page costs two queries and never
// fans out to one pair per item.
type ebookEnrichmentStore struct {
	*stubMediaStore
	files      map[string][]*models.MediaFile
	selections map[string]EbookPrimarySelection
	singleFile int
	singlePref int
	batchFile  int
	batchPref  int
	fileErr    error
	prefErr    error
}

func newEbookEnrichmentStore() *ebookEnrichmentStore {
	return &ebookEnrichmentStore{stubMediaStore: &stubMediaStore{}}
}

func (s *ebookEnrichmentStore) GetMediaFiles(_ context.Context, contentID string, _ catalog.AccessFilter) ([]*models.MediaFile, error) {
	s.singleFile++
	return s.files[contentID], nil
}

func (s *ebookEnrichmentStore) GetPrimaryEbookFileID(_ context.Context, contentID string) (EbookPrimarySelection, error) {
	s.singlePref++
	return s.selections[contentID], nil
}

func (s *ebookEnrichmentStore) GetMediaFilesByContentIDs(_ context.Context, contentIDs []string, _ catalog.AccessFilter) (map[string][]*models.MediaFile, error) {
	s.batchFile++
	if s.fileErr != nil {
		return nil, s.fileErr
	}
	out := make(map[string][]*models.MediaFile, len(contentIDs))
	for _, contentID := range contentIDs {
		out[contentID] = s.files[contentID]
	}
	return out, nil
}

func (s *ebookEnrichmentStore) GetPrimaryEbookFileIDs(_ context.Context, contentIDs []string) (map[string]EbookPrimarySelection, error) {
	s.batchPref++
	if s.prefErr != nil {
		return nil, s.prefErr
	}
	out := make(map[string]EbookPrimarySelection, len(contentIDs))
	for _, contentID := range contentIDs {
		if selection, ok := s.selections[contentID]; ok {
			out[contentID] = selection
		}
	}
	return out, nil
}

func ebookEnrichmentEntries() []LibraryItem {
	return []LibraryItem{
		{ID: testEbookID, Media: LibraryItemMedia{ID: testEbookID}},
		{ID: testSecondEbookID, Media: LibraryItemMedia{ID: testSecondEbookID}},
	}
}

func ebookEnrichmentFiles() map[string][]*models.MediaFile {
	return map[string][]*models.MediaFile{
		testEbookID: {
			{ID: 11, FilePath: "/books/one.pdf"},
			{ID: 12, FilePath: "/books/one.epub"},
		},
		testSecondEbookID: {{ID: 21, FilePath: "/books/two.cbz"}},
	}
}

func (s *ebookEnrichmentStore) assertNoFanOut(t *testing.T) {
	t.Helper()
	if s.singleFile != 0 || s.singlePref != 0 {
		t.Fatalf("single-item calls = files:%d preferences:%d, want none", s.singleFile, s.singlePref)
	}
}

func TestEnrichEbookLibraryItemsDoesNotFanOutAfterBatchFailure(t *testing.T) {
	entries := ebookEnrichmentEntries()
	store := newEbookEnrichmentStore()
	store.fileErr = errors.New("database unavailable")
	h := New(Dependencies{MediaStore: store})

	got := h.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})

	if !reflect.DeepEqual(got, entries) {
		t.Fatalf("failed batch changed entries\ngot:  %#v\nwant: %#v", got, entries)
	}
	if store.batchFile != 1 || store.batchPref != 0 {
		t.Fatalf("batch calls = files:%d preferences:%d, want 1 and 0", store.batchFile, store.batchPref)
	}
	store.assertNoFanOut(t)
}

func TestEnrichEbookLibraryItemsFallsBackWithoutFanOutAfterPrimaryBatchFailure(t *testing.T) {
	entries := ebookEnrichmentEntries()
	files := ebookEnrichmentFiles()
	store := newEbookEnrichmentStore()
	store.files = files
	store.prefErr = errors.New("database unavailable")
	h := New(Dependencies{MediaStore: store})

	got := h.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})
	want := []LibraryItem{
		siloEbookToLibraryItemDetail(entries[0], files[testEbookID], EbookPrimarySelection{}),
		siloEbookToLibraryItemDetail(entries[1], files[testSecondEbookID], EbookPrimarySelection{}),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failed primary batch did not use file fallback\ngot:  %#v\nwant: %#v", got, want)
	}
	if store.batchFile != 1 || store.batchPref != 1 {
		t.Fatalf("batch calls = files:%d preferences:%d, want 1 each", store.batchFile, store.batchPref)
	}
	store.assertNoFanOut(t)
}

// TestEnrichEbookLibraryItemsUsesBatchCapability pins the query budget: one
// page of ebooks costs one file query and one primary-selection query no
// matter how many items it holds, and the curated selection still reaches the
// emitted item.
func TestEnrichEbookLibraryItemsUsesBatchCapability(t *testing.T) {
	entries := ebookEnrichmentEntries()
	files := ebookEnrichmentFiles()
	selections := map[string]EbookPrimarySelection{
		testEbookID: {FileID: 11, Configured: true, HasPrimary: true},
	}
	store := newEbookEnrichmentStore()
	store.files = files
	store.selections = selections
	h := New(Dependencies{MediaStore: store})

	got := h.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})
	want := []LibraryItem{
		siloEbookToLibraryItemDetail(entries[0], files[testEbookID], selections[testEbookID]),
		siloEbookToLibraryItemDetail(entries[1], files[testSecondEbookID], EbookPrimarySelection{}),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch enrichment output mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if store.batchFile != 1 || store.batchPref != 1 {
		t.Fatalf("batch calls = files:%d preferences:%d, want 1 each", store.batchFile, store.batchPref)
	}
	store.assertNoFanOut(t)
}

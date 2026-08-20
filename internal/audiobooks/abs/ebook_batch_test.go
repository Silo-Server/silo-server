package abs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type singleEbookEnrichmentStore struct {
	*stubMediaStore
	files      map[string][]*models.MediaFile
	selections map[string]EbookPrimarySelection
	singleFile int
	singlePref int
}

func (s *singleEbookEnrichmentStore) GetMediaFiles(_ context.Context, contentID string, _ catalog.AccessFilter) ([]*models.MediaFile, error) {
	s.singleFile++
	return s.files[contentID], nil
}

func (s *singleEbookEnrichmentStore) GetPrimaryEbookFileID(_ context.Context, contentID string) (int, bool, bool, error) {
	s.singlePref++
	selection := s.selections[contentID]
	return selection.FileID, selection.Configured, selection.HasPrimary, nil
}

func (s *singleEbookEnrichmentStore) SetPrimaryEbookFileID(context.Context, string, int) error {
	return nil
}

func (s *singleEbookEnrichmentStore) ClearPrimaryEbookFileID(context.Context, string) error {
	return nil
}

type batchEbookEnrichmentStore struct {
	*singleEbookEnrichmentStore
	batchFile int
	batchPref int
	fileErr   error
	prefErr   error
}

func (s *batchEbookEnrichmentStore) GetMediaFilesByContentIDs(_ context.Context, contentIDs []string, _ catalog.AccessFilter) (map[string][]*models.MediaFile, error) {
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

func (s *batchEbookEnrichmentStore) GetPrimaryEbookFileIDs(_ context.Context, contentIDs []string) (map[string]EbookPrimarySelection, error) {
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

func TestEnrichEbookLibraryItemsDoesNotFanOutAfterBatchFailure(t *testing.T) {
	entries := []LibraryItem{
		{ID: testEbookID, Media: LibraryItemMedia{ID: testEbookID}},
		{ID: testSecondEbookID, Media: LibraryItemMedia{ID: testSecondEbookID}},
	}
	store := &batchEbookEnrichmentStore{
		singleEbookEnrichmentStore: &singleEbookEnrichmentStore{stubMediaStore: &stubMediaStore{}},
		fileErr:                    errors.New("database unavailable"),
	}
	h := New(Dependencies{MediaStore: store})

	got := h.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})

	if !reflect.DeepEqual(got, entries) {
		t.Fatalf("failed batch changed entries\ngot:  %#v\nwant: %#v", got, entries)
	}
	if store.batchFile != 1 || store.batchPref != 0 {
		t.Fatalf("batch calls = files:%d preferences:%d, want 1 and 0", store.batchFile, store.batchPref)
	}
	if store.singleFile != 0 || store.singlePref != 0 {
		t.Fatalf("single-item calls = files:%d preferences:%d, want none", store.singleFile, store.singlePref)
	}
}

func TestEnrichEbookLibraryItemsFallsBackWithoutFanOutAfterPrimaryBatchFailure(t *testing.T) {
	entries := []LibraryItem{
		{ID: testEbookID, Media: LibraryItemMedia{ID: testEbookID}},
		{ID: testSecondEbookID, Media: LibraryItemMedia{ID: testSecondEbookID}},
	}
	files := map[string][]*models.MediaFile{
		testEbookID: {
			{ID: 11, FilePath: "/books/one.pdf"},
			{ID: 12, FilePath: "/books/one.epub"},
		},
		testSecondEbookID: {{ID: 21, FilePath: "/books/two.cbz"}},
	}
	store := &batchEbookEnrichmentStore{
		singleEbookEnrichmentStore: &singleEbookEnrichmentStore{
			stubMediaStore: &stubMediaStore{},
			files:          files,
		},
		prefErr: errors.New("database unavailable"),
	}
	h := New(Dependencies{MediaStore: store})

	got := h.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})
	want := []LibraryItem{
		siloEbookToLibraryItemDetail(entries[0], files[testEbookID], 0, false, false),
		siloEbookToLibraryItemDetail(entries[1], files[testSecondEbookID], 0, false, false),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failed primary batch did not use file fallback\ngot:  %#v\nwant: %#v", got, want)
	}
	if store.batchFile != 1 || store.batchPref != 1 {
		t.Fatalf("batch calls = files:%d preferences:%d, want 1 each", store.batchFile, store.batchPref)
	}
	if store.singleFile != 0 || store.singlePref != 0 {
		t.Fatalf("single-item calls = files:%d preferences:%d, want none", store.singleFile, store.singlePref)
	}
}

func TestEnrichEbookLibraryItemsUsesBatchCapability(t *testing.T) {
	files := map[string][]*models.MediaFile{
		testEbookID: {
			{ID: 11, FilePath: "/books/one.pdf"},
			{ID: 12, FilePath: "/books/one.epub"},
		},
		testSecondEbookID: {{ID: 21, FilePath: "/books/two.cbz"}},
	}
	selections := map[string]EbookPrimarySelection{
		testEbookID: {FileID: 11, Configured: true, HasPrimary: true},
	}
	entries := []LibraryItem{
		{ID: testEbookID, Media: LibraryItemMedia{ID: testEbookID}},
		{ID: testSecondEbookID, Media: LibraryItemMedia{ID: testSecondEbookID}},
	}

	singleStore := &singleEbookEnrichmentStore{stubMediaStore: &stubMediaStore{}, files: files, selections: selections}
	singleHandler := New(Dependencies{MediaStore: singleStore})
	want := singleHandler.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})

	batchStore := &batchEbookEnrichmentStore{singleEbookEnrichmentStore: &singleEbookEnrichmentStore{
		stubMediaStore: &stubMediaStore{}, files: files, selections: selections,
	}}
	batchHandler := New(Dependencies{MediaStore: batchStore})
	got := batchHandler.enrichEbookLibraryItems(context.Background(), append([]LibraryItem(nil), entries...), catalog.AccessFilter{})

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch output differs from single-item fallback\ngot:  %#v\nwant: %#v", got, want)
	}
	if batchStore.batchFile != 1 || batchStore.batchPref != 1 {
		t.Fatalf("batch calls = files:%d preferences:%d, want 1 each", batchStore.batchFile, batchStore.batchPref)
	}
	if batchStore.singleFile != 0 || batchStore.singlePref != 0 {
		t.Fatalf("single-item calls = files:%d preferences:%d, want none", batchStore.singleFile, batchStore.singlePref)
	}
	if singleStore.singleFile != len(entries) || singleStore.singlePref != len(entries) {
		t.Fatalf("fallback calls = files:%d preferences:%d, want %d each", singleStore.singleFile, singleStore.singlePref, len(entries))
	}
}

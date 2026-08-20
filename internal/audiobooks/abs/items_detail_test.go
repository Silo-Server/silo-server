package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestSiloItemToLibraryItemDetail_ExpandedShape guards that GET /items/{id}
// matches real ABS LibraryItem.toOldJSONExpanded + Book.toOldJSONExpanded +
// oldMetadataToJSONExpanded: expanded outer keys, media.size + tracks, and the
// expanded metadata keys (authorName, descriptionPlain, ...).
func TestSiloItemToLibraryItemDetail_ExpandedShape(t *testing.T) {
	item := &models.MediaItem{
		ContentID: "book-7",
		Title:     "The Test",
		Overview:  "<p>Hello <b>world</b></p>",
		People: []models.ItemPerson{
			{Person: models.Person{ID: 5, Name: "Jane Roe"}, Kind: models.PersonKindAuthor},
			{Person: models.Person{ID: 6, Name: "Ann Reader"}, Kind: models.PersonKindNarrator},
		},
	}
	files := []*models.MediaFile{
		{FilePath: "/x/part1.mp3", Duration: 120, FileSize: 4096},
	}

	detail := siloItemToLibraryItemDetail(item, files, AudiobookLibrary{ID: 1, Name: "Audiobooks"}, "http://x")
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}

	outer := []string{
		"id", "ino", "oldLibraryItemId", "libraryId", "folderId", "path",
		"relPath", "isFile", "mtimeMs", "ctimeMs", "birthtimeMs", "addedAt",
		"updatedAt", "lastScan", "scanVersion", "isMissing", "isInvalid",
		"mediaType", "media", "libraryFiles", "size",
	}
	for _, k := range outer {
		if _, ok := m[k]; !ok {
			t.Errorf("expanded item missing outer key %q", k)
		}
	}
	if lf, ok := m["libraryFiles"].([]any); !ok || len(lf) != 1 {
		t.Errorf("libraryFiles = %v, want 1 entry", m["libraryFiles"])
	}
	if sz, _ := m["size"].(float64); sz != 4096 {
		t.Errorf("size = %v, want 4096", m["size"])
	}

	media, _ := m["media"].(map[string]any)
	for _, k := range []string{"id", "libraryItemId", "metadata", "coverPath", "tags", "audioFiles", "chapters", "duration", "size", "tracks"} {
		if _, ok := media[k]; !ok {
			t.Errorf("expanded media missing key %q", k)
		}
	}
	if media["id"] != "book-7" {
		t.Errorf("media.id = %v, want book-7", media["id"])
	}

	meta, _ := media["metadata"].(map[string]any)
	for _, k := range []string{
		"title", "titleIgnorePrefix", "subtitle", "authors", "authorName",
		"authorNameLF", "narrators", "narratorName", "series", "seriesName",
		"genres", "publishedYear", "publishedDate", "publisher", "description",
		"descriptionPlain", "isbn", "asin", "language", "explicit", "abridged",
	} {
		if _, ok := meta[k]; !ok {
			t.Errorf("expanded metadata missing key %q", k)
		}
	}
	if meta["authorName"] != "Jane Roe" {
		t.Errorf("authorName = %v, want Jane Roe", meta["authorName"])
	}
	if meta["narratorName"] != "Ann Reader" {
		t.Errorf("narratorName = %v, want Ann Reader", meta["narratorName"])
	}
	// descriptionPlain strips HTML tags.
	if dp, _ := meta["descriptionPlain"].(string); dp != "Hello world" {
		t.Errorf("descriptionPlain = %q, want %q", dp, "Hello world")
	}
}

func TestSiloEbookToLibraryItemDetail_ExposesReaderFile(t *testing.T) {
	item := &models.MediaItem{ContentID: "ebook-7", Type: mediaTypeEbook, Title: "Reader Test"} //nolint:goconst // Stable fixture label.
	files := []*models.MediaFile{
		{ID: 11, FilePath: "/x/reader-test.pdf", FileSize: 100},
		{ID: 12, FilePath: "/x/reader-test.epub", FileSize: 200},
	}

	detail := siloItemToLibraryItemDetail(item, files, AudiobookLibrary{ID: 17, Name: "Books", Type: mediaTypeEbook}, "http://x")
	if detail.Media.EbookFile == nil {
		t.Fatal("ebookFile is missing")
	}
	if detail.Media.EbookFile.Ino != "12" || detail.Media.EbookFile.EbookFormat != "epub" {
		t.Fatalf("ebookFile = %#v, want preferred EPUB file", detail.Media.EbookFile)
	}
	if detail.Media.EbookFile.Metadata.Filename != "reader-test.epub" || detail.Media.EbookFile.Metadata.Size != 200 {
		t.Fatalf("ebook metadata = %#v", detail.Media.EbookFile.Metadata)
	}
	if detail.Media.NumTracks != 0 || len(detail.Media.Tracks) != 0 || detail.Size != 200 {
		t.Fatalf("ebook detail has audio state: tracks=%d size=%d", detail.Media.NumTracks, detail.Size)
	}
}

func TestHandleItemUsesActualLibraryMembership(t *testing.T) {
	media := &inProgressStubMediaStore{
		libs: []AudiobookLibrary{
			{ID: 17, Name: "First Books", Type: mediaTypeEbook},  //nolint:goconst // Stable fixture label.
			{ID: 18, Name: "Second Books", Type: mediaTypeEbook}, //nolint:goconst // Stable fixture label.
		},
		byID: map[string]*models.MediaItem{
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook, Title: "Reader Test"},
		},
		libraryByID: map[string]int64{testEbookID: 18},
	}
	h := New(Dependencies{MediaStore: media})
	rec := dispatchABSWithParams(http.MethodGet, "/api/items/ebook-1",
		map[string]string{"id": testEbookID}, nil, "1", testProfileID, h.handleItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["libraryId"] != "18" {
		t.Fatalf("libraryId = %v, want 18", got["libraryId"])
	}
}

type staticRecommender struct{ ids []string }

func (r staticRecommender) Similar(context.Context, string, int) ([]string, error) { return r.ids, nil }

func TestHandleSimilarItemsUsesEachItemsActualLibrary(t *testing.T) {
	media := &inProgressStubMediaStore{
		libs: []AudiobookLibrary{
			{ID: 1, Name: "Audio", Type: "audiobooks"}, //nolint:goconst // Persisted library type fixture.
			{ID: 17, Name: "First Books", Type: mediaTypeEbook},
			{ID: 18, Name: "Second Books", Type: mediaTypeEbook},
		},
		byID: map[string]*models.MediaItem{
			testBookID:  {ContentID: testBookID, Type: mediaTypeAudiobook, Title: "Audio"},
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook, Title: "Reader Test"},
		},
		libraryByID: map[string]int64{testBookID: 1, testEbookID: 18},
		files: map[string][]*models.MediaFile{
			testEbookID: {{ID: 7, FilePath: "/books/reader.epub"}},
		},
	}
	h := New(Dependencies{MediaStore: media, Recommender: staticRecommender{ids: []string{testEbookID, testBookID}}})
	rec := dispatchABSWithParams(http.MethodGet, "/api/items/source/similar",
		map[string]string{"id": "source"}, nil, "1", testProfileID, h.handleSimilarItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	results, _ := got["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %#v, want two", got["results"])
	}
	first, _ := results[0].(map[string]any)
	second, _ := results[1].(map[string]any)
	if first["libraryId"] != "18" || second["libraryId"] != "1" {
		t.Fatalf("library IDs = (%v, %v), want (18, 1)", first["libraryId"], second["libraryId"])
	}
	mediaBlock := first["media"].(map[string]any)
	ebookFile, _ := mediaBlock["ebookFile"].(map[string]any)
	if ebookFile["ebookFormat"] != "epub" {
		t.Fatalf("similar ebook file = %v, want epub", mediaBlock["ebookFile"])
	}
}

var _ itemLibraryBatchStore = (*inProgressStubMediaStore)(nil)

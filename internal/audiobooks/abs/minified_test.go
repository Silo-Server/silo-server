package abs

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestMinify_ConformsToRealABSKeys asserts the minified list item matches real
// ABS LibraryItem.toOldJSONMinified + Book.toOldJSONMinified key sets — a
// missing key crashes strict clients, and media.numTracks/numAudioFiles must
// be >= 1 or Plappa drops the item.
func TestMinify_ConformsToRealABSKeys(t *testing.T) {
	item := &models.MediaItem{
		ContentID: testBookID,
		Title:     "Test Book",
		People: []models.ItemPerson{
			{Person: models.Person{ID: 42, Name: "Stephen King"}, Kind: models.PersonKindAuthor},
		},
	}
	lib := AudiobookLibrary{ID: 18, Name: "Audiobooks", Type: "audiobooks"}

	full := siloItemToLibraryItem(item, lib, "http://x")
	min := Minify(full)

	body, err := json.Marshal(min)
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
		"updatedAt", "isMissing", "isInvalid", "mediaType", "media", "numFiles", "size",
	}
	for _, k := range outer {
		if _, ok := m[k]; !ok {
			t.Errorf("minified item missing outer key %q", k)
		}
	}

	media, _ := m["media"].(map[string]any)
	mediaKeys := []string{
		"id", "metadata", "coverPath", "tags", "numTracks", "numAudioFiles",
		"numChapters", "duration", "size", "ebookFormat",
	}
	for _, k := range mediaKeys {
		if _, ok := media[k]; !ok {
			t.Errorf("minified media missing key %q", k)
		}
	}
	if media["id"] != testBookID {
		t.Errorf("media.id = %v, want book-1", media["id"])
	}
	// Plappa guard: never advertise 0 audio files.
	if n, _ := media["numTracks"].(float64); n < 1 {
		t.Errorf("media.numTracks = %v, want >= 1", media["numTracks"])
	}
	if n, _ := media["numAudioFiles"].(float64); n < 1 {
		t.Errorf("media.numAudioFiles = %v, want >= 1", media["numAudioFiles"])
	}

	meta, _ := media["metadata"].(map[string]any)
	metaKeys := []string{
		"title", "titleIgnorePrefix", "subtitle", "authorName", "authorNameLF",
		"narratorName", "seriesName", "genres", "publishedYear", "publishedDate",
		"publisher", "description", "isbn", "asin", "language", "explicit", "abridged",
	}
	for _, k := range metaKeys {
		if _, ok := meta[k]; !ok {
			t.Errorf("minified metadata missing key %q", k)
		}
	}
}

// TestSiloItemToLibraryItem_MediaHasID guards the yaabsa BookMedia.id crash:
// the non-minified media object must carry id + libraryItemId = ContentID.
func TestSiloItemToLibraryItem_MediaHasID(t *testing.T) {
	item := &models.MediaItem{ContentID: "book-9", Title: "T"}
	full := siloItemToLibraryItem(item, AudiobookLibrary{ID: 1}, "http://x")
	if full.Media.ID != "book-9" {
		t.Errorf("media.id = %q, want book-9", full.Media.ID)
	}
	if full.Media.LibraryItemID != "book-9" {
		t.Errorf("media.libraryItemId = %q, want book-9", full.Media.LibraryItemID)
	}
}

func TestMinifyEbookPreservesReaderSignal(t *testing.T) {
	format := "pdf"
	item := LibraryItem{
		ID:           testEbookID,
		LibraryFiles: []map[string]any{{"fileType": mediaTypeEbook}},
		Media: LibraryItemMedia{
			ID:        testEbookID,
			Metadata:  Metadata{Language: testJapanese},
			EbookFile: &EbookFile{EbookFormat: format},
		},
	}

	got := Minify(item)
	if got.Media.EbookFormat == nil || *got.Media.EbookFormat != format {
		t.Fatalf("ebookFormat = %v, want %q", got.Media.EbookFormat, format)
	}
	if got.Media.NumTracks != 0 || got.Media.NumAudioFiles != 0 {
		t.Fatalf("ebook audio counts = (%d, %d), want zero", got.Media.NumTracks, got.Media.NumAudioFiles)
	}
	if got.NumFiles != 1 {
		t.Fatalf("ebook numFiles = %d, want 1", got.NumFiles)
	}
	if got.Media.Metadata.Language != testJapanese {
		t.Fatalf("ebook language = %q, want %q", got.Media.Metadata.Language, testJapanese)
	}
}

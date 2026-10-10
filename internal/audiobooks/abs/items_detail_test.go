package abs

import (
	"encoding/json"
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

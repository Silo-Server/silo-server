package abs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSiloItemToMetadata_AuthorsHaveIDs(t *testing.T) {
	item := &models.MediaItem{
		Title: "Test Book",
		People: []models.ItemPerson{
			{Person: models.Person{ID: 42, Name: "Stephen King"}, Kind: models.PersonKindAuthor},
			{Person: models.Person{ID: 43, Name: "Audie Murphy"}, Kind: models.PersonKindNarrator},
		},
	}
	m := siloItemToMetadata(item)
	if len(m.Authors) != 1 {
		t.Fatalf("authors len = %d, want 1", len(m.Authors))
	}
	if m.Authors[0].ID != "42" {
		t.Errorf("author ID = %q, want %q", m.Authors[0].ID, "42")
	}
	if m.Authors[0].Name != "Stephen King" {
		t.Errorf("author Name = %q, want %q", m.Authors[0].Name, "Stephen King")
	}
}

func TestSiloItemToMetadata_SeriesHaveSlugIDs(t *testing.T) {
	item := &models.MediaItem{
		Title:   "Test Book",
		Studios: []string{"The Dark Tower"},
	}
	m := siloItemToMetadata(item)
	if len(m.Series) != 1 {
		t.Fatalf("series len = %d, want 1", len(m.Series))
	}
	if m.Series[0].ID == "" {
		t.Errorf("series ID is empty; want slugified name")
	}
	if m.Series[0].Name != "The Dark Tower" {
		t.Errorf("series Name = %q, want %q", m.Series[0].Name, "The Dark Tower")
	}
}

func TestSiloItemToMetadata_GenresEmptyArrayNotNil(t *testing.T) {
	item := &models.MediaItem{Title: "Test Book"} // Genres nil
	m := siloItemToMetadata(item)
	if m.Genres == nil {
		t.Errorf("Genres is nil; want empty slice")
	}
	if len(m.Genres) != 0 {
		t.Errorf("Genres len = %d, want 0", len(m.Genres))
	}
}

func TestSiloItemToMetadata_TagsEmptyArrayNotNil(t *testing.T) {
	item := &models.MediaItem{Title: "Test Book"}
	m := siloItemToMetadata(item)
	if m.Tags == nil {
		t.Errorf("Tags is nil; want empty slice")
	}
}

func TestSiloItemToMetadata_NarratorsListed(t *testing.T) {
	item := &models.MediaItem{
		Title: "Test Book",
		People: []models.ItemPerson{
			{Person: models.Person{ID: 1, Name: "Narrator One"}, Kind: models.PersonKindNarrator},
			{Person: models.Person{ID: 2, Name: "Narrator Two"}, Kind: models.PersonKindNarrator},
		},
	}
	m := siloItemToMetadata(item)
	if len(m.Narrators) != 2 {
		t.Fatalf("narrators len = %d, want 2", len(m.Narrators))
	}
	if m.Narrators[0] != "Narrator One" || m.Narrators[1] != "Narrator Two" {
		t.Errorf("narrators = %v, want [Narrator One Narrator Two]", m.Narrators)
	}
}

// TestSiloItemToMetadata_JSONKeysAlwaysPresent guards the omitempty fix:
// 3rd-party clients branch on the presence of "genres" and "tags" keys
// even when the values are empty arrays. Removing omitempty from those
// fields means the keys serialize even when the slice is empty.
func TestSiloItemToMetadata_JSONKeysAlwaysPresent(t *testing.T) {
	item := &models.MediaItem{Title: "Test Book"} // no genres, no tags, no people
	m := siloItemToMetadata(item)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	for _, key := range []string{`"genres":`, `"tags":`, `"authors":`, `"series":`, `"narrators":`} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON missing required key %s; got %s", key, s)
		}
	}
}

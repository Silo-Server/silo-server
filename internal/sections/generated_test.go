package sections

import (
	"testing"
)

func TestParseGeneratedHomeLibraryRecentConfig(t *testing.T) {
	libraryID, ok := ParseGeneratedHomeLibraryRecentConfig(GeneratedHomeLibraryRecentConfig(42))
	if !ok {
		t.Fatalf("expected config to parse")
	}
	if libraryID != 42 {
		t.Fatalf("library id = %d, want 42", libraryID)
	}
}

func TestParseGeneratedHomeLibraryRecentEpisodesConfig(t *testing.T) {
	libraryID, ok := ParseGeneratedHomeLibraryRecentConfig(GeneratedHomeLibraryRecentEpisodesConfig(42))
	if !ok {
		t.Fatalf("expected config to parse")
	}
	if libraryID != 42 {
		t.Fatalf("library id = %d, want 42", libraryID)
	}

	def, err := ParseQueryDefinition(GeneratedHomeLibraryRecentEpisodesConfig(42))
	if err != nil {
		t.Fatalf("ParseQueryDefinition() error = %v", err)
	}
	if def.MediaScope != "episode" {
		t.Fatalf("media_scope = %q, want episode", def.MediaScope)
	}
	if def.Sort.Field != "release_date" || def.Sort.Order != "desc" {
		t.Fatalf("sort = %#v, want release_date desc", def.Sort)
	}
	if len(def.LibraryIDs) != 1 || def.LibraryIDs[0] != 42 {
		t.Fatalf("library_ids = %v, want [42]", def.LibraryIDs)
	}
}

func TestShouldSyncGeneratedHomeLibraryRecentTitle(t *testing.T) {
	section := &PageSection{
		Scope:       "home",
		SectionType: SectionRecentlyAdded,
		Title:       "Recently Added in Movies",
		Config:      GeneratedHomeLibraryRecentConfig(3),
	}
	if !ShouldSyncGeneratedHomeLibraryRecentTitle(section, "Movies") {
		t.Fatalf("expected generated title to sync")
	}

	section.Title = "Staff Picks"
	if ShouldSyncGeneratedHomeLibraryRecentTitle(section, "Movies") {
		t.Fatalf("did not expect custom title to sync")
	}
}

func TestShouldSyncGeneratedHomeLibraryRecentEpisodesTitle(t *testing.T) {
	section := &PageSection{
		Scope:       "home",
		SectionType: SectionCustomFilter,
		Title:       "Recently Released Episodes in Shows",
		Config:      GeneratedHomeLibraryRecentEpisodesConfig(3),
	}
	if !ShouldSyncGeneratedHomeLibraryRecentTitle(section, "Shows") {
		t.Fatalf("expected generated episode title to sync")
	}

	if got := GeneratedHomeLibraryRecentSyncedTitle(section, "TV"); got != "Recently Released Episodes in TV" {
		t.Fatalf("synced title = %q, want %q", got, "Recently Released Episodes in TV")
	}

	section.Title = "Staff Picks"
	if ShouldSyncGeneratedHomeLibraryRecentTitle(section, "Shows") {
		t.Fatalf("did not expect custom title to sync")
	}
}

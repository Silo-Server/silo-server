package sections

import (
	"testing"
)

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

package abs

import "testing"

func TestFilterMatchesGenreAndLanguageMetadata(t *testing.T) {
	item := LibraryItem{Media: LibraryItemMedia{Metadata: Metadata{
		Genres:    []string{"Science Fiction", "Adventure"},
		Language:  testJapanese,
		Publisher: "Tor Books",
	}}}
	if !((Filter{Kind: FilterGenres, Value: "science fiction"}).Matches(item, false, false, false)) {
		t.Fatal("genre filter did not match mapped metadata")
	}
	if !((Filter{Kind: FilterLanguages, Value: "JPN"}).Matches(item, false, false, false)) {
		t.Fatal("language filter did not match mapped metadata")
	}
	if !((Filter{Kind: FilterPublishers, Value: "tor books"}).Matches(item, false, false, false)) {
		t.Fatal("publisher filter did not match mapped metadata")
	}
}

func TestParsePublisherFilter(t *testing.T) {
	filter, ok := ParseFilter("publishers.VG9yIEJvb2tz")
	if !ok || filter.Kind != FilterPublishers || filter.Value != "Tor Books" {
		t.Fatalf("publisher filter = %#v, %v", filter, ok)
	}
}

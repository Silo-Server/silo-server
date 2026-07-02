package catalogseed

import (
	"reflect"
	"testing"
)

func TestCatalogSeedSearchUpsertIDsIncludesChangedItemsAndEmbeddings(t *testing.T) {
	itemStates := map[string]bool{
		"movie-1":  true,
		"movie-2":  false,
		"series-1": true,
	}
	embeddings := []EmbeddingRecord{
		{MediaItemID: " movie-3 "},
		{MediaItemID: "movie-1"},
		{MediaItemID: ""},
	}
	files := []FileRecord{
		{ContentID: " movie-4 "},
		{ContentID: ""},
	}
	links := []LibraryLinkRecord{
		{ContentID: "movie-5"},
		{ContentID: "movie-2"},
	}

	got := catalogSeedSearchUpsertIDs(itemStates, embeddings, files, links)
	want := []string{"movie-1", "movie-2", "movie-3", "movie-4", "movie-5", "series-1"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogSeedSearchUpsertIDs = %#v, want %#v", got, want)
	}
}

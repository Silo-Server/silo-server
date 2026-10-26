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

func TestEmbeddingsForModelKeepsOnlyThatModel(t *testing.T) {
	records := []EmbeddingRecord{
		{MediaItemID: "movie-1", Model: "nomic-embed-text"},
		{MediaItemID: "movie-2", Model: "gemini-embedding-001"},
		{MediaItemID: "movie-3", Model: "nomic-embed-text"},
		{MediaItemID: "movie-4", Model: ""},
	}
	got := embeddingsForModel(records, "nomic-embed-text")
	if len(got) != 2 || got[0].MediaItemID != "movie-1" || got[1].MediaItemID != "movie-3" {
		t.Fatalf("embeddingsForModel = %#v", got)
	}
	if got := embeddingsForModel(records, ""); len(got) != 0 {
		t.Fatalf("an unknown model kept %#v", got)
	}
}

package catalogseed

import (
	"errors"
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

func TestRewriteFileRecordKeepsSidecarsInsideTheLibrary(t *testing.T) {
	rewrites := normalizeRewrites([]PathRewrite{{From: "/old/movies", To: "/media/movies"}})
	folderPaths := map[int][]string{7: {"/media/movies"}}

	tests := []struct {
		name     string
		sidecar  string
		wantPath string
		wantErr  bool
	}{
		{name: "beside the video", sidecar: "/old/movies/Film/Subs/Film.en.ass", wantPath: "/media/movies/Film/Subs/Film.en.ass"},
		{name: "outside the library", sidecar: "/etc/silo/config.ass", wantErr: true},
		{name: "climbs out of the library", sidecar: "/old/movies/Film/../../../etc/passwd", wantErr: true},
		{name: "sibling folder sharing the root's name", sidecar: "/media/movies-private/Film.en.srt", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := FileRecord{
				MediaFolderID:     7,
				FilePath:          "/old/movies/Film/Film.mkv",
				ExternalSubtitles: []ExternalSubtitleRecord{{Path: tt.sidecar, Format: "ass"}},
			}
			got, err := rewriteFileRecord(file, rewrites, folderPaths)
			if tt.wantErr {
				var unmatched *UnmatchedRootsError
				if !errors.As(err, &unmatched) {
					t.Fatalf("rewriteFileRecord error = %v, want *UnmatchedRootsError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("rewriteFileRecord: %v", err)
			}
			if got.ExternalSubtitles[0].Path != tt.wantPath {
				t.Fatalf("sidecar path = %q, want %q", got.ExternalSubtitles[0].Path, tt.wantPath)
			}
		})
	}
}

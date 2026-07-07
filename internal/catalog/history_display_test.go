package catalog

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestHistoryEpisodeScopeIDs(t *testing.T) {
	entries := []userstore.WatchHistoryEntry{
		{MediaItemID: "episode-tmdb-100-1-3"},
		{MediaItemID: "movie-tmdb-555"},
		{MediaItemID: "episode-tmdb-100-1-3"}, // older rewatch collapses into the first
		{MediaItemID: "  "},
		{MediaItemID: "episode-tmdb-100-1-2"},
	}

	got := HistoryEpisodeScopeIDs(entries)
	want := []string{"episode-tmdb-100-1-3", "movie-tmdb-555", "episode-tmdb-100-1-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HistoryEpisodeScopeIDs = %v, want %v", got, want)
	}
}

func TestHistoryEpisodeScopeIDsEmpty(t *testing.T) {
	if got := HistoryEpisodeScopeIDs(nil); len(got) != 0 {
		t.Fatalf("HistoryEpisodeScopeIDs(nil) = %v, want empty", got)
	}
}

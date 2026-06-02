package autoscan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func TestArrHistoryChangedPaths(t *testing.T) {
	// imports contribute importedPath; renames contribute both new path and old
	// sourcePath; unrelated events (grabbed, episodeFileDeleted) are ignored.
	body := `[
	  {"eventType":"downloadFolderImported","data":{"importedPath":"/mnt/media/Movies/Dune (2021)/Dune.mkv"}},
	  {"eventType":"grabbed","data":{"importedPath":"/should/be/ignored"}},
	  {"eventType":"episodeFileRenamed","data":{"path":"/mnt/media/Show/S01/E01 new.mkv","sourcePath":"/mnt/media/Show/S01/E01 old.mkv"}},
	  {"eventType":"movieFileRenamed","data":{"path":"/mnt/media/Movies/Heat/Heat new.mkv","sourcePath":"/mnt/media/Movies/Heat/Heat old.mkv"}},
	  {"eventType":"episodeFileDeleted","data":{"reason":"Upgrade"}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/history/since" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("date") == "" {
			t.Errorf("missing date param")
		}
		// AUTH ASSERTION: arrclient uses X-Api-Key header (confirmed in client.go:97).
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("missing/incorrect api key header")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewArrHistoryClient(nil)
	paths, err := c.ChangedPaths(context.Background(), srv.URL, "k", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	sort.Strings(paths)
	want := []string{
		"/mnt/media/Movies/Dune (2021)/Dune.mkv",
		"/mnt/media/Movies/Heat/Heat new.mkv",
		"/mnt/media/Movies/Heat/Heat old.mkv",
		"/mnt/media/Show/S01/E01 new.mkv",
		"/mnt/media/Show/S01/E01 old.mkv",
	}
	if len(paths) != len(want) {
		t.Fatalf("ChangedPaths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("ChangedPaths = %v, want %v", paths, want)
		}
	}
}

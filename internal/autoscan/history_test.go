package autoscan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func TestArrHistoryImportedPaths(t *testing.T) {
	body := `[
	  {"eventType":"downloadFolderImported","data":{"importedPath":"/mnt/media/Movies/Dune (2021)/Dune.mkv"}},
	  {"eventType":"grabbed","data":{"importedPath":"/should/be/ignored"}},
	  {"eventType":"downloadFolderImported","data":{"importedPath":"/mnt/media/Show/S01/E01.mkv"}}
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
	paths, err := c.ImportedPaths(context.Background(), srv.URL, "k", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ImportedPaths: %v", err)
	}
	sort.Strings(paths)
	want := []string{"/mnt/media/Movies/Dune (2021)/Dune.mkv", "/mnt/media/Show/S01/E01.mkv"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("ImportedPaths = %v, want %v", paths, want)
	}
}

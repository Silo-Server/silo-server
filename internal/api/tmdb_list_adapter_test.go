package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// tmdbListTestServer serves list 310 with entries alternating movie/tv and
// answers /{movie,tv}/{id}/external_ids. onLookup, when set, runs before each
// external-ID response.
func tmdbListTestServer(entries int, onLookup func()) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.URL.Path == "/list/310":
			items := make([]string, 0, entries)
			for i := 1; i <= entries; i++ {
				kind := "movie"
				if i%2 == 0 {
					kind = "tv"
				}
				items = append(items, fmt.Sprintf(`{"id":%d,"media_type":%q,"title":"Entry %d"}`, i, kind, i))
			}
			_, _ = fmt.Fprintf(w, `{"page":1,"total_pages":1,"items":[%s]}`, strings.Join(items, ","))
		case len(parts) == 3 && parts[2] == "external_ids":
			if onLookup != nil {
				onLookup()
			}
			id, _ := strconv.Atoi(parts[1])
			_, _ = fmt.Fprintf(w, `{"imdb_id":"tt%07d","tvdb_id":%d}`, id, id*10)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestTMDBListAdapterKeepsListOrderWithExternalIDs(t *testing.T) {
	server := tmdbListTestServer(20, nil)
	defer server.Close()
	client := tmdb.NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	entries, err := (&tmdbListAdapter{client: client}).GetList(t.Context(), 310, 0)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if len(entries) != 20 {
		t.Fatalf("entries = %d, want 20", len(entries))
	}
	for i, entry := range entries {
		id := i + 1
		if entry.ID != id || entry.IMDbID != fmt.Sprintf("tt%07d", id) || entry.TVDBID != id*10 {
			t.Fatalf("entries[%d] = %+v, want list position %d with its external IDs", i, entry, id)
		}
	}
}

func TestTMDBListAdapterStopsWhenCancelledDuringLookups(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	server := tmdbListTestServer(20, func() { once.Do(cancel) })
	defer server.Close()
	client := tmdb.NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	if _, err := (&tmdbListAdapter{client: client}).GetList(ctx, 310, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetList error = %v, want context.Canceled", err)
	}
}

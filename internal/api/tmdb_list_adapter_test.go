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
	"sync/atomic"
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

// GetList keeps list order and makes no per-entry lookups; the matcher asks
// for external IDs only for entries their TMDB ID leaves unmatched.
func TestTMDBListAdapterReadsListWithoutLookups(t *testing.T) {
	var lookups atomic.Int64
	server := tmdbListTestServer(20, func() { lookups.Add(1) })
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
		if entry.ID != i+1 || entry.IMDbID != "" || entry.TVDBID != 0 {
			t.Fatalf("entries[%d] = %+v, want list position %d without external IDs", i, entry, i+1)
		}
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("GetList made %d external-ID lookups, want 0", n)
	}
}

func TestTMDBEnrichExternalIDsFillsEntriesInPlace(t *testing.T) {
	server := tmdbListTestServer(20, nil)
	defer server.Close()
	client := tmdb.NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)
	adapter := &tmdbListAdapter{client: client}

	entries, err := adapter.GetList(t.Context(), 310, 0)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if err := adapter.EnrichExternalIDs(t.Context(), entries); err != nil {
		t.Fatalf("EnrichExternalIDs: %v", err)
	}
	for i, entry := range entries {
		id := i + 1
		if entry.ID != id || entry.IMDbID != fmt.Sprintf("tt%07d", id) || entry.TVDBID != id*10 {
			t.Fatalf("entries[%d] = %+v, want list position %d with its external IDs", i, entry, id)
		}
	}
}

func TestTMDBEnrichExternalIDsStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	server := tmdbListTestServer(20, func() { once.Do(cancel) })
	defer server.Close()
	client := tmdb.NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)
	adapter := &tmdbListAdapter{client: client}

	entries, err := adapter.GetList(t.Context(), 310, 0)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if err := adapter.EnrichExternalIDs(ctx, entries); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnrichExternalIDs error = %v, want context.Canceled", err)
	}
}

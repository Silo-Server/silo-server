package historyimport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// A systematic failure yields one warning per record; the run keeps only as
// many as it can store.
func TestRunWarningsStopAtTheStoredLimit(t *testing.T) {
	t.Parallel()
	var summary ExecutionSummary
	for range maxStoredWarnings * 100 {
		addRunWarning(&summary, "the same failure")
	}
	if len(summary.Warnings) != maxStoredWarnings {
		t.Fatalf("warnings = %d, want %d", len(summary.Warnings), maxStoredWarnings)
	}
}

// Lookups keep a fixed number of entries however many distinct keys pass.
func TestLookupCacheStaysAtCapacity(t *testing.T) {
	t.Parallel()
	cache := newLookupCache[int](3)
	for i, key := range []string{"a", "b", "c", "d"} {
		cache.put(key, i)
	}
	if _, ok := cache.get("a"); ok {
		t.Fatal("oldest entry survived past capacity")
	}
	if got, ok := cache.get("d"); !ok || got != 3 {
		t.Fatalf("newest = %d, %v", got, ok)
	}
	if len(cache.items) != 3 {
		t.Fatalf("entries = %d", len(cache.items))
	}
}

// peakHeapDuring returns the most heap in use, sampled while fn runs, after a
// collection, so garbage awaiting collection does not count.
func peakHeapDuring(fn func(sample func())) uint64 {
	var peak uint64
	sample := func() {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		peak = max(peak, stats.HeapAlloc)
	}
	sample()
	fn(sample)
	return peak
}

// historyPages serves a synthetic source of total watched items. Each page is
// generated on request, so the fake's own memory does not grow with total.
type syntheticSource struct {
	total int
}

func (s syntheticSource) slice(start, limit int) (int, int) {
	start = min(max(start, 0), s.total)
	return start, min(start+limit, s.total)
}

func (s syntheticSource) plexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/library/sections":
		_, _ = io.WriteString(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"show","title":"TV"}]}}`)
	case strings.HasPrefix(r.URL.Path, "/library/metadata/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/library/metadata/"), ",")
		var items []string
		for _, key := range parts {
			items = append(items, fmt.Sprintf(`{"ratingKey":%q,"type":"show","title":"Show","year":2001,"Guid":[{"id":"tvdb://%s"}]}`, key, key))
		}
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[%s]}}`, strings.Join(items, ","))
	case q.Get("inProgress") == "1":
		_, _ = io.WriteString(w, `{"MediaContainer":{"totalSize":0,"Metadata":[]}}`)
	default:
		offset, _ := strconv.Atoi(q.Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(q.Get("X-Plex-Container-Size"))
		s.plexPage(w, r, offset, size)
	}
}

func (s syntheticSource) plexPage(w http.ResponseWriter, _ *http.Request, offset, size int) {
	start, end := s.slice(offset, size)
	_, _ = fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[`, s.total)
	for i := start; i < end; i++ {
		if i > start {
			_, _ = io.WriteString(w, ",")
		}
		_, _ = fmt.Fprintf(w, `{"ratingKey":"%d","type":"episode","title":"Episode","grandparentTitle":"Show","grandparentRatingKey":"%d","parentIndex":1,"index":%d,"duration":1000,"viewCount":1,"lastViewedAt":1700000000,"viewedAt":1700000000,"Guid":[{"id":"tvdb://%d"}]}`, i+1, i%2000+1000000, i+1, i+1)
	}
	_, _ = io.WriteString(w, `]}}`)
}

func (s syntheticSource) adminHandler(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/status/sessions/history") {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(q.Get("X-Plex-Container-Size"))
		s.plexPage(w, r, offset, size)
		return
	}
	s.plexHandler(w, r)
}

func (s syntheticSource) mediaBrowserItems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	if ids := q.Get("Ids"); ids != "" {
		var items []string
		for _, id := range strings.Split(ids, ",") {
			items = append(items, fmt.Sprintf(`{"Id":%q,"Type":"Series","Name":"Show","ProviderIds":{"Tvdb":"1"}}`, id))
		}
		_, _ = fmt.Fprintf(w, `{"Items":[%s]}`, strings.Join(items, ","))
		return
	}
	if q.Get("Filters") != "IsPlayed" {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
		return
	}
	offset, _ := strconv.Atoi(q.Get("StartIndex"))
	limit, _ := strconv.Atoi(q.Get("Limit"))
	start, end := s.slice(offset, limit)
	_, _ = fmt.Fprintf(w, `{"TotalRecordCount":%d,"Items":[`, s.total)
	for i := start; i < end; i++ {
		if i > start {
			_, _ = io.WriteString(w, ",")
		}
		_, _ = fmt.Fprintf(w, `{"Id":"item-%d","Name":"Episode","Type":"Episode","SeriesId":"series-%d","SeriesName":"Show","ParentIndexNumber":1,"IndexNumber":%d,"RunTimeTicks":1000000000,"UserData":{"Played":true,"PlayCount":1,"LastPlayedDate":"2026-09-02T00:00:00Z"},"ProviderIds":{"Tvdb":"%d"}}`, i, i%2000, i+1, i)
	}
	_, _ = io.WriteString(w, `]}`)
}

// Peak heap must not depend on history size: the same source at ten times the
// length uses about the same memory. Not parallel, because it measures heap.
func TestProviderPeakHeapIsIndependentOfHistorySize(t *testing.T) {
	if testing.Short() {
		t.Skip("streams a million fake items")
	}
	ctx := trustLoopback(context.Background())
	providers := []struct {
		name    string
		handler func(syntheticSource) http.HandlerFunc
		make    func(url string) Provider
	}{
		{"plex", func(s syntheticSource) http.HandlerFunc { return s.plexHandler },
			func(url string) Provider {
				return NewPlexServerProvider(newUnthrottledPlexClient(), []string{url}, "t")
			}},
		{"plex admin", func(s syntheticSource) http.HandlerFunc { return s.adminHandler },
			func(url string) Provider { return NewPlexAdminProvider(newUnthrottledPlexClient(), url, "t", "1") }},
		{"emby", func(s syntheticSource) http.HandlerFunc { return s.mediaBrowserItems },
			func(url string) Provider {
				client := NewEmbyClient()
				client.limiter = nil
				return NewEmbyProvider(client, embyLocalAuth{BaseURL: url, UserID: "u", AccessToken: "t"})
			}},
		{"jellyfin", func(s syntheticSource) http.HandlerFunc { return s.mediaBrowserItems },
			func(url string) Provider {
				return NewJellyfinProvider(newUnthrottledJellyfinClient(), jellyfinLocalAuth{BaseURL: url, UserID: "u", AccessToken: "t"})
			}},
	}
	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			peak := func(total int) (uint64, int) {
				server := httptest.NewServer(tc.handler(syntheticSource{total: total}))
				defer server.Close()
				delivered := 0
				heap := peakHeapDuring(func(sample func()) {
					err := tc.make(server.URL).Stream(withRunBudget(ctx), nil, func(batch RecordBatch) error {
						delivered += len(batch.Records)
						if delivered%50_000 < len(batch.Records) {
							sample()
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				})
				return heap, delivered
			}
			small, n := peak(100_000)
			if n != 100_000 {
				t.Fatalf("delivered %d of 100000", n)
			}
			large, n := peak(1_000_000)
			if n != 1_000_000 {
				t.Fatalf("delivered %d of 1000000", n)
			}
			t.Logf("peak heap: 100k=%d MiB, 1M=%d MiB", small>>20, large>>20)
			if large > small+16<<20 {
				t.Fatalf("peak heap grew with history: 100k=%d MiB, 1M=%d MiB", small>>20, large>>20)
			}
		})
	}
}

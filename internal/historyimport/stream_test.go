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

// syntheticSource serves a synthetic source of total watched items. Each page is
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

// A title large enough to distinguish retained records from a few live pages.
var syntheticEpisodeTitle = strings.Repeat("episode ", 64)

func (s syntheticSource) plexPage(w http.ResponseWriter, _ *http.Request, offset, size int) {
	start, end := s.slice(offset, size)
	_, _ = fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[`, s.total)
	for i := start; i < end; i++ {
		if i > start {
			_, _ = io.WriteString(w, ",")
		}
		_, _ = fmt.Fprintf(w, `{"ratingKey":"%d","type":"episode","title":%q,"grandparentTitle":"Show","grandparentRatingKey":"%d","parentIndex":1,"index":%d,"duration":1000,"viewCount":1,"lastViewedAt":1700000000,"viewedAt":1700000000,"Guid":[{"id":"tvdb://%d"}]}`, i+1, syntheticEpisodeTitle, i%2000+1000000, i+1, i+1)
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
		_, _ = fmt.Fprintf(w, `{"Id":"item-%d","Name":%q,"Type":"Episode","SeriesId":"series-%d","SeriesName":"Show","ParentIndexNumber":1,"IndexNumber":%d,"RunTimeTicks":1000000000,"UserData":{"Played":true,"PlayCount":1,"LastPlayedDate":"2026-09-02T00:00:00Z"},"ProviderIds":{"Tvdb":"%d"}}`, i, syntheticEpisodeTitle, i%2000, i+1, i)
	}
	_, _ = io.WriteString(w, `]}`)
}

// Watched records are released as pages are applied. Plex admin also keeps
// compact deduplication keys. Run serially so heap samples include little
// unrelated activity.
func TestProviderPeakHeapIsIndependentOfHistorySize(t *testing.T) {
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
						if len(batch.Records) > 0 && (delivered == total || delivered%5_000 < len(batch.Records)) {
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
			const smallHistory, largeHistory = 2_000, 25_000
			small, n := peak(smallHistory)
			if n != smallHistory {
				t.Fatalf("delivered %d of %d", n, smallHistory)
			}
			large, n := peak(largeHistory)
			if n != largeHistory {
				t.Fatalf("delivered %d of %d", n, largeHistory)
			}
			t.Logf("peak heap: %d records=%d MiB, %d records=%d MiB", smallHistory, small>>20, largeHistory, large>>20)
			if large > small+5<<20 {
				t.Fatalf("peak heap grew with history: %d records=%d MiB, %d records=%d MiB", smallHistory, small>>20, largeHistory, large>>20)
			}
		})
	}
}

// A resumed admin import reads earlier pages again to learn which items it has
// imported, so an older play past the checkpoint stays out of history.
func TestPlexAdminResumeKeepsOlderPlaysOut(t *testing.T) {
	t.Parallel()
	rows := []string{
		`{"ratingKey":"1","type":"movie","viewedAt":300,"Guid":[{"id":"tmdb://1"}]}`,
		`{"ratingKey":"1","type":"movie","viewedAt":200,"Guid":[{"id":"tmdb://1"}]}`,
		`{"ratingKey":"2","type":"movie","viewedAt":100,"Guid":[{"id":"tmdb://2"}]}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[%s]}}`, len(rows), rows[start])
	}))
	t.Cleanup(server.Close)

	var got []string
	provider := NewPlexAdminProvider(newUnthrottledPlexClient(), server.URL, "t", "1")
	err := provider.Stream(trustLoopback(context.Background()), &StreamPosition{Stage: plexStageHistory, Offset: 1}, func(batch RecordBatch) error {
		for _, record := range batch.Records {
			got = append(got, record.ExternalID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "2" {
		t.Fatalf("records = %v, want only the item first seen past the checkpoint", got)
	}
}

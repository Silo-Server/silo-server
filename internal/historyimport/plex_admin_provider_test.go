package historyimport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlexAdminProviderTreatsPlexOnlyGuidAsUnresolved(t *testing.T) {
	t.Parallel()

	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"9","type":"movie","title":"Diuna","year":2021,"Guid":"plex://movie/abc"}
			]}}`)
		case "/library/metadata/9":
			metadataCalls++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"9","type":"movie","Guid":[{"id":"tmdb://438631"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(trustLoopback(context.Background()))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 || metadataCalls != 1 || len(records) != 1 || records[0].TMDBID != "438631" {
		t.Fatalf("records = %+v, warnings = %v, metadata calls = %d", records, warnings, metadataCalls)
	}
}

func TestPlexAdminProviderAbortsMetadataSweepOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(trustLoopback(context.Background()))
	defer cancel()
	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/status/sessions/history/all":
			var history strings.Builder
			history.WriteString(`{"MediaContainer":{"totalSize":200,"Metadata":[`)
			for i := 1; i <= 200; i++ {
				if i > 1 {
					history.WriteByte(',')
				}
				_, _ = fmt.Fprintf(&history, `{"ratingKey":"%d","type":"movie","title":"Film %d"}`, i, i)
			}
			history.WriteString(`]}}`)
			_, _ = fmt.Fprint(w, history.String())
		case strings.HasPrefix(r.URL.Path, "/library/metadata/"):
			metadataCalls++
			// The run is canceled while the first batch is in flight.
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, _, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if records != nil {
		t.Fatalf("records = %d, want none after cancellation", len(records))
	}
	if metadataCalls != 1 {
		t.Fatalf("metadata calls = %d, want the sweep to stop at the first canceled request", metadataCalls)
	}
}

func newUnthrottledPlexClient() *PlexClient {
	client := NewPlexClient()
	client.limiter = nil
	return client
}

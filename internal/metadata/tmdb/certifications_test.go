package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetCertificationsPreservesCountryAndStrictestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/1/release_dates":
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"AU","release_dates":[{"certification":"PG","type":3},{"certification":"MA 15+","type":4},{"certification":"","type":1}]},{"iso_3166_1":"US","release_dates":[{"certification":"PG-13","type":3}]}]}`))
		case "/tv/2/content_ratings":
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"AU","rating":"M"},{"iso_3166_1":"US","rating":"TV-MA"}]}`))
		default:
			http.Error(w, "failed", http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	client := NewClient("test", 40)
	defer client.Close()
	client.SetBaseURL(server.URL)
	for _, tc := range []struct {
		kind   string
		id     int
		au, us string
	}{{"movie", 1, "MA15+", "PG-13"}, {"series", 2, "M", "TV-MA"}} {
		got, err := client.GetCertifications(context.Background(), tc.kind, tc.id)
		if err != nil || got["AU"] != tc.au || got["US"] != tc.us {
			t.Fatalf("%s: %v, %v", tc.kind, got, err)
		}
	}
	if _, err := client.GetCertifications(context.Background(), "movie", 3); err == nil {
		t.Fatal("provider failure treated as missing ratings")
	}
}

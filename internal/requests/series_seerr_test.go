package requests

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// seerrSeriesJSON is how Seerr answers /api/v1/tv/{tmdbId}, trimmed.
const seerrSeriesJSON = `{"id": 60059, "mediaInfo": {
	"seasons": [
		{"seasonNumber": 0, "status": 5, "status4k": 1},
		{"seasonNumber": 1, "status": 7, "status4k": 1},
		{"seasonNumber": 2, "status": 4, "status4k": 1},
		{"seasonNumber": 3, "status": 1, "status4k": 3}
	],
	"requests": [
		{"status": 1, "seasons": [{"seasonNumber": 4, "status": 1}]},
		{"status": 3, "seasons": [{"seasonNumber": 5, "status": 3}]},
		{"status": 2, "seasons": [{"seasonNumber": 6, "status": 2}, {"seasonNumber": 7, "status": 4}]}
	]
}}`

func TestFindSeerrSeriesAndMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tv/60059" || r.Header.Get("X-Api-Key") != "key" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(seerrSeriesJSON))
	}))
	defer srv.Close()

	media, err := findSeerrSeries(t.Context(), srv.URL+"/", "key", 60059)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]bool{}
	mergeSeerrSeasons(got, media)
	// Specials, deleted seasons and declined or failed requests stay out.
	want := map[int]bool{2: true, 3: true, 4: true, 6: true}
	if len(got) != len(want) {
		t.Fatalf("requested = %v, want %v", got, want)
	}
	for season := range want {
		if !got[season] {
			t.Fatalf("requested = %v, want %v", got, want)
		}
	}
}

func TestMergeSeerrSeasonsUnknownSeries(t *testing.T) {
	got := map[int]bool{}
	mergeSeerrSeasons(got, &seerrSeries{})
	mergeSeerrSeasons(got, nil)
	if len(got) != 0 {
		t.Fatalf("requested = %v, want none", got)
	}
}

func TestFindSeerrSeriesReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := findSeerrSeries(t.Context(), srv.URL, "bad", 1); err == nil {
		t.Fatal("a 403 must be an error")
	}
}

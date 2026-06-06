package introdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestFetchEpisodeSendsTVDBWhenNoTMDB(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"episode"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	if _, err := c.FetchEpisode(context.Background(), "", "55555", "tt1234567", 2, 3, 0); err != nil {
		t.Fatalf("FetchEpisode: %v", err)
	}
	if gotQuery.Get("tvdb_id") != "55555" {
		t.Errorf("tvdb_id = %q, want 55555", gotQuery.Get("tvdb_id"))
	}
	if gotQuery.Get("tmdb_id") != "" {
		t.Errorf("tmdb_id = %q, want empty", gotQuery.Get("tmdb_id"))
	}
	if gotQuery.Get("imdb_id") != "" {
		t.Errorf("imdb_id should be omitted when tvdb present, got %q", gotQuery.Get("imdb_id"))
	}
}

func TestFetchEpisodePrefersTMDBOverTVDBAndIMDB(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"episode"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	if _, err := c.FetchEpisode(context.Background(), "111", "222", "tt333", 1, 1, 0); err != nil {
		t.Fatalf("FetchEpisode: %v", err)
	}
	if gotQuery.Get("tmdb_id") != "111" {
		t.Errorf("tmdb_id = %q, want 111", gotQuery.Get("tmdb_id"))
	}
	if gotQuery.Get("tvdb_id") != "" || gotQuery.Get("imdb_id") != "" {
		t.Errorf("only tmdb_id expected, got tvdb=%q imdb=%q", gotQuery.Get("tvdb_id"), gotQuery.Get("imdb_id"))
	}
}

func TestFetchMovieSendsTVDB(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"movie"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	if _, err := c.FetchMovie(context.Background(), "", "888", "", 0); err != nil {
		t.Fatalf("FetchMovie: %v", err)
	}
	if gotQuery.Get("tvdb_id") != "888" {
		t.Errorf("tvdb_id = %q, want 888", gotQuery.Get("tvdb_id"))
	}
}

func TestFetchEpisodeCachesByID(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"type":"episode"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.FetchEpisode(context.Background(), "", "999", "", 1, 1, 0); err != nil {
			t.Fatalf("FetchEpisode: %v", err)
		}
	}
	if hits != 1 {
		t.Fatalf("server hits = %d, want 1 (cached after first)", hits)
	}
}

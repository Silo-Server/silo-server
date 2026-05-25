package radarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

func TestSubmitMovieAddsLookupResult(t *testing.T) {
	qualityProfileID := 7
	var posted movieResource
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "radarr-key" {
			t.Fatalf("X-Api-Key = %q, want radarr-key", got)
		}
		switch r.URL.Path {
		case "/api/v3/movie":
			if r.Method == http.MethodGet {
				if got := r.URL.Query().Get("tmdbId"); got != "550" {
					t.Fatalf("tmdbId = %q, want 550", got)
				}
				w.Write([]byte(`[]`))
				return
			}
			if r.Method == http.MethodPost {
				if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
					t.Fatalf("decode posted movie: %v", err)
				}
				w.Write([]byte(`{"id":42,"tmdbId":550}`))
				return
			}
		case "/api/v3/movie/lookup/tmdb":
			if got := r.URL.Query().Get("tmdbId"); got != "550" {
				t.Fatalf("lookup tmdbId = %q, want 550", got)
			}
			w.Write([]byte(`{"title":"Fight Club","tmdbId":550,"titleSlug":"fight-club"}`))
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	client := NewClient(server.Client())
	result, err := client.SubmitMovie(context.Background(), mediarequests.Request{
		MediaType: mediarequests.MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	}, mediarequests.Integration{
		Kind:             "radarr",
		BaseURL:          server.URL,
		APIKeyRef:        "radarr-key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
		Options: map[string]any{
			"search_on_add": false,
		},
	})
	if err != nil {
		t.Fatalf("SubmitMovie returned error: %v", err)
	}
	if result.ExternalID != "42" || result.IntegrationKind != "radarr" {
		t.Fatalf("result = %+v, want radarr external id 42", result)
	}
	if posted.RootFolderPath != "/movies" || posted.QualityProfileID != qualityProfileID {
		t.Fatalf("posted movie = %+v, missing root folder/quality profile", posted)
	}
	if posted.AddOptions.SearchForMovie {
		t.Fatalf("searchForMovie = true, want false")
	}
}

func TestSubmitMovieRecoversFromEmptyAddResponse(t *testing.T) {
	qualityProfileID := 7
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup/tmdb":
			w.Write([]byte(`{"title":"Fight Club","tmdbId":550,"titleSlug":"fight-club"}`))
		case "/api/v3/movie":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == http.MethodGet {
				if got := r.URL.Query().Get("tmdbId"); got != "550" {
					t.Fatalf("tmdbId = %q, want 550", got)
				}
				w.Write([]byte(`[{"id":99,"tmdbId":550,"title":"Fight Club"}]`))
				return
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	result, err := client.SubmitMovie(context.Background(), mediarequests.Request{
		MediaType: mediarequests.MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	}, mediarequests.Integration{
		Kind:             "radarr",
		BaseURL:          server.URL,
		APIKeyRef:        "radarr-key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
	})
	if err != nil {
		t.Fatalf("SubmitMovie returned error: %v", err)
	}
	if result.ExternalID != "99" {
		t.Fatalf("ExternalID = %q, want 99 (recovered after empty 201)", result.ExternalID)
	}
	if result.ExternalStatus != "queued" {
		t.Fatalf("ExternalStatus = %q, want queued", result.ExternalStatus)
	}
}

func TestSubmitMovieFallsBackWhenEmptyResponseAndLookupFails(t *testing.T) {
	qualityProfileID := 7
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup/tmdb":
			w.Write([]byte(`{"title":"Fight Club","tmdbId":550,"titleSlug":"fight-club"}`))
		case "/api/v3/movie":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == http.MethodGet {
				w.Write([]byte(`[]`))
				return
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	result, err := client.SubmitMovie(context.Background(), mediarequests.Request{
		MediaType: mediarequests.MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	}, mediarequests.Integration{
		Kind:             "radarr",
		BaseURL:          server.URL,
		APIKeyRef:        "radarr-key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
	})
	if err != nil {
		t.Fatalf("SubmitMovie returned error: %v", err)
	}
	if result.ExternalStatus != "accepted_without_response" {
		t.Fatalf("ExternalStatus = %q, want accepted_without_response", result.ExternalStatus)
	}
}

func TestCheckMovieStatusReadsQueueDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "radarr-key" {
			t.Fatalf("X-Api-Key = %q, want radarr-key", got)
		}
		if r.URL.Path != "/api/v3/queue/details" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("movieId"); got != "42" {
			t.Fatalf("movieId = %q, want 42", got)
		}
		w.Write([]byte(`[{"movieId":42,"status":"downloading","trackedDownloadState":"downloading"}]`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	status, err := client.CheckMovieStatus(context.Background(), mediarequests.Request{
		MediaType:  mediarequests.MediaTypeMovie,
		TMDBID:     550,
		ExternalID: "42",
	}, mediarequests.Integration{
		Kind:      "radarr",
		BaseURL:   server.URL,
		APIKeyRef: "radarr-key",
	})
	if err != nil {
		t.Fatalf("CheckMovieStatus returned error: %v", err)
	}
	if status.Status != mediarequests.StatusDownloading || status.ExternalStatus != "downloading/downloading" {
		t.Fatalf("status = %+v, want downloading", status)
	}
}

func TestListMovieIntegrationOptionsLoadsChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "radarr-key" {
			t.Fatalf("X-Api-Key = %q, want radarr-key", got)
		}
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"path":"/movies","freeSpace":123,"totalSpace":456,"accessible":true}]`))
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":7,"name":"HD-1080p"}]`))
		case "/api/v3/tag":
			w.Write([]byte(`[{"id":2,"label":"requests"}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	options, err := client.ListMovieIntegrationOptions(context.Background(), mediarequests.Integration{
		Kind:      "radarr",
		BaseURL:   server.URL,
		APIKeyRef: "radarr-key",
	})
	if err != nil {
		t.Fatalf("ListMovieIntegrationOptions returned error: %v", err)
	}
	if options.Kind != "radarr" {
		t.Fatalf("kind = %q, want radarr", options.Kind)
	}
	if len(options.RootFolders) != 1 || options.RootFolders[0].Path != "/movies" {
		t.Fatalf("root folders = %+v, want /movies", options.RootFolders)
	}
	if len(options.QualityProfiles) != 1 || options.QualityProfiles[0].ID != 7 {
		t.Fatalf("quality profiles = %+v, want id 7", options.QualityProfiles)
	}
	if len(options.Tags) != 1 || options.Tags[0].ID != 2 {
		t.Fatalf("tags = %+v, want id 2", options.Tags)
	}
}

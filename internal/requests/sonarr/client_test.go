package sonarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

func TestSubmitSeriesAddsLookupResult(t *testing.T) {
	qualityProfileID := 3
	tvdbID := 121361
	var posted seriesResource
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "sonarr-key" {
			t.Fatalf("X-Api-Key = %q, want sonarr-key", got)
		}
		switch r.URL.Path {
		case "/api/v3/series":
			if r.Method == http.MethodGet {
				if got := r.URL.Query().Get("tvdbId"); got != "121361" {
					t.Fatalf("tvdbId = %q, want 121361", got)
				}
				w.Write([]byte(`[]`))
				return
			}
			if r.Method == http.MethodPost {
				if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
					t.Fatalf("decode posted series: %v", err)
				}
				w.Write([]byte(`{"id":24,"tvdbId":121361}`))
				return
			}
		case "/api/v3/series/lookup":
			if got := r.URL.Query().Get("term"); got != "tvdb:121361" {
				t.Fatalf("lookup term = %q, want tvdb:121361", got)
			}
			w.Write([]byte(`[{"title":"Game of Thrones","tvdbId":121361,"titleSlug":"game-of-thrones"}]`))
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	client := NewClient(server.Client())
	result, err := client.SubmitSeries(context.Background(), mediarequests.Request{
		MediaType: mediarequests.MediaTypeSeries,
		TMDBID:    1399,
		TVDBID:    &tvdbID,
		Title:     "Game of Thrones",
	}, mediarequests.Integration{
		Kind:             "sonarr",
		BaseURL:          server.URL,
		APIKeyRef:        "sonarr-key",
		RootFolder:       "/series",
		QualityProfileID: &qualityProfileID,
		Options: map[string]any{
			"monitor": "future",
		},
	})
	if err != nil {
		t.Fatalf("SubmitSeries returned error: %v", err)
	}
	if result.ExternalID != "24" || result.IntegrationKind != "sonarr" {
		t.Fatalf("result = %+v, want sonarr external id 24", result)
	}
	if posted.RootFolderPath != "/series" || posted.QualityProfileID != qualityProfileID {
		t.Fatalf("posted series = %+v, missing root folder/quality profile", posted)
	}
	if posted.AddOptions.Monitor != "future" {
		t.Fatalf("monitor = %q, want future", posted.AddOptions.Monitor)
	}
}

func TestSubmitSeriesRecoversFromEmptyAddResponse(t *testing.T) {
	qualityProfileID := 3
	tvdbID := 121361
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Game of Thrones","tvdbId":121361,"titleSlug":"game-of-thrones"}]`))
		case "/api/v3/series":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == http.MethodGet {
				if got := r.URL.Query().Get("tvdbId"); got != "121361" {
					t.Fatalf("tvdbId = %q, want 121361", got)
				}
				w.Write([]byte(`[{"id":77,"tvdbId":121361,"title":"Game of Thrones"}]`))
				return
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	result, err := client.SubmitSeries(context.Background(), mediarequests.Request{
		MediaType: mediarequests.MediaTypeSeries,
		TMDBID:    1399,
		TVDBID:    &tvdbID,
		Title:     "Game of Thrones",
	}, mediarequests.Integration{
		Kind:             "sonarr",
		BaseURL:          server.URL,
		APIKeyRef:        "sonarr-key",
		RootFolder:       "/series",
		QualityProfileID: &qualityProfileID,
	})
	if err != nil {
		t.Fatalf("SubmitSeries returned error: %v", err)
	}
	if result.ExternalID != "77" {
		t.Fatalf("ExternalID = %q, want 77 (recovered after empty 201)", result.ExternalID)
	}
	if result.ExternalStatus != "queued" {
		t.Fatalf("ExternalStatus = %q, want queued", result.ExternalStatus)
	}
}

func TestSubmitSeriesRejectsNonExactTVDBLookupMatch(t *testing.T) {
	qualityProfileID := 3
	tvdbID := 121361
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/series/lookup" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		w.Write([]byte(`[{"title":"Wrong Show","tvdbId":999,"titleSlug":"wrong-show"}]`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	_, err := client.SubmitSeries(context.Background(), mediarequests.Request{
		MediaType: mediarequests.MediaTypeSeries,
		TMDBID:    1399,
		TVDBID:    &tvdbID,
		Title:     "Game of Thrones",
	}, mediarequests.Integration{
		Kind:             "sonarr",
		BaseURL:          server.URL,
		APIKeyRef:        "sonarr-key",
		RootFolder:       "/series",
		QualityProfileID: &qualityProfileID,
	})
	if err == nil {
		t.Fatal("expected error for non-exact TVDB lookup match")
	}
}

func TestCheckSeriesStatusReadsQueueDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "sonarr-key" {
			t.Fatalf("X-Api-Key = %q, want sonarr-key", got)
		}
		if r.URL.Path != "/api/v3/queue/details" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("seriesId"); got != "24" {
			t.Fatalf("seriesId = %q, want 24", got)
		}
		w.Write([]byte(`[{"seriesId":24,"status":"downloading","trackedDownloadState":"importing"}]`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	status, err := client.CheckSeriesStatus(context.Background(), mediarequests.Request{
		MediaType:  mediarequests.MediaTypeSeries,
		TMDBID:     1399,
		ExternalID: "24",
	}, mediarequests.Integration{
		Kind:      "sonarr",
		BaseURL:   server.URL,
		APIKeyRef: "sonarr-key",
	})
	if err != nil {
		t.Fatalf("CheckSeriesStatus returned error: %v", err)
	}
	if status.Status != mediarequests.StatusDownloading || status.ExternalStatus != "downloading/importing" {
		t.Fatalf("status = %+v, want downloading", status)
	}
}

func TestListSeriesIntegrationOptionsLoadsChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "sonarr-key" {
			t.Fatalf("X-Api-Key = %q, want sonarr-key", got)
		}
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"path":"/series","freeSpace":123,"totalSpace":456,"accessible":true}]`))
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":3,"name":"HD-1080p"}]`))
		case "/api/v3/tag":
			w.Write([]byte(`[{"id":4,"label":"requests"}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	options, err := client.ListSeriesIntegrationOptions(context.Background(), mediarequests.Integration{
		Kind:      "sonarr",
		BaseURL:   server.URL,
		APIKeyRef: "sonarr-key",
	})
	if err != nil {
		t.Fatalf("ListSeriesIntegrationOptions returned error: %v", err)
	}
	if options.Kind != "sonarr" {
		t.Fatalf("kind = %q, want sonarr", options.Kind)
	}
	if len(options.RootFolders) != 1 || options.RootFolders[0].Path != "/series" {
		t.Fatalf("root folders = %+v, want /series", options.RootFolders)
	}
	if len(options.QualityProfiles) != 1 || options.QualityProfiles[0].ID != 3 {
		t.Fatalf("quality profiles = %+v, want id 3", options.QualityProfiles)
	}
	if len(options.Tags) != 1 || options.Tags[0].ID != 4 {
		t.Fatalf("tags = %+v, want id 4", options.Tags)
	}
}

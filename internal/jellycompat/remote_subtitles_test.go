package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeRemoteSubtitles struct {
	searchedFile      int
	searchedLanguages []string
	downloads         []subtitles.DownloadRequest
}

func (f *fakeRemoteSubtitles) SearchSubtitles(_ context.Context, _ catalog.AccessFilter, fileID int, languages []string) (*subtitles.SearchResponse, error) {
	f.searchedFile, f.searchedLanguages = fileID, languages
	return &subtitles.SearchResponse{Results: []subtitles.SubtitleResult{{
		ID: "os-123", Provider: "opensubtitles", Language: "en", ReleaseName: "Movie.2020.1080p.WEB", Format: "srt",
		Score: 8.5, Downloads: 42, HearingImpaired: true, UploadDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}, nil
}

func (f *fakeRemoteSubtitles) DownloadStoredSubtitle(_ context.Context, _ catalog.AccessFilter, req subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error) {
	f.downloads = append(f.downloads, req)
	return &subtitles.DownloadedSubtitle{ID: 1, MediaFileID: req.MediaFileID}, nil
}

type mapSettings map[string]string

func (m mapSettings) Get(_ context.Context, key string) (string, error) { return m[key], nil }

func testRemoteSubtitleToken(t *testing.T, token remoteSubtitleToken) string {
	t.Helper()
	id, ok := encodeRemoteSubtitleToken(token)
	if !ok {
		t.Fatal("encode remote subtitle token")
	}
	return id
}

// Jellyfin clients search the configured subtitle providers for an item and
// download a result through GET and POST /Items/{id}/RemoteSearch/Subtitles
// (#3205). The search runs for the item's media file, or the media source the
// client names; a download stores the result for the file it was found for,
// and only a file of that item.
func TestRemoteSubtitleSearchAndDownload(t *testing.T) {
	codec := NewResourceIDCodec()
	itemID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	content := &countingContentService{episodeDetail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{{FileID: 11}, {FileID: 12}}}}
	newRouter := func(service RemoteSubtitleService, settings SettingsReader) chi.Router {
		h := NewRemoteSubtitlesHandler(content, codec, nil, func() RemoteSubtitleService { return service }, settings)
		r := chi.NewRouter()
		r.Get("/Items/{id}/RemoteSearch/Subtitles/{language}", h.HandleSearch)
		r.Post("/Items/{id}/RemoteSearch/Subtitles/{subtitleId}", h.HandleDownload)
		return r
	}
	serve := func(r chi.Router, method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	search := func(t *testing.T, r chi.Router, query string) []remoteSubtitleInfo {
		t.Helper()
		rec := serve(r, http.MethodGet, "/Items/"+itemID+"/RemoteSearch/Subtitles/eng"+query)
		if rec.Code != http.StatusOK {
			t.Fatalf("search status = %d: %s", rec.Code, rec.Body.String())
		}
		var out []remoteSubtitleInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("search", func(t *testing.T) {
		service := &fakeRemoteSubtitles{}
		results := search(t, newRouter(service, nil), "")
		if service.searchedFile != 11 || !slices.Equal(service.searchedLanguages, []string{"en"}) {
			t.Fatalf("searched file %d languages %v, want file 11 and [en]", service.searchedFile, service.searchedLanguages)
		}
		if len(results) != 1 {
			t.Fatalf("results = %+v", results)
		}
		got := results[0]
		if got.ThreeLetterISOLanguageName != "eng" || got.ProviderName != "opensubtitles" || got.Name != "Movie.2020.1080p.WEB" || got.Format != "srt" || !got.HearingImpaired ||
			got.DownloadCount == nil || *got.DownloadCount != 42 || got.DateCreated == nil {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("search names a media source", func(t *testing.T) {
		service := &fakeRemoteSubtitles{}
		search(t, newRouter(service, nil), "?MediaSourceId="+codec.EncodeIntID(EncodedIDMediaSource, 12))
		if service.searchedFile != 12 {
			t.Fatalf("searched file %d, want the named source 12", service.searchedFile)
		}
	})

	t.Run("download stores the result for its file", func(t *testing.T) {
		service := &fakeRemoteSubtitles{}
		r := newRouter(service, nil)
		id := search(t, r, "?MediaSourceId="+codec.EncodeIntID(EncodedIDMediaSource, 12))[0].ID
		if rec := serve(r, http.MethodPost, "/Items/"+itemID+"/RemoteSearch/Subtitles/"+id); rec.Code != http.StatusNoContent {
			t.Fatalf("download status = %d: %s", rec.Code, rec.Body.String())
		}
		want := subtitles.DownloadRequest{MediaFileID: 12, ProviderName: "opensubtitles", SubtitleID: "os-123", Language: "en",
			ReleaseName: "Movie.2020.1080p.WEB", Score: 8.5, HearingImpaired: true}
		if len(service.downloads) != 1 || service.downloads[0] != want {
			t.Fatalf("downloads = %+v, want %+v", service.downloads, want)
		}
	})

	for _, tc := range []struct {
		name     string
		id       string
		settings SettingsReader
		want     int
	}{
		{name: "a file outside the item", id: testRemoteSubtitleToken(t, remoteSubtitleToken{FileID: 99, Provider: "opensubtitles", SubtitleID: "os-123", Language: "en"}), want: http.StatusNotFound},
		{name: "an id that isn't a search result", id: "not-a-result", want: http.StatusNotFound},
		{name: "a demo server", id: testRemoteSubtitleToken(t, remoteSubtitleToken{FileID: 11, Provider: "opensubtitles", SubtitleID: "os-123", Language: "en"}), settings: mapSettings{"demo.enabled": "true"}, want: http.StatusForbidden},
	} {
		t.Run("download refuses "+tc.name, func(t *testing.T) {
			service := &fakeRemoteSubtitles{}
			if rec := serve(newRouter(service, tc.settings), http.MethodPost, "/Items/"+itemID+"/RemoteSearch/Subtitles/"+tc.id); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if len(service.downloads) != 0 {
				t.Fatalf("downloads = %+v, want none", service.downloads)
			}
		})
	}

	t.Run("without the subtitle service", func(t *testing.T) {
		if rec := serve(newRouter(nil, nil), http.MethodGet, "/Items/"+itemID+"/RemoteSearch/Subtitles/eng"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
}

package handlers

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type imageItemLookupFake map[string]*models.MediaItem

func (f imageItemLookupFake) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if item, ok := f[contentID]; ok {
		return item, nil
	}
	return nil, catalog.ErrItemNotFound
}

type imageSeasonLookupFake map[string]*models.Season

func (f imageSeasonLookupFake) GetByID(_ context.Context, contentID string) (*models.Season, error) {
	if season, ok := f[contentID]; ok {
		return season, nil
	}
	return nil, catalog.ErrSeasonNotFound
}

type imageEpisodeLookupFake struct{}

func (imageEpisodeLookupFake) GetByID(context.Context, string) (*models.Episode, error) {
	return nil, catalog.ErrEpisodeNotFound
}

type imageFolderLookupFake struct {
	folderID int
}

type imageProviderIDLookupFake struct {
	rows       map[string][]*models.MediaItemProviderID
	err        error
	contentIDs []string
}

func (f *imageProviderIDLookupFake) GetByContentID(_ context.Context, contentID string) ([]*models.MediaItemProviderID, error) {
	f.contentIDs = append(f.contentIDs, contentID)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows[contentID], nil
}

func (f imageFolderLookupFake) GetFolderIDForItem(context.Context, string) (int, error) {
	return f.folderID, nil
}

func (f imageFolderLookupFake) GetFolderIDsForItem(context.Context, string) ([]int, error) {
	return []int{f.folderID}, nil
}

type seasonImageFetchCall struct {
	providerIDs map[string]string
	language    string
	folderID    int
	season      int
}

type imageServiceFake struct {
	seasonImages    []metadata.RemoteImage
	itemCalls       int
	itemProviderIDs []map[string]string
	seasonCalls     []seasonImageFetchCall
}

func (f *imageServiceFake) FetchItemImages(_ context.Context, providerIDs map[string]string, _ string, _ string, _ int) ([]metadata.RemoteImage, map[string]string, error) {
	f.itemCalls++
	f.itemProviderIDs = append(f.itemProviderIDs, providerIDs)
	return []metadata.RemoteImage{{ProviderID: "tmdb", URL: "tmdb://show-poster.jpg", Type: metadata.ImagePoster}}, nil, nil
}

func (f *imageServiceFake) FetchSeasonImages(_ context.Context, providerIDs map[string]string, language string, folderID int, seasonNumber int) ([]metadata.RemoteImage, map[string]string, error) {
	f.seasonCalls = append(f.seasonCalls, seasonImageFetchCall{
		providerIDs: providerIDs,
		language:    language,
		folderID:    folderID,
		season:      seasonNumber,
	})
	return f.seasonImages, nil, nil
}

func (f *imageServiceFake) ApplyItemImage(context.Context, metadata.ApplyItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	return nil, nil
}

func TestHandleGetItemImagesUsesSeasonSpecificFetch(t *testing.T) {
	tests := []struct {
		name          string
		contentID     string
		seasonNumber  int
		language      string
		currentPoster string
	}{
		{name: "numbered season", contentID: "season-2", seasonNumber: 2, language: "fr", currentPoster: "cache://season-2.jpg"},
		{name: "specials", contentID: "season-0", seasonNumber: 0, language: "", currentPoster: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const seriesID = "series-1"
			imageSvc := &imageServiceFake{seasonImages: []metadata.RemoteImage{{
				ProviderID: "tmdb",
				URL:        "tmdb://requested-season.jpg",
				Type:       metadata.ImagePoster,
			}}}
			providerIDs := &imageProviderIDLookupFake{rows: map[string][]*models.MediaItemProviderID{
				seriesID: {{ContentID: seriesID, Provider: "custom", ProviderID: "show-42"}},
			}}
			handler := NewAdminImageHandler(
				imageItemLookupFake{seriesID: {
					ContentID:               seriesID,
					Type:                    "series",
					TmdbID:                  "123",
					TvdbID:                  "456",
					DefaultMetadataLanguage: tt.language,
					PosterPath:              "cache://show-poster.jpg",
				}},
				imageSeasonLookupFake{tt.contentID: {
					ContentID:    tt.contentID,
					SeriesID:     seriesID,
					SeasonNumber: tt.seasonNumber,
					PosterPath:   tt.currentPoster,
				}},
				imageEpisodeLookupFake{},
				imageFolderLookupFake{folderID: 9},
				imageSvc,
				nil,
				nil,
				providerIDs,
			)
			router := chi.NewRouter()
			router.Get("/admin/items/{id}/images", handler.HandleGetItemImages)

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/admin/items/"+tt.contentID+"/images", nil)
			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if imageSvc.itemCalls != 0 {
				t.Fatalf("generic item image calls = %d, want 0", imageSvc.itemCalls)
			}
			if len(imageSvc.seasonCalls) != 1 {
				t.Fatalf("season image calls = %d, want 1", len(imageSvc.seasonCalls))
			}
			call := imageSvc.seasonCalls[0]
			wantLanguage := tt.language
			if wantLanguage == "" {
				wantLanguage = "en"
			}
			if call.season != tt.seasonNumber || call.folderID != 9 || call.language != wantLanguage || call.providerIDs["tmdb"] != "123" || call.providerIDs["tvdb"] != "456" || call.providerIDs["custom"] != "show-42" {
				t.Fatalf("season fetch call = %#v", call)
			}
			if len(providerIDs.contentIDs) != 1 || providerIDs.contentIDs[0] != seriesID {
				t.Fatalf("provider ID lookup content IDs = %#v", providerIDs.contentIDs)
			}

			var response getItemImagesResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(response.Images) != 1 || response.Images[0].OriginalURL != "tmdb://requested-season.jpg" {
				t.Fatalf("images = %#v", response.Images)
			}
			if response.Current.PosterURL != tt.currentPoster {
				t.Fatalf("current poster = %q, want season poster %q", response.Current.PosterURL, tt.currentPoster)
			}
		})
	}
}

func TestHandleGetItemImagesMergesSavedProviderIDsWithoutOverridingCanonicalIDs(t *testing.T) {
	const seriesID = "series-1"
	imageSvc := &imageServiceFake{}
	providerIDs := &imageProviderIDLookupFake{rows: map[string][]*models.MediaItemProviderID{
		seriesID: {
			{ContentID: seriesID, Provider: " custom ", ProviderID: " saved-id "},
			{ContentID: seriesID, Provider: "TMDB", ProviderID: "wrong-saved-id"},
			{ContentID: seriesID, Provider: " imdb ", ProviderID: " tt123 "},
			{ContentID: seriesID, Provider: "mediux", ProviderID: "show-art"},
			{ContentID: seriesID, Provider: "durable", ProviderID: "fallback"},
			{ContentID: seriesID, Provider: "metadb", ProviderID: "transient-hint"},
			{ContentID: seriesID, Provider: "_filepath", ProviderID: "/media/show.mkv"},
			{ContentID: seriesID, Provider: "oshash", ProviderID: "hash"},
			{ContentID: seriesID, Provider: "manga_series", ProviderID: "scan-anchor"},
			nil,
			{ContentID: seriesID, Provider: " ", ProviderID: "ignored"},
			{ContentID: seriesID, Provider: "ignored", ProviderID: " "},
		},
	}}
	handler := NewAdminImageHandler(
		imageItemLookupFake{seriesID: {
			ContentID: seriesID,
			Type:      "series",
			TmdbID:    "123",
			TvdbID:    "456",
		}},
		imageSeasonLookupFake{},
		imageEpisodeLookupFake{},
		imageFolderLookupFake{folderID: 9},
		imageSvc,
		nil,
		nil,
		providerIDs,
	)
	router := chi.NewRouter()
	router.Get("/admin/items/{id}/images", handler.HandleGetItemImages)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/items/"+seriesID+"/images", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if imageSvc.itemCalls != 1 || len(imageSvc.itemProviderIDs) != 1 {
		t.Fatalf("item calls = %d, provider IDs = %#v", imageSvc.itemCalls, imageSvc.itemProviderIDs)
	}
	got := imageSvc.itemProviderIDs[0]
	want := map[string]string{"tmdb": "123", "tvdb": "456", "imdb": "tt123", "custom": "saved-id", "mediux": "show-art", "durable": "fallback"}
	if !maps.Equal(got, want) {
		t.Errorf("provider IDs = %#v, want %#v", got, want)
	}
}

func TestHandleGetItemImagesSavedProviderIDLookupFailureStopsBeforeFetch(t *testing.T) {
	const seriesID = "series-1"
	imageSvc := &imageServiceFake{}
	handler := NewAdminImageHandler(
		imageItemLookupFake{seriesID: {ContentID: seriesID, Type: "series", TmdbID: "123"}},
		imageSeasonLookupFake{}, imageEpisodeLookupFake{}, imageFolderLookupFake{folderID: 9},
		imageSvc, nil, nil,
		&imageProviderIDLookupFake{err: context.DeadlineExceeded},
	)
	router := chi.NewRouter()
	router.Get("/admin/items/{id}/images", handler.HandleGetItemImages)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/items/"+seriesID+"/images", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if imageSvc.itemCalls != 0 || len(imageSvc.seasonCalls) != 0 {
		t.Fatalf("image fetch ran after durable ID lookup failure: item=%d seasons=%#v", imageSvc.itemCalls, imageSvc.seasonCalls)
	}
	if !json.Valid(recorder.Body.Bytes()) || !containsJSONError(recorder.Body.Bytes(), "Failed to load provider IDs") {
		t.Fatalf("unexpected error response: %s", recorder.Body.String())
	}
}

func containsJSONError(body []byte, want string) bool {
	var response struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(body, &response) == nil && response.Message == want
}

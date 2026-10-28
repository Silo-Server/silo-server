package handlers

import (
	"context"
	"errors"
	"testing"

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
	seasonImages []metadata.RemoteImage
	itemCalls    int
	seasonCalls  []seasonImageFetchCall
}

func (f *imageServiceFake) FetchItemImages(context.Context, map[string]string, string, string, int) ([]metadata.RemoteImage, map[string]string, error) {
	f.itemCalls++
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

func (f *imageServiceFake) FetchItemImagesWithLocal(ctx context.Context, providerIDs map[string]string, contentType, language string, folderID int, _ string) ([]metadata.RemoteImage, map[string]string, error) {
	return f.FetchItemImages(ctx, providerIDs, contentType, language, folderID)
}

func (f *imageServiceFake) LocalImagePreview(context.Context, string, string) (string, error) {
	return "", errors.New("no local images")
}

func (f *imageServiceFake) ApplyLocalItemImage(context.Context, metadata.ApplyLocalItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	return nil, errors.New("no local images")
}

func (f *imageServiceFake) ApplyUploadedItemImage(context.Context, metadata.ApplyUploadedItemImageRequest) (*metadata.ApplyUploadedItemImageResult, error) {
	return nil, errors.New("no uploads")
}

func TestAdminItemImagesUsesSeasonSpecificFetch(t *testing.T) {
	for _, tt := range []struct {
		name          string
		contentID     string
		seasonNumber  int
		language      string
		currentPoster string
	}{
		{name: "numbered season", contentID: "season-2", seasonNumber: 2, language: "fr", currentPoster: "cache://season-2.jpg"},
		{name: "specials", contentID: "season-0", seasonNumber: 0, language: "", currentPoster: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const seriesID = "series-1"
			imageSvc := &imageServiceFake{seasonImages: []metadata.RemoteImage{{
				ProviderID: "tmdb", URL: "tmdb://requested-season.jpg", Type: metadata.ImagePoster,
			}}}
			handler := NewAdminImageHandler(
				imageItemLookupFake{seriesID: {
					ContentID: seriesID, Type: "series", TmdbID: "123", TvdbID: "456",
					DefaultMetadataLanguage: tt.language, PosterPath: "cache://show-poster.jpg",
				}},
				imageSeasonLookupFake{tt.contentID: {
					ContentID: tt.contentID, SeriesID: seriesID, SeasonNumber: tt.seasonNumber, PosterPath: tt.currentPoster,
				}},
				imageEpisodeLookupFake{},
				imageFolderLookupFake{folderID: 9},
				imageSvc,
				nil,
				nil,
			)

			response, err := handler.GetAdminItemImages(t.Context(), tt.contentID)
			if err != nil {
				t.Fatal(err)
			}
			if imageSvc.itemCalls != 0 || len(imageSvc.seasonCalls) != 1 {
				t.Fatalf("item calls %d, season calls %d, want only one season fetch", imageSvc.itemCalls, len(imageSvc.seasonCalls))
			}
			call := imageSvc.seasonCalls[0]
			wantLanguage := tt.language
			if wantLanguage == "" {
				wantLanguage = "en"
			}
			if call.season != tt.seasonNumber || call.folderID != 9 || call.language != wantLanguage || call.providerIDs["tmdb"] != "123" || call.providerIDs["tvdb"] != "456" {
				t.Fatalf("season fetch call = %#v", call)
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

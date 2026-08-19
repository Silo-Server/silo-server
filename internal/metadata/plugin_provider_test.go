package metadata

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const (
	pluginInstallationIDSetting = "plugin_installation_id"
	capabilityIDSetting         = "capability_id"
)

type fakePluginMetadataClient struct {
	searchResponse *pluginv1.SearchMetadataResponse
	response       *pluginv1.GetMetadataResponse
	imagesResponse *pluginv1.GetImagesResponse
	seasonsResp    *pluginv1.GetSeasonsResponse
	episodesResp   *pluginv1.GetEpisodesResponse

	searchReq      *pluginv1.SearchMetadataRequest
	getMetadataReq *pluginv1.GetMetadataRequest
	getImagesReq   *pluginv1.GetImagesRequest
	getSeasonsReq  *pluginv1.GetSeasonsRequest
	getEpisodesReq *pluginv1.GetEpisodesRequest
}

func (f *fakePluginMetadataClient) Search(_ context.Context, req *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error) {
	f.searchReq = req
	return f.searchResponse, nil
}

func (f *fakePluginMetadataClient) GetMetadata(_ context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error) {
	f.getMetadataReq = req
	return f.response, nil
}

func (f *fakePluginMetadataClient) GetSeasons(_ context.Context, req *pluginv1.GetSeasonsRequest) (*pluginv1.GetSeasonsResponse, error) {
	f.getSeasonsReq = req
	return f.seasonsResp, nil
}

func (f *fakePluginMetadataClient) GetEpisodes(_ context.Context, req *pluginv1.GetEpisodesRequest) (*pluginv1.GetEpisodesResponse, error) {
	f.getEpisodesReq = req
	return f.episodesResp, nil
}

func (f *fakePluginMetadataClient) GetImages(_ context.Context, req *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error) {
	f.getImagesReq = req
	return f.imagesResponse, nil
}

func (f *fakePluginMetadataClient) GetPersonDetail(_ context.Context, _ *pluginv1.GetPersonDetailRequest) (*pluginv1.GetPersonDetailResponse, error) {
	return nil, nil
}

func (f *fakePluginMetadataClient) ResolveImageURL(context.Context, *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	return nil, nil
}

func (f *fakePluginMetadataClient) ResolveImageURLs(context.Context, *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	return nil, nil
}

func setPluginMetadataItemReleaseDate(t *testing.T, item *pluginv1.MetadataItem, value string) {
	t.Helper()

	field := item.ProtoReflect().Descriptor().Fields().ByName("release_date")
	if field == nil {
		t.Fatal("MetadataItem descriptor is missing release_date")
	}

	item.ProtoReflect().Set(field, protoreflect.ValueOfString(value))
}

//nolint:goconst // Keep the production-shaped protobuf fixture readable in place.
func TestPluginProviderSearch_MapsLocalizedTitlesAndCrossReferences(t *testing.T) {
	const localizedJapanesePrimaryTitle = "十兵衛ちゃん -ラブリー眼帯の秘密-"
	client := &fakePluginMetadataClient{
		searchResponse: &pluginv1.SearchMetadataResponse{Results: []*pluginv1.ProviderSearchResult{
			{
				ProviderId:       localizedTVDBID,
				ItemType:         anchoredItemTypeSeries,
				Title:            localizedJapanesePrimaryTitle,
				OriginalTitle:    localizedOriginalTitle,
				Year:             1999,
				TitleLanguage:    "ja",
				TitleIsFallback:  true,
				OriginalLanguage: "ja",
				TitleAliases: []*pluginv1.TitleAlias{
					{Title: localizedEnglishTitle, Language: "en", Kind: testAlternateAliasKind},
				},
				ProviderIds: mustStructFromStringMap(t, map[string]string{
					testTMDBProvider: localizedTMDBID,
					testIMDBProvider: "tt0213338",
				}),
			},
		}},
	}

	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         testTVDBProvider,
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	results, err := provider.Search(context.Background(), SearchQuery{
		Title:       localizedEnglishTitle,
		ContentType: anchoredItemTypeSeries,
		Language:    "en",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if client.searchReq == nil || client.searchReq.GetQuery() != localizedEnglishTitle || client.searchReq.GetItemType() != anchoredItemTypeSeries || client.searchReq.GetLanguage() != "en" {
		t.Fatalf("captured search request = %+v", client.searchReq)
	}
	if len(results) != 1 {
		t.Fatalf("Search() returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.Name != localizedJapanesePrimaryTitle || got.OriginalTitle != localizedOriginalTitle || got.Year != 1999 {
		t.Fatalf("localized identity fields = %+v", got)
	}
	if got.TitleLanguage != "ja" || !got.TitleIsFallback || got.OriginalLanguage != "ja" {
		t.Fatalf("language/fallback fields = language:%q fallback:%v original:%q", got.TitleLanguage, got.TitleIsFallback, got.OriginalLanguage)
	}
	wantAliases := []TitleAlias{{Title: localizedEnglishTitle, Language: "en", Kind: testAlternateAliasKind, Provider: testTVDBProvider}}
	if !reflect.DeepEqual(got.TitleAliases, wantAliases) {
		t.Fatalf("TitleAliases = %#v, want %#v", got.TitleAliases, wantAliases)
	}
	if !reflect.DeepEqual(got.ProviderIDs, map[string]string{
		testTVDBProvider: localizedTVDBID,
		testTMDBProvider: localizedTMDBID,
		testIMDBProvider: "tt0213338",
	}) {
		t.Fatalf("ProviderIDs = %#v", got.ProviderIDs)
	}
}

func TestPluginProviderGetMetadata_MapsReleaseDate(t *testing.T) {
	item := &pluginv1.MetadataItem{
		ProviderId: "provider-1",
		ItemType:   "movie",
		Title:      "Example Movie",
		ProviderIds: mustStructFromStringMap(t, map[string]string{
			"tmdb": "123",
		}),
	}
	setPluginMetadataItemReleaseDate(t, item, "2024-01-02")

	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		"plugin_installation_id": "1",
		"capability_id":          "tmdb",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return &fakePluginMetadataClient{
			response: &pluginv1.GetMetadataResponse{Item: item},
		}, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	result, err := provider.GetMetadata(context.Background(), MetadataRequest{
		ProviderIDs: map[string]string{"tmdb": "provider-1"},
		ContentType: "movie",
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected metadata result")
	}
	if result.ReleaseDate != "2024-01-02" {
		t.Fatalf("ReleaseDate = %q, want 2024-01-02", result.ReleaseDate)
	}
}

func TestPluginProviderGetMetadata_MapsAirTime(t *testing.T) {
	item := &pluginv1.MetadataItem{
		ProviderId: "provider-1",
		ItemType:   "series",
		Title:      "Example Series",
		AirTime:    "20:00",
		ProviderIds: mustStructFromStringMap(t, map[string]string{
			"tvdb": "123",
		}),
	}

	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		"plugin_installation_id": "1",
		"capability_id":          "tvdb",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return &fakePluginMetadataClient{
			response: &pluginv1.GetMetadataResponse{Item: item},
		}, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	result, err := provider.GetMetadata(context.Background(), MetadataRequest{
		ProviderIDs: map[string]string{"tvdb": "provider-1"},
		ContentType: "series",
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected metadata result")
	}
	if result.AirTime != "20:00" {
		t.Fatalf("AirTime = %q, want 20:00", result.AirTime)
	}
}

func TestPluginProviderGetMetadata_MapsKeywordsFromMetadata(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"keywords": []any{"time loop", "heist", "time loop", "", 42},
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	item := &pluginv1.MetadataItem{
		ProviderId: "provider-1",
		ItemType:   "movie",
		Title:      "Example Movie",
		Metadata:   metadata,
		ProviderIds: mustStructFromStringMap(t, map[string]string{
			"tmdb": "123",
		}),
	}

	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		"plugin_installation_id": "1",
		"capability_id":          "tmdb",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return &fakePluginMetadataClient{
			response: &pluginv1.GetMetadataResponse{Item: item},
		}, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	result, err := provider.GetMetadata(context.Background(), MetadataRequest{
		ProviderIDs: map[string]string{"tmdb": "provider-1"},
		ContentType: "movie",
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected metadata result")
	}
	want := []string{"time loop", "heist"}
	if !reflect.DeepEqual(result.Keywords, want) {
		t.Fatalf("Keywords = %#v, want %#v", result.Keywords, want)
	}
}

func TestPluginProviderGetMetadata_ForwardsRequestContext(t *testing.T) {
	client := &fakePluginMetadataClient{
		response: &pluginv1.GetMetadataResponse{
			Item: &pluginv1.MetadataItem{ProviderId: "provider-1"},
		},
	}

	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		"plugin_installation_id": "1",
		"capability_id":          "tmdb",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	_, err = provider.GetMetadata(context.Background(), MetadataRequest{
		ProviderIDs: map[string]string{
			"tmdb": "provider-1",
			"imdb": "tt1234567",
		},
		ContentType: "movie",
		Language:    "fr",
		FilePath:    "/media/movies/example.mkv",
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if client.getMetadataReq == nil {
		t.Fatal("expected GetMetadata request to be captured")
	}
	if client.getMetadataReq.GetProviderId() != "provider-1" {
		t.Fatalf("ProviderId = %q, want provider-1", client.getMetadataReq.GetProviderId())
	}
	if client.getMetadataReq.GetItemType() != "movie" {
		t.Fatalf("ItemType = %q, want movie", client.getMetadataReq.GetItemType())
	}
	if client.getMetadataReq.GetLanguage() != "fr" {
		t.Fatalf("Language = %q, want fr", client.getMetadataReq.GetLanguage())
	}
	if client.getMetadataReq.GetFilePath() != "/media/movies/example.mkv" {
		t.Fatalf("FilePath = %q, want /media/movies/example.mkv", client.getMetadataReq.GetFilePath())
	}
	assertStructStringMap(t, client.getMetadataReq.GetProviderIds(), map[string]string{
		"tmdb": "provider-1",
		"imdb": "tt1234567",
	})
}

func TestPluginProviderAssetRequests_ForwardProviderContext(t *testing.T) {
	client := &fakePluginMetadataClient{
		imagesResponse: &pluginv1.GetImagesResponse{},
		seasonsResp:    &pluginv1.GetSeasonsResponse{},
		episodesResp:   &pluginv1.GetEpisodesResponse{},
	}

	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		"plugin_installation_id": "1",
		"capability_id":          "tmdb",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	_, err = provider.GetImages(context.Background(), ImageRequest{
		ProviderIDs: map[string]string{
			"tmdb": "provider-1",
			"imdb": "tt1234567",
		},
		ContentType: "movie",
		Language:    "es",
	})
	if err != nil {
		t.Fatalf("GetImages() error = %v", err)
	}
	if client.getImagesReq == nil {
		t.Fatal("expected GetImages request to be captured")
	}
	if client.getImagesReq.GetLanguage() != "es" {
		t.Fatalf("Language = %q, want es", client.getImagesReq.GetLanguage())
	}
	assertStructStringMap(t, client.getImagesReq.GetProviderIds(), map[string]string{
		"tmdb": "provider-1",
		"imdb": "tt1234567",
	})

	_, err = provider.GetSeasons(context.Background(), SeasonsRequest{
		ProviderIDs: map[string]string{
			"tmdb": "series-1",
			"tvdb": "81189",
		},
		ContentType: "series",
	})
	if err != nil {
		t.Fatalf("GetSeasons() error = %v", err)
	}
	if client.getSeasonsReq == nil {
		t.Fatal("expected GetSeasons request to be captured")
	}
	if client.getSeasonsReq.GetSeriesProviderId() != "series-1" {
		t.Fatalf("SeriesProviderId = %q, want series-1", client.getSeasonsReq.GetSeriesProviderId())
	}
	assertStructStringMap(t, client.getSeasonsReq.GetProviderIds(), map[string]string{
		"tmdb": "series-1",
		"tvdb": "81189",
	})

	_, err = provider.GetEpisodes(context.Background(), EpisodesRequest{
		ProviderIDs: map[string]string{
			"tmdb": "series-1",
			"tvdb": "81189",
		},
		SeasonNumber: 2,
	})
	if err != nil {
		t.Fatalf("GetEpisodes() error = %v", err)
	}
	if client.getEpisodesReq == nil {
		t.Fatal("expected GetEpisodes request to be captured")
	}
	if client.getEpisodesReq.GetSeasonNumber() != 2 {
		t.Fatalf("SeasonNumber = %d, want 2", client.getEpisodesReq.GetSeasonNumber())
	}
	assertStructStringMap(t, client.getEpisodesReq.GetProviderIds(), map[string]string{
		"tmdb": "series-1",
		"tvdb": "81189",
	})
}

func TestPluginProviderGetImages_MapsIncludesTextMetadata(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"rating":        8.5,
		"includes_text": false,
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	client := &fakePluginMetadataClient{
		imagesResponse: &pluginv1.GetImagesResponse{Images: []*pluginv1.ImageRecord{
			{
				Url:      "https://artworks.thetvdb.com/example.jpg",
				Kind:     "poster",
				Language: "en",
				Metadata: metadata,
			},
		}},
	}
	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         "tvdb",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	images, err := provider.GetImages(context.Background(), ImageRequest{
		ProviderIDs: map[string]string{"tvdb": "series-1"},
		ContentType: "series",
		Language:    "en",
	})
	if err != nil {
		t.Fatalf("GetImages() error = %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("GetImages() returned %d images, want 1", len(images))
	}
	if images[0].Rating != 8.5 {
		t.Fatalf("Rating = %v, want 8.5", images[0].Rating)
	}
	if images[0].IncludesText == nil || *images[0].IncludesText {
		t.Fatalf("IncludesText = %v, want explicit false", images[0].IncludesText)
	}
}

func mustStructFromStringMap(t *testing.T, values map[string]string) *structpb.Struct {
	t.Helper()

	asAny := make(map[string]any, len(values))
	for key, value := range values {
		asAny[key] = value
	}

	result, err := structpb.NewStruct(asAny)
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	return result
}

func assertStructStringMap(t *testing.T, value *structpb.Struct, want map[string]string) {
	t.Helper()

	got := make(map[string]string, len(want))
	if value != nil {
		for key, raw := range value.AsMap() {
			text, ok := raw.(string)
			if ok {
				got[key] = text
			}
		}
	}

	if len(got) != len(want) {
		t.Fatalf("struct map length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("struct map[%q] = %q, want %q", key, got[key], wantValue)
		}
	}
}

package metadata

import (
	"context"

	"testing"

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
	getMetadataErr error
	imagesResponse *pluginv1.GetImagesResponse
	seasonsResp    *pluginv1.GetSeasonsResponse
	episodesResp   *pluginv1.GetEpisodesResponse
	personResp     *pluginv1.GetPersonDetailResponse

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
	return f.response, f.getMetadataErr
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
	return f.personResp, nil
}

func (f *fakePluginMetadataClient) ResolveImageURL(context.Context, *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	return nil, nil
}

func (f *fakePluginMetadataClient) ResolveImageURLs(context.Context, *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	return nil, nil
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

	specials := 0
	_, err = provider.GetImages(context.Background(), ImageRequest{
		ProviderIDs: map[string]string{
			"tmdb": "provider-1",
			"imdb": "tt1234567",
		},
		ContentType:  "series",
		Language:     "es",
		SeasonNumber: &specials,
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
	if client.getImagesReq.SeasonNumber == nil || client.getImagesReq.GetSeasonNumber() != 0 {
		t.Fatalf("SeasonNumber = %v, want present Specials value 0", client.getImagesReq.SeasonNumber)
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

// TestPluginProvider_DropsLocalArtworkSources checks that a metadata plugin
// cannot mint a local artwork source: file:// and nfo+http(s):// values are
// dropped from every artwork field, and ordinary remote URLs are kept.
func TestPluginProvider_DropsLocalArtworkSources(t *testing.T) {
	const remote = "https://img.example.org/poster.jpg"
	const sidecar = "file:///etc/passwd?v=1-1"
	const nfoURL = "nfo+https://img.example.org/poster.jpg"

	client := &fakePluginMetadataClient{
		response: &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{
			ProviderId:   "provider-1",
			ItemType:     "movie",
			Title:        "Example",
			PosterPath:   sidecar,
			BackdropPath: nfoURL,
			LogoPath:     remote,
			People: []*pluginv1.PersonRecord{
				{Name: "Local", Kind: "actor", PhotoPath: "FILE:///tmp/photo.jpg"},
				{Name: "Remote", Kind: "actor", PhotoPath: remote},
			},
		}},
		imagesResponse: &pluginv1.GetImagesResponse{Images: []*pluginv1.ImageRecord{
			{Url: sidecar, Kind: "poster"},
			{Url: remote, Kind: "poster"},
			{Url: nfoURL, Kind: "backdrop"},
		}},
		seasonsResp: &pluginv1.GetSeasonsResponse{Seasons: []*pluginv1.SeasonRecord{
			{SeasonNumber: 1, PosterPath: sidecar},
			{SeasonNumber: 2, PosterPath: remote},
		}},
		episodesResp: &pluginv1.GetEpisodesResponse{Episodes: []*pluginv1.EpisodeRecord{
			{SeasonNumber: 1, EpisodeNumber: 1, StillPath: nfoURL},
			{SeasonNumber: 1, EpisodeNumber: 2, StillPath: remote},
		}},
		searchResponse: &pluginv1.SearchMetadataResponse{Results: []*pluginv1.ProviderSearchResult{
			{Title: "Local", ImageUrl: sidecar},
			{Title: "Remote", ImageUrl: remote},
		}},
		personResp: &pluginv1.GetPersonDetailResponse{Person: &pluginv1.PersonDetailRecord{Name: "Local", PhotoPath: sidecar}},
	}
	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         "thirdparty",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}
	ctx := context.Background()
	ids := map[string]string{"thirdparty": "provider-1"}

	result, err := provider.GetMetadata(ctx, MetadataRequest{ProviderIDs: ids, ContentType: "movie"})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.PosterPath != "" || result.BackdropPath != "" || result.LogoPath != remote {
		t.Fatalf("artwork = poster %q, backdrop %q, logo %q; want only the remote logo", result.PosterPath, result.BackdropPath, result.LogoPath)
	}
	if len(result.People) != 2 || result.People[0].PhotoPath != "" || result.People[1].PhotoPath != remote {
		t.Fatalf("people photos = %+v, want local dropped and remote kept", result.People)
	}

	images, err := provider.GetImages(ctx, ImageRequest{ProviderIDs: ids, ContentType: "movie"})
	if err != nil {
		t.Fatalf("GetImages() error = %v", err)
	}
	if len(images) != 1 || images[0].URL != remote {
		t.Fatalf("GetImages() = %+v, want only %q", images, remote)
	}

	seasons, err := provider.GetSeasons(ctx, SeasonsRequest{ProviderIDs: ids})
	if err != nil {
		t.Fatalf("GetSeasons() error = %v", err)
	}
	if len(seasons) != 2 || seasons[0].PosterPath != "" || seasons[1].PosterPath != remote {
		t.Fatalf("season posters = %+v, want local dropped and remote kept", seasons)
	}

	episodes, err := provider.GetEpisodes(ctx, EpisodesRequest{ProviderIDs: ids, SeasonNumber: 1})
	if err != nil {
		t.Fatalf("GetEpisodes() error = %v", err)
	}
	if len(episodes) != 2 || episodes[0].StillPath != "" || episodes[1].StillPath != remote {
		t.Fatalf("episode stills = %+v, want local dropped and remote kept", episodes)
	}

	results, err := provider.Search(ctx, SearchQuery{Title: "Example", ContentType: "movie"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 || results[0].ImageURL != "" || results[1].ImageURL != remote {
		t.Fatalf("search images = %+v, want local dropped and remote kept", results)
	}

	person, err := provider.GetPersonDetail(ctx, PersonDetailRequest{ProviderIDs: ids})
	if err != nil {
		t.Fatalf("GetPersonDetail() error = %v", err)
	}
	if person == nil || person.PhotoPath != "" {
		t.Fatalf("person detail = %+v, want the local photo dropped", person)
	}
}

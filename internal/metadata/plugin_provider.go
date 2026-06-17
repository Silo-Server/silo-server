package metadata

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type pluginMetadataResolver interface {
	MetadataProviderClient(ctx context.Context, installationID int, capabilityID string) (pluginMetadataClient, error)
}

type pluginMetadataClient interface {
	Search(ctx context.Context, req *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error)
	GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error)
	GetPersonDetail(ctx context.Context, req *pluginv1.GetPersonDetailRequest) (*pluginv1.GetPersonDetailResponse, error)
	GetSeasons(ctx context.Context, req *pluginv1.GetSeasonsRequest) (*pluginv1.GetSeasonsResponse, error)
	GetEpisodes(ctx context.Context, req *pluginv1.GetEpisodesRequest) (*pluginv1.GetEpisodesResponse, error)
	GetImages(ctx context.Context, req *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error)
	ResolveImageURL(ctx context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error)
	ResolveImageURLs(ctx context.Context, req *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error)
}

type pluginMetadataClientFactory func(ctx context.Context, installationID int, capabilityID string) (pluginMetadataClient, error)

// PluginResolverAdapter wraps a concrete resolver (like *plugins.Service)
// whose MetadataProviderClient returns *pluginhost.MetadataProviderClient,
// adapting it to satisfy the pluginMetadataResolver interface.
type PluginResolverAdapter struct {
	inner interface {
		MetadataProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.MetadataProviderClient, error)
	}
}

// NewPluginResolverAdapter creates an adapter from a concrete plugin service.
func NewPluginResolverAdapter(svc interface {
	MetadataProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.MetadataProviderClient, error)
}) *PluginResolverAdapter {
	if svc == nil {
		return nil
	}
	return &PluginResolverAdapter{inner: svc}
}

func (a *PluginResolverAdapter) MetadataProviderClient(ctx context.Context, installationID int, capabilityID string) (pluginMetadataClient, error) {
	return a.inner.MetadataProviderClient(ctx, installationID, capabilityID)
}

type PluginProvider struct {
	installationID int
	capabilityID   string
	displayName    string
	clientFactory  pluginMetadataClientFactory
}

func NewPluginProvider(settings map[string]string, resolver pluginMetadataResolver) (*PluginProvider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("plugin metadata resolver is required")
	}

	return newPluginProvider(settings, resolver.MetadataProviderClient)
}

func newPluginProvider(
	settings map[string]string,
	clientFactory pluginMetadataClientFactory,
) (*PluginProvider, error) {
	if clientFactory == nil {
		return nil, fmt.Errorf("plugin metadata client factory is required")
	}

	installationIDText := settings["plugin_installation_id"]
	if installationIDText == "" {
		return nil, fmt.Errorf("plugin_installation_id is required")
	}
	installationID, err := strconv.Atoi(installationIDText)
	if err != nil {
		return nil, fmt.Errorf("parse plugin_installation_id %q: %w", installationIDText, err)
	}

	capabilityID := settings["capability_id"]
	if capabilityID == "" {
		return nil, fmt.Errorf("capability_id is required")
	}

	displayName := settings["display_name"]
	if displayName == "" {
		displayName = capabilityID
	}

	return &PluginProvider{
		installationID: installationID,
		capabilityID:   capabilityID,
		displayName:    displayName,
		clientFactory:  clientFactory,
	}, nil
}

func NewPluginProviderWithClientFactory(
	settings map[string]string,
	clientFactory pluginMetadataClientFactory,
) (*PluginProvider, error) {
	return newPluginProvider(settings, clientFactory)
}

// NewPluginProviderFromCapability constructs a PluginProvider directly from
// plugin capability data, without going through a settings map or registry.
func NewPluginProviderFromCapability(
	installationID int,
	capabilityID string,
	displayName string,
	resolver pluginMetadataResolver,
) (*PluginProvider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("plugin metadata resolver is required")
	}
	if displayName == "" {
		displayName = capabilityID
	}
	return &PluginProvider{
		installationID: installationID,
		capabilityID:   capabilityID,
		displayName:    displayName,
		clientFactory:  resolver.MetadataProviderClient,
	}, nil
}

func NewPluginProviderWithTypedResolver(
	settings map[string]string,
	resolver interface {
		MetadataProviderClient(
			ctx context.Context,
			installationID int,
			capabilityID string,
		) (*pluginhost.MetadataProviderClient, error)
	},
) (*PluginProvider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("plugin metadata resolver is required")
	}

	return newPluginProvider(settings, func(
		ctx context.Context,
		installationID int,
		capabilityID string,
	) (pluginMetadataClient, error) {
		return resolver.MetadataProviderClient(ctx, installationID, capabilityID)
	})
}

func (p *PluginProvider) Slug() string {
	return p.capabilityID
}

func (p *PluginProvider) Name() string {
	return p.displayName
}

func (p *PluginProvider) ForTypes() []string {
	return []string{"movie", "series"}
}

func (p *PluginProvider) Search(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	providerIDs, err := structFromStringMap(query.ProviderIDs)
	if err != nil {
		return nil, fmt.Errorf("encode provider ids for plugin search: %w", err)
	}

	response, err := client.Search(ctx, &pluginv1.SearchMetadataRequest{
		Query:       query.Title,
		ItemType:    query.ContentType,
		Year:        int32(query.Year),
		ProviderIds: providerIDs,
		Language:    query.Language,
	})
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(response.GetResults()))
	for _, result := range response.GetResults() {
		results = append(results, SearchResult{
			Name:        result.GetTitle(),
			Year:        int(result.GetYear()),
			ProviderIDs: mergePluginProviderIDs(p.capabilityID, result.GetProviderId(), result.GetProviderIds()),
			ImageURL:    result.GetImageUrl(),
			Overview:    result.GetOverview(),
			Provider:    p.Slug(),
		})
	}
	return results, nil
}

func (p *PluginProvider) GetMetadata(ctx context.Context, req MetadataRequest) (*MetadataResult, error) {
	providerID := req.ProviderIDs[p.capabilityID]
	if providerID == "" {
		return nil, nil
	}

	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	providerIDs, err := structFromStringMap(req.ProviderIDs)
	if err != nil {
		return nil, err
	}

	response, err := client.GetMetadata(ctx, &pluginv1.GetMetadataRequest{
		ProviderId:  providerID,
		ItemType:    req.ContentType,
		ProviderIds: providerIDs,
		Language:    req.Language,
		FilePath:    req.FilePath,
	})
	if err != nil {
		return nil, err
	}
	if response.GetItem() == nil {
		return nil, nil
	}

	return &MetadataResult{
		HasMetadata:       true,
		ProviderIDs:       mergePluginProviderIDs(p.capabilityID, response.GetItem().GetProviderId(), response.GetItem().GetProviderIds()),
		Title:             response.GetItem().GetTitle(),
		OriginalTitle:     response.GetItem().GetOriginalTitle(),
		SortTitle:         response.GetItem().GetSortTitle(),
		Overview:          response.GetItem().GetOverview(),
		Tagline:           response.GetItem().GetTagline(),
		Year:              int(response.GetItem().GetYear()),
		Runtime:           int(response.GetItem().GetRuntime()),
		Genres:            append([]string(nil), response.GetItem().GetGenres()...),
		Keywords:          keywordsFromPluginMetadata(response.GetItem().GetMetadata()),
		Studios:           append([]string(nil), response.GetItem().GetStudios()...),
		Networks:          append([]string(nil), response.GetItem().GetNetworks()...),
		Countries:         append([]string(nil), response.GetItem().GetCountries()...),
		OriginalLanguage:  response.GetItem().GetOriginalLanguage(),
		ContentRating:     response.GetItem().GetContentRating(),
		Ratings:           ratingsFromStruct(response.GetItem().GetRatings()),
		People:            peopleFromRecords(response.GetItem().GetPeople()),
		PosterPath:        response.GetItem().GetPosterPath(),
		PosterThumbhash:   response.GetItem().GetPosterThumbhash(),
		BackdropPath:      response.GetItem().GetBackdropPath(),
		ShowStatus:        response.GetItem().GetStatus(),
		BackdropThumbhash: response.GetItem().GetBackdropThumbhash(),
		LogoPath:          response.GetItem().GetLogoPath(),
		SeasonCount:       int(response.GetItem().GetSeasonCount()),
		FirstAirDate:      response.GetItem().GetFirstAirDate(),
		LastAirDate:       response.GetItem().GetLastAirDate(),
		AirTime:           response.GetItem().GetAirTime(),
		ReleaseDate:       response.GetItem().GetReleaseDate(),
	}, nil
}

func (p *PluginProvider) GetPersonDetail(ctx context.Context, req PersonDetailRequest) (*PersonDetailResult, error) {
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	providerIDs, err := structFromStringMap(req.ProviderIDs)
	if err != nil {
		return nil, err
	}

	response, err := client.GetPersonDetail(ctx, &pluginv1.GetPersonDetailRequest{
		ProviderIds: providerIDs,
		Language:    req.Language,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, nil
		}
		return nil, err
	}
	if response.GetPerson() == nil {
		return nil, nil
	}

	return &PersonDetailResult{
		Name:           response.GetPerson().GetName(),
		SortName:       response.GetPerson().GetSortName(),
		Bio:            response.GetPerson().GetBio(),
		BirthDate:      response.GetPerson().GetBirthDate(),
		DeathDate:      response.GetPerson().GetDeathDate(),
		Birthplace:     response.GetPerson().GetBirthplace(),
		Homepage:       response.GetPerson().GetHomepage(),
		PhotoPath:      response.GetPerson().GetPhotoPath(),
		PhotoThumbhash: response.GetPerson().GetPhotoThumbhash(),
		ProviderIDs:    stringMapFromStruct(response.GetPerson().GetProviderIds()),
	}, nil
}

func (p *PluginProvider) GetImages(ctx context.Context, req ImageRequest) ([]RemoteImage, error) {
	providerID := req.ProviderIDs[p.capabilityID]
	if providerID == "" {
		return nil, nil
	}

	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	providerIDs, err := structFromStringMap(req.ProviderIDs)
	if err != nil {
		return nil, err
	}

	response, err := client.GetImages(ctx, &pluginv1.GetImagesRequest{
		ProviderId:  providerID,
		ItemType:    req.ContentType,
		ProviderIds: providerIDs,
		Language:    req.Language,
	})
	if err != nil {
		return nil, err
	}

	images := make([]RemoteImage, 0, len(response.GetImages()))
	for _, image := range response.GetImages() {
		ri := RemoteImage{
			ProviderID: p.capabilityID,
			URL:        image.GetUrl(),
			Type:       imageTypeFromKind(image.GetKind()),
			Language:   image.GetLanguage(),
			Width:      int(image.GetWidth()),
			Height:     int(image.GetHeight()),
		}
		// Extract rating from the metadata struct if the plugin provided it.
		if md := image.GetMetadata(); md != nil {
			if v, ok := md.GetFields()["rating"]; ok {
				ri.Rating = v.GetNumberValue()
			}
		}
		images = append(images, ri)
	}
	return images, nil
}

func (p *PluginProvider) GetSeasons(ctx context.Context, req SeasonsRequest) ([]SeasonResult, error) {
	providerID := req.ProviderIDs[p.capabilityID]
	if providerID == "" {
		return nil, nil
	}

	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	providerIDs, err := structFromStringMap(req.ProviderIDs)
	if err != nil {
		return nil, err
	}

	response, err := client.GetSeasons(ctx, &pluginv1.GetSeasonsRequest{
		SeriesProviderId: providerID,
		ProviderIds:      providerIDs,
		Language:         req.Language,
	})
	if err != nil {
		return nil, err
	}

	seasons := make([]SeasonResult, 0, len(response.GetSeasons()))
	for _, season := range response.GetSeasons() {
		ids := mergePluginProviderIDs(p.capabilityID, season.GetProviderId(), season.GetProviderIds())
		seasons = append(seasons, SeasonResult{
			ContentID:    ids[p.capabilityID],
			SeasonNumber: int(season.GetSeasonNumber()),
			Title:        season.GetTitle(),
			Overview:     season.GetOverview(),
			AirDate:      season.GetAirDate(),
			PosterPath:   season.GetPosterPath(),
		})
	}
	return seasons, nil
}

func (p *PluginProvider) GetEpisodes(ctx context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	providerID := req.ProviderIDs[p.capabilityID]
	if providerID == "" {
		return nil, nil
	}

	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	providerIDs, err := structFromStringMap(req.ProviderIDs)
	if err != nil {
		return nil, err
	}

	response, err := client.GetEpisodes(ctx, &pluginv1.GetEpisodesRequest{
		SeriesProviderId: providerID,
		SeasonNumber:     int32(req.SeasonNumber),
		ProviderIds:      providerIDs,
		Language:         req.Language,
	})
	if err != nil {
		return nil, err
	}

	episodes := make([]EpisodeResult, 0, len(response.GetEpisodes()))
	for _, episode := range response.GetEpisodes() {
		ids := mergePluginProviderIDs(p.capabilityID, episode.GetProviderId(), episode.GetProviderIds())
		episodes = append(episodes, EpisodeResult{
			ContentID:     ids[p.capabilityID],
			ProviderIDs:   ids,
			SeasonNumber:  int(episode.GetSeasonNumber()),
			EpisodeNumber: int(episode.GetEpisodeNumber()),
			Title:         episode.GetTitle(),
			Overview:      episode.GetOverview(),
			AirDate:       episode.GetAirDate(),
			Runtime:       int(episode.GetRuntime()),
			Ratings:       ratingsFromStruct(episode.GetRatings()),
			StillPath:     episode.GetStillPath(),
		})
	}
	return episodes, nil
}

// ResolveImageURL resolves a single image path via the plugin's gRPC call.
// The path should be a bare path without the plugin prefix.
func (p *PluginProvider) ResolveImageURL(ctx context.Context, path string, variant string) (string, error) {
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return "", err
	}

	response, err := client.ResolveImageURL(ctx, &pluginv1.ResolveImageURLRequest{
		Path: path, Variant: variant,
	})
	if err != nil {
		return "", err
	}
	return response.GetUrl(), nil
}

// ResolveImageURLs resolves multiple image paths via a single plugin gRPC call.
// Paths should be bare paths without the plugin prefix.
func (p *PluginProvider) ResolveImageURLs(ctx context.Context, paths []string, variant string) (map[string]string, error) {
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, err
	}

	response, err := client.ResolveImageURLs(ctx, &pluginv1.ResolveImageURLsRequest{
		Paths: paths, Variant: variant,
	})
	if err != nil {
		return nil, err
	}
	return response.GetUrls(), nil
}

func mergePluginProviderIDs(capabilityID, providerID string, ids *structpb.Struct) map[string]string {
	merged := stringMapFromStruct(ids)
	if providerID != "" {
		merged[capabilityID] = providerID
	}
	return merged
}

func stringMapFromStruct(value *structpb.Struct) map[string]string {
	result := make(map[string]string)
	if value == nil {
		return result
	}
	for key, raw := range value.AsMap() {
		text, ok := raw.(string)
		if ok && text != "" {
			result[key] = text
		}
	}
	return result
}

func ratingsFromStruct(value *structpb.Struct) Ratings {
	var ratings Ratings
	if value == nil {
		return ratings
	}
	for key, raw := range value.AsMap() {
		number, ok := raw.(float64)
		if !ok {
			continue
		}
		switch key {
		case "imdb":
			ratings.IMDB = number
		case "tmdb":
			ratings.TMDB = number
		case "rt_critic":
			ratings.RTCritic = number
		case "rt_audience":
			ratings.RTAudience = number
		}
	}
	return ratings
}

func keywordsFromPluginMetadata(value *structpb.Struct) []string {
	if value == nil {
		return nil
	}

	raw, ok := value.AsMap()["keywords"]
	if !ok {
		return nil
	}

	var entries []any
	switch typed := raw.(type) {
	case []any:
		entries = typed
	case []string:
		entries = make([]any, 0, len(typed))
		for _, entry := range typed {
			entries = append(entries, entry)
		}
	default:
		return nil
	}

	keywords := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		text, ok := entry.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keywords = append(keywords, text)
	}
	return keywords
}

func structFromStringMap(value map[string]string) (*structpb.Struct, error) {
	if len(value) == 0 {
		return nil, nil
	}

	converted := make(map[string]any, len(value))
	for key, entry := range value {
		if entry == "" {
			continue
		}
		converted[key] = entry
	}
	if len(converted) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(converted)
}

func peopleFromRecords(records []*pluginv1.PersonRecord) []models.ItemPerson {
	if len(records) == 0 {
		return nil
	}

	people := make([]models.ItemPerson, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		people = append(people, models.ItemPerson{
			Person: models.Person{
				Name:           record.GetName(),
				TmdbID:         record.GetTmdbId(),
				TvdbID:         record.GetTvdbId(),
				ImdbID:         record.GetImdbId(),
				PlexGUID:       record.GetPlexGuid(),
				PhotoPath:      record.GetPhotoPath(),
				PhotoThumbhash: record.GetPhotoThumbhash(),
			},
			Kind:      personKindFromString(record.GetKind()),
			Character: record.GetCharacter(),
			SortOrder: int(record.GetSortOrder()),
		})
	}
	return people
}

func personKindFromString(value string) models.PersonKind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "actor":
		return models.PersonKindActor
	case "director":
		return models.PersonKindDirector
	case "writer":
		return models.PersonKindWriter
	case "producer":
		return models.PersonKindProducer
	case "gueststar", "guest_star", "guest star":
		return models.PersonKindGuestStar
	case "composer":
		return models.PersonKindComposer
	case "author":
		return models.PersonKindAuthor
	case "narrator":
		return models.PersonKindNarrator
	default:
		return models.PersonKindFromJob(value)
	}
}

func imageTypeFromKind(kind string) ImageType {
	switch kind {
	case "backdrop":
		return ImageBackdrop
	case "logo":
		return ImageLogo
	case "still":
		return ImageStill
	default:
		return ImagePoster
	}
}

package requests

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// DiscoverBrandCard is one card on the Studios / Networks / Genres carousels.
// Studios and networks carry a TMDB ID and a brand color plus lazy-fetched logo.
// Genres carry no TMDB ID at this layer and render with a gradient and display
// name instead of a logo.
type DiscoverBrandCard struct {
	TMDBID          int     `json:"tmdb_id,omitempty"`
	Slug            string  `json:"slug"`
	DisplayName     string  `json:"display_name"`
	BrandColor      string  `json:"brand_color,omitempty"`
	LogoURL         *string `json:"logo_url,omitempty"`
	GradientFrom    string  `json:"gradient_from,omitempty"`
	GradientTo      string  `json:"gradient_to,omitempty"`
	SeriesSupported bool    `json:"series_supported,omitempty"`
}

// ListStudios returns the bundled studios with lazily-fetched logo URLs.
// A failed logo lookup for any individual studio yields a card with LogoURL=nil;
// the response is never failed wholesale.
func (s *Service) ListStudios(ctx context.Context, _ Viewer) ([]DiscoverBrandCard, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	out := make([]DiscoverBrandCard, 0, len(BundledStudios))
	for _, studio := range BundledStudios {
		out = append(out, DiscoverBrandCard{
			TMDBID:      studio.TMDBID,
			Slug:        studio.Slug,
			DisplayName: studio.DisplayName,
			BrandColor:  studio.BrandColor,
			LogoURL:     s.resolveLogoURL(ctx, s.companyLogos, studio.TMDBID, "company", studio.Slug),
		})
	}
	return out, nil
}

// ListNetworks returns the bundled TV networks with lazily-fetched logo URLs.
func (s *Service) ListNetworks(ctx context.Context, _ Viewer) ([]DiscoverBrandCard, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	out := make([]DiscoverBrandCard, 0, len(BundledNetworks))
	for _, network := range BundledNetworks {
		out = append(out, DiscoverBrandCard{
			TMDBID:      network.TMDBID,
			Slug:        network.Slug,
			DisplayName: network.DisplayName,
			BrandColor:  network.BrandColor,
			LogoURL:     s.resolveLogoURL(ctx, s.networkLogos, network.TMDBID, "network", network.Slug),
		})
	}
	return out, nil
}

// ListGenres returns the bundled genres. Each card carries gradient hints
// (no logo URL) and a SeriesSupported flag for the browse page to decide
// whether to show the Series tab.
func (s *Service) ListGenres(_ context.Context, _ Viewer) ([]DiscoverBrandCard, error) {
	if s == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	out := make([]DiscoverBrandCard, 0, len(BundledGenres))
	for _, genre := range BundledGenres {
		out = append(out, DiscoverBrandCard{
			Slug:            genre.Slug,
			DisplayName:     genre.DisplayName,
			GradientFrom:    genre.GradientFrom,
			GradientTo:      genre.GradientTo,
			SeriesSupported: genre.SeriesID > 0,
		})
	}
	return out, nil
}

// resolveLogoURL fetches the cached logo path and renders it as a TMDB image
// URL. It returns nil on lookup failure or when TMDB has no logo for the entity.
func (s *Service) resolveLogoURL(ctx context.Context, cache *logoCache, id int, kind, slug string) *string {
	if cache == nil {
		return nil
	}
	path, err := cache.Get(ctx, id)
	if err != nil {
		slog.Warn("requests: logo lookup failed", "kind", kind, "slug", slug, "id", id, "error", err)
		return nil
	}
	if path == "" {
		return nil
	}
	url := tmdbImageURL(path, "w300")
	return &url
}

// tmdbImageURL is the public TMDB image CDN URL for a given file path and size.
func tmdbImageURL(path, size string) string {
	return "https://image.tmdb.org/t/p/" + size + path
}

// DiscoverBrowseResponse is the shape returned by the browse endpoints.
// Results share the same MediaResult shape as search and the existing
// discovery sections, so the frontend can reuse RequestPosterCard.
type DiscoverBrowseResponse struct {
	Kind        string        `json:"kind"`
	Slug        string        `json:"slug"`
	DisplayName string        `json:"display_name"`
	BrandColor  string        `json:"brand_color,omitempty"`
	LogoURL     *string       `json:"logo_url,omitempty"`
	MediaType   MediaType     `json:"media_type"`
	Sort        string        `json:"sort"`
	Page        int           `json:"page"`
	TotalPages  int           `json:"total_pages"`
	Results     []MediaResult `json:"results"`
}

var validBrowseSorts = map[string]string{
	"popularity":   "popularity.desc",
	"vote_average": "vote_average.desc",
	"release_date": "primary_release_date.desc",
}

const defaultBrowseSort = "popularity"

// BrowseStudio returns a page of movies from a bundled studio, enriched with
// Silo availability and request state.
func (s *Service) BrowseStudio(ctx context.Context, viewer Viewer, slug, sort string, page int) (*DiscoverBrowseResponse, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	studio, ok := FindStudioBySlug(strings.TrimSpace(slug))
	if !ok {
		return nil, ErrNotFound
	}
	tmdbSort, sortKey, err := normalizeBrowseSort(sort, "movie")
	if err != nil {
		return nil, err
	}
	tmdbPage, err := s.tmdb.DiscoverPage(ctx, "movie", tmdb.DiscoverParams{
		SortBy:        tmdbSort,
		WithCompanies: []int{studio.TMDBID},
		VoteCountGte:  voteCountFloorForSort(sortKey),
	}, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, tmdbPage)
	if err != nil {
		return nil, err
	}
	return &DiscoverBrowseResponse{
		Kind:        "studio",
		Slug:        studio.Slug,
		DisplayName: studio.DisplayName,
		BrandColor:  studio.BrandColor,
		LogoURL:     s.resolveLogoURL(ctx, s.companyLogos, studio.TMDBID, "company", studio.Slug),
		MediaType:   MediaTypeMovie,
		Sort:        sortKey,
		Page:        enriched.Page,
		TotalPages:  enriched.TotalPages,
		Results:     enriched.Results,
	}, nil
}

// BrowseNetwork returns a page of series from a bundled TV network.
func (s *Service) BrowseNetwork(ctx context.Context, viewer Viewer, slug, sort string, page int) (*DiscoverBrowseResponse, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	network, ok := FindNetworkBySlug(strings.TrimSpace(slug))
	if !ok {
		return nil, ErrNotFound
	}
	tmdbSort, sortKey, err := normalizeBrowseSort(sort, "tv")
	if err != nil {
		return nil, err
	}
	tmdbPage, err := s.tmdb.DiscoverPage(ctx, "tv", tmdb.DiscoverParams{
		SortBy:       tmdbSort,
		WithNetworks: []int{network.TMDBID},
		VoteCountGte: voteCountFloorForSort(sortKey),
	}, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, tmdbPage)
	if err != nil {
		return nil, err
	}
	return &DiscoverBrowseResponse{
		Kind:        "network",
		Slug:        network.Slug,
		DisplayName: network.DisplayName,
		BrandColor:  network.BrandColor,
		LogoURL:     s.resolveLogoURL(ctx, s.networkLogos, network.TMDBID, "network", network.Slug),
		MediaType:   MediaTypeSeries,
		Sort:        sortKey,
		Page:        enriched.Page,
		TotalPages:  enriched.TotalPages,
		Results:     enriched.Results,
	}, nil
}

// BrowseGenre returns a page of movies or series from a bundled genre.
func (s *Service) BrowseGenre(ctx context.Context, viewer Viewer, slug string, rawMediaType MediaType, sort string, page int) (*DiscoverBrowseResponse, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	genre, ok := FindGenreBySlug(strings.TrimSpace(slug))
	if !ok {
		return nil, ErrNotFound
	}
	mediaType, err := normalizeMediaType(rawMediaType)
	if err != nil {
		return nil, fmt.Errorf("%w: media_type is required for genre browse", ErrInvalidInput)
	}

	var (
		tmdbMediaType string
		genreID       int
	)
	switch mediaType {
	case MediaTypeMovie:
		tmdbMediaType = "movie"
		genreID = genre.MovieID
	case MediaTypeSeries:
		tmdbMediaType = "tv"
		genreID = genre.SeriesID
	}
	if genreID == 0 {
		return nil, fmt.Errorf("%w: %s has no %s equivalent", ErrInvalidInput, slug, mediaType)
	}

	tmdbSort, sortKey, err := normalizeBrowseSort(sort, tmdbMediaType)
	if err != nil {
		return nil, err
	}
	tmdbPage, err := s.tmdb.DiscoverPage(ctx, tmdbMediaType, tmdb.DiscoverParams{
		SortBy:       tmdbSort,
		WithGenres:   []int{genreID},
		VoteCountGte: voteCountFloorForSort(sortKey),
	}, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, tmdbPage)
	if err != nil {
		return nil, err
	}
	return &DiscoverBrowseResponse{
		Kind:        "genre",
		Slug:        genre.Slug,
		DisplayName: genre.DisplayName,
		MediaType:   mediaType,
		Sort:        sortKey,
		Page:        enriched.Page,
		TotalPages:  enriched.TotalPages,
		Results:     enriched.Results,
	}, nil
}

func normalizeBrowseSort(sort, tmdbMediaType string) (string, string, error) {
	sort = strings.TrimSpace(sort)
	if sort == "" {
		sort = defaultBrowseSort
	}
	tmdbSort, ok := validBrowseSorts[sort]
	if !ok {
		return "", "", fmt.Errorf("%w: unknown sort %q", ErrInvalidInput, sort)
	}
	if sort == "release_date" && tmdbMediaType == "tv" {
		tmdbSort = "first_air_date.desc"
	}
	return tmdbSort, sort, nil
}

func voteCountFloorForSort(sortKey string) int {
	if sortKey == "vote_average" {
		return 100
	}
	return 0
}

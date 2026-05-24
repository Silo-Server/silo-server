package requests

import (
	"context"
	"fmt"
	"log/slog"
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

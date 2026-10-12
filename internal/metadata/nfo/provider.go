package nfo

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Provider reads metadata from NFO sidecar files. Its caches let the
// identity-hint, search, and metadata phases of a pass share one directory
// listing and one parse per file; the provider lives as long as its cached
// provider chain.
type Provider struct {
	parses   boundedCache[[sha256.Size]byte, *parsedNFO]
	listings boundedCache[dirCacheKey, *dirListing]
}

func NewProvider() *Provider { return &Provider{} }

// providerSlug is the NFO provider's capability id.
const providerSlug = "nfo"

func (p *Provider) Slug() string { return providerSlug }

// RatingSources declares the rating sources beyond IMDb and TMDB the provider
// reads from local .nfo files: the Rotten Tomatoes scores it stores in the
// rt_critic and rt_audience rating columns, and the other named ratings Kodi
// writers carry, so an administrator can choose to show them.
func (p *Provider) RatingSources() []models.RatingSourceDefinition {
	return slices.Clone(ratingSourceDefinitions)
}
func (p *Provider) Name() string       { return "NFO Files" }
func (p *Provider) ForTypes() []string { return []string{typeMovie, typeSeries} }

// IdentityHints implements metadata.IdentityHintProvider: the external IDs a
// curated NFO declares (<uniqueid>, the legacy id forms, an episodeguide, or
// a provider link) are trusted identity hints that anchor Phase-1 candidate
// selection. A title-only NFO contributes no hints; its title participates
// only as a defanged search candidate. An NFO that cannot be read yields no
// hints; Search and GetMetadata report the read error.
func (p *Provider) IdentityHints(ctx context.Context, query metadata.SearchQuery) map[string]string {
	parsed, err := p.findNFOForQuery(ctx, query)
	if err != nil {
		slog.WarnContext(ctx, "nfo: cannot read NFO for identity hints", "component", "metadata", "error", err)
		return nil
	}
	if parsed == nil {
		return nil
	}
	hints := parsed.providerIDs()
	if len(hints) == 0 {
		return nil
	}
	return hints
}

// Search extracts external IDs from NFO for matching.
func (p *Provider) Search(ctx context.Context, query metadata.SearchQuery) ([]metadata.SearchResult, error) {
	parsed, err := p.findNFOForQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, nil // missing/unparseable/mismatched NFOs are not fatal
	}
	ids := parsed.providerIDs()
	if len(ids) == 0 && parsed.Title == "" {
		return nil, nil
	}
	return []metadata.SearchResult{{
		Name:        parsed.Title,
		Year:        parsed.Year,
		ProviderIDs: ids,
		Provider:    p.Slug(),
	}}, nil
}

// GetMetadata parses full metadata from an NFO file. Like Search, it carries
// the ContentType guard (enforced inside findItemNFO): a tvshow.nfo next to a
// movie file must not inject series data into a movie item at top priority.
// A URL-only NFO names the item but carries no fields, so it returns no
// metadata. An NFO that exists but cannot be read returns an error wrapping
// metadata.ErrLocalMetadataUnavailable, never an empty result.
//
// An actor's <thumb> comes back in the credit's LocalPhotoSource, resolved
// to an image source (see artResolver); the metadata service decides whether
// the person takes it.
func (p *Provider) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	mediaFiles, sidecarDirs := requestPaths(req)
	nfoPath, parsed, err := p.newLookup(ctx).findItemNFO(mediaFiles, sidecarDirs, req.ContentType)
	if err != nil {
		return nil, err
	}
	if parsed == nil || parsed.LinkOnly {
		return &metadata.MetadataResult{}, nil
	}
	resolvePeoplePhotos(parsed.People, newArtResolver(newSidecarFinder(ctx), nfoPath, itemFolders(sidecarDirs, mediaFiles)))
	return &metadata.MetadataResult{
		HasMetadata:      true,
		Title:            parsed.Title,
		OriginalTitle:    parsed.OriginalTitle,
		SortTitle:        parsed.SortTitle,
		Tagline:          parsed.Tagline,
		Year:             parsed.Year,
		Overview:         parsed.Overview,
		Runtime:          parsed.Runtime,
		ReleaseDate:      parsed.ReleaseDate,
		FirstAirDate:     parsed.FirstAirDate,
		ContentRating:    parsed.ContentRating,
		OriginalLanguage: parsed.OriginalLanguage,
		ShowStatus:       parsed.ShowStatus,
		Genres:           parsed.Genres,
		Studios:          parsed.Studios,
		Networks:         parsed.Networks,
		Countries:        parsed.Countries,
		Keywords:         parsed.Keywords,
		Ratings: metadata.Ratings{
			IMDB:       parsed.RatingIMDB,
			TMDB:       parsed.RatingTMDB,
			RTCritic:   parsed.RatingRTCritic,
			RTAudience: parsed.RatingRTAudience,
		},
		RatingSources: parsed.ratingSources(p.Slug()),
		People:        parsed.People,
		Videos:        parsed.Videos,
		ProviderIDs:   parsed.providerIDs(),
	}, nil
}

// providerIDs returns the tmdb/imdb/tvdb ids the NFO declares.
func (p *parsedNFO) providerIDs() map[string]string {
	ids := make(map[string]string)
	if p.TmdbID != "" {
		ids[idKeyTMDB] = p.TmdbID
	}
	if p.ImdbID != "" {
		ids[idKeyIMDB] = p.ImdbID
	}
	if p.TvdbID != "" {
		ids[idKeyTVDB] = p.TvdbID
	}
	return ids
}

func (p *Provider) findNFOForQuery(ctx context.Context, query metadata.SearchQuery) (*parsedNFO, error) {
	mediaFiles := compactNFOPaths(append([]string{
		query.FilePath,
		query.RepresentativeFilePath,
		query.ProviderIDs["_filepath"],
	}, query.AllGroupFilePaths...))
	_, parsed, err := p.newLookup(ctx).findItemNFO(mediaFiles, compactNFOPaths(query.PrimarySidecarSearchPaths), query.ContentType)
	return parsed, err
}

// requestPaths returns a metadata request's media files and directory-level
// sidecar folders.
func requestPaths(req metadata.MetadataRequest) (mediaFiles, sidecarDirs []string) {
	mediaFiles = compactNFOPaths(append([]string{req.FilePath, req.RepresentativeFilePath}, req.AllGroupFilePaths...))
	return mediaFiles, compactNFOPaths(req.PrimarySidecarSearchPaths)
}

func compactNFOPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || clean == "." {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

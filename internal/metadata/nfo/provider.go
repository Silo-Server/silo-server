package nfo

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Provider reads metadata from NFO sidecar files.
type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Slug() string       { return "nfo" }
func (p *Provider) Name() string       { return "NFO Files" }
func (p *Provider) ForTypes() []string { return []string{typeMovie, typeSeries} }

// IdentityHints implements metadata.IdentityHintProvider: the external IDs a
// curated NFO declares (<uniqueid> tmdb/imdb/tvdb) are trusted identity hints
// that anchor Phase-1 candidate selection. A title-only NFO contributes no
// hints; its title participates only as a defanged search candidate.
func (p *Provider) IdentityHints(_ context.Context, query metadata.SearchQuery) map[string]string {
	parsed := findNFOForQuery(query)
	if parsed == nil {
		return nil
	}
	hints := make(map[string]string)
	if parsed.TmdbID != "" {
		hints["tmdb"] = parsed.TmdbID
	}
	if parsed.ImdbID != "" {
		hints["imdb"] = parsed.ImdbID
	}
	if parsed.TvdbID != "" {
		hints["tvdb"] = parsed.TvdbID
	}
	if len(hints) == 0 {
		return nil
	}
	return hints
}

// Search extracts external IDs from NFO for matching.
func (p *Provider) Search(ctx context.Context, query metadata.SearchQuery) ([]metadata.SearchResult, error) {
	parsed := findNFOForQuery(query)
	if parsed == nil {
		return nil, nil // missing/unreadable/mismatched NFOs are not fatal
	}
	ids := make(map[string]string)
	if parsed.TmdbID != "" {
		ids["tmdb"] = parsed.TmdbID
	}
	if parsed.ImdbID != "" {
		ids["imdb"] = parsed.ImdbID
	}
	if parsed.TvdbID != "" {
		ids["tvdb"] = parsed.TvdbID
	}
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
// the ContentType guard (enforced inside findNFO): a tvshow.nfo next to a
// movie file must not inject series data into a movie item at top priority.
func (p *Provider) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	parsed := findNFOForRequest(req)
	if parsed == nil {
		return &metadata.MetadataResult{}, nil
	}
	result := &metadata.MetadataResult{
		HasMetadata:   true,
		Title:         parsed.Title,
		OriginalTitle: parsed.OriginalTitle,
		Tagline:       parsed.Tagline,
		Year:          parsed.Year,
		Overview:      parsed.Overview,
		Runtime:       parsed.Runtime,
		ReleaseDate:   parsed.ReleaseDate,
		FirstAirDate:  parsed.FirstAirDate,
		ContentRating: parsed.ContentRating,
		Genres:        parsed.Genres,
		Studios:       parsed.Studios,
		Countries:     parsed.Countries,
		Keywords:      parsed.Keywords,
		Ratings: metadata.Ratings{
			IMDB:       parsed.RatingIMDB,
			TMDB:       parsed.RatingTMDB,
			RTCritic:   parsed.RatingRTCritic,
			RTAudience: parsed.RatingRTAudience,
		},
		People:      parsed.People,
		ProviderIDs: make(map[string]string),
	}
	if parsed.TmdbID != "" {
		result.ProviderIDs["tmdb"] = parsed.TmdbID
	}
	if parsed.ImdbID != "" {
		result.ProviderIDs["imdb"] = parsed.ImdbID
	}
	if parsed.TvdbID != "" {
		result.ProviderIDs["tvdb"] = parsed.TvdbID
	}
	return result, nil
}

func findNFOForQuery(query metadata.SearchQuery) *parsedNFO {
	candidatePaths := candidateSidecarPaths(
		query.FilePath,
		query.RepresentativeFilePath,
		query.AllGroupFilePaths,
		query.PrimarySidecarSearchPaths,
		query.ProviderIDs["_filepath"],
	)
	_, parsed := findNFO(candidatePaths, query.ContentType)
	return parsed
}

func findNFOForRequest(req metadata.MetadataRequest) *parsedNFO {
	candidatePaths := candidateSidecarPaths(
		req.FilePath,
		req.RepresentativeFilePath,
		req.AllGroupFilePaths,
		req.PrimarySidecarSearchPaths,
	)
	_, parsed := findNFO(candidatePaths, req.ContentType)
	return parsed
}

func candidateSidecarPaths(primary string, representative string, groupFiles []string, searchPaths []string, extras ...string) []string {
	candidates := make([]string, 0, 2+len(groupFiles)+len(searchPaths)+len(extras))
	for _, path := range []string{primary, representative} {
		if strings.TrimSpace(path) != "" {
			candidates = append(candidates, path)
		}
	}
	candidates = append(candidates, groupFiles...)
	candidates = append(candidates, searchPaths...)
	candidates = append(candidates, extras...)
	return compactNFOPaths(candidates)
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

// findNFO locates the best candidate NFO file across media-file and directory
// search paths. File paths check legacy directory-level sidecars first, then a
// basename-matched sidecar in the same directory. A candidate that exists but
// fails to parse, or whose root type mismatches contentType, falls through to
// the next candidate — a stray movie.nfo in a series root must not shadow
// tvshow.nfo, and a tvshow.nfo beside a movie file must not inject series
// data. Returns the winning path and its parsed contents, or ("", nil).
func findNFO(paths []string, contentType string) (string, *parsedNFO) {
	candidates := make([]string, 0, len(paths)*3)
	for _, path := range paths {
		candidates = append(candidates, nfoCandidatesForPath(path)...)
	}
	candidates = compactNFOPaths(candidates)
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		parsed, err := parseNFOData(data)
		if err != nil {
			continue
		}
		if contentType != "" && parsed.Type != "" && parsed.Type != contentType {
			continue
		}
		return c, parsed
	}
	return "", nil
}

func nfoCandidatesForPath(path string) []string {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return directoryLevelNFOCandidates(path)
		}
		return fileLevelNFOCandidates(path)
	}
	return append(fileLevelNFOCandidates(path), directoryLevelNFOCandidates(path)...)
}

func fileLevelNFOCandidates(path string) []string {
	ext := filepath.Ext(path)
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	return []string{
		filepath.Join(dir, "movie.nfo"),
		filepath.Join(dir, "tvshow.nfo"),
		filepath.Join(dir, base+".nfo"),
	}
}

func directoryLevelNFOCandidates(path string) []string {
	return []string{
		filepath.Join(path, "movie.nfo"),
		filepath.Join(path, "tvshow.nfo"),
	}
}

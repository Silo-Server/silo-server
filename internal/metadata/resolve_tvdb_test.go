package metadata

import (
	"context"
	"testing"
	"time"
)

// idSearchProvider answers a provider-ID-only search from a fixed table keyed
// by the single ID it was queried with ("imdb:tt1", "tmdb:42").
type idSearchProvider struct {
	slug    string
	results map[string][]SearchResult
	queries []map[string]string
}

func (p *idSearchProvider) Slug() string       { return p.slug }
func (p *idSearchProvider) Name() string       { return p.slug }
func (p *idSearchProvider) ForTypes() []string { return []string{"series"} }

func (p *idSearchProvider) Search(_ context.Context, query SearchQuery) ([]SearchResult, error) {
	p.queries = append(p.queries, query.ProviderIDs)
	for key, value := range query.ProviderIDs {
		return p.results[key+":"+value], nil
	}
	return nil, nil
}

func serviceWithSeriesChain(providers ...Provider) *MetadataService {
	return &MetadataService{
		chainCache: map[string]chainCacheEntry{
			"0:series": {providers: providers, expiresAt: time.Now().Add(time.Hour)},
		},
		chainCacheTTL: time.Hour,
	}
}

func TestResolveSeriesTVDBIDPrefersIMDbLookup(t *testing.T) {
	tvdb := &idSearchProvider{slug: "tvdb", results: map[string][]SearchResult{
		"imdb:tt31000000": {{ProviderIDs: map[string]string{"tvdb": "456789", "imdb": "tt31000000"}}},
		"tmdb:240001":     {{ProviderIDs: map[string]string{"tvdb": "111", "tmdb": "240001"}}},
	}}
	got, err := serviceWithSeriesChain(tvdb).ResolveSeriesTVDBID(context.Background(), 240001, "tt31000000")
	if err != nil {
		t.Fatalf("ResolveSeriesTVDBID error = %v", err)
	}
	if got != 456789 {
		t.Fatalf("tvdb id = %d, want 456789 from the IMDb lookup", got)
	}
	if len(tvdb.queries) != 1 {
		t.Fatalf("queries = %v, want only the IMDb lookup", tvdb.queries)
	}
}

func TestResolveSeriesTVDBIDFallsBackToTMDBLookup(t *testing.T) {
	tvdb := &idSearchProvider{slug: "tvdb", results: map[string][]SearchResult{
		"tmdb:240001": {{ProviderIDs: map[string]string{"tvdb": "456789", "tmdb": "240001"}}},
	}}
	got, err := serviceWithSeriesChain(tvdb).ResolveSeriesTVDBID(context.Background(), 240001, "tt31000000")
	if err != nil {
		t.Fatalf("ResolveSeriesTVDBID error = %v", err)
	}
	if got != 456789 {
		t.Fatalf("tvdb id = %d, want 456789 from the TMDB lookup", got)
	}
}

func TestResolveSeriesTVDBIDRejectsResultThatDoesNotEchoTheLookupID(t *testing.T) {
	// A TMDB number that collides with another source's ID on TVDB comes back
	// as a series whose own TMDB cross-reference differs (or is absent).
	tvdb := &idSearchProvider{slug: "tvdb", results: map[string][]SearchResult{
		"tmdb:240001": {
			{ProviderIDs: map[string]string{"tvdb": "999"}},
			{ProviderIDs: map[string]string{"tvdb": "998", "tmdb": "77"}},
		},
	}}
	// A provider that doesn't know TVDB at all contributes nothing.
	tmdbOnly := &idSearchProvider{slug: "tmdb", results: map[string][]SearchResult{
		"tmdb:240001": {{ProviderIDs: map[string]string{"tmdb": "240001"}}},
	}}
	got, err := serviceWithSeriesChain(tmdbOnly, tvdb).ResolveSeriesTVDBID(context.Background(), 240001, "")
	if err != nil {
		t.Fatalf("ResolveSeriesTVDBID error = %v", err)
	}
	if got != 0 {
		t.Fatalf("tvdb id = %d, want 0 when no result confirms the TMDB id", got)
	}
}

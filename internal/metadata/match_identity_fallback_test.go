//nolint:goconst // Repeated titles and provider IDs document each fallback scenario.
package metadata

import (
	"context"
	"sync"
	"testing"
)

type identityFallbackProvider struct {
	mu      sync.Mutex
	queries []SearchQuery
}

type typeMismatchProvider struct{}

func (p *typeMismatchProvider) Slug() string       { return "tmdb" }
func (p *typeMismatchProvider) Name() string       { return "TMDB" }
func (p *typeMismatchProvider) ForTypes() []string { return []string{"movie", "series"} }
func (p *typeMismatchProvider) Search(_ context.Context, query SearchQuery) ([]SearchResult, error) {
	if query.ContentType != "series" || query.ProviderIDs["imdb"] != "tt10473306" {
		return nil, nil
	}
	return []SearchResult{{
		Name: "Are You Afraid of the Dark?", Year: 2019, Provider: "tmdb",
		ProviderIDs: map[string]string{"imdb": "tt10473306", "tmdb": "83755"},
	}}, nil
}
func (p *typeMismatchProvider) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return &MetadataResult{}, nil
}

func (p *identityFallbackProvider) Slug() string       { return "tmdb" }
func (p *identityFallbackProvider) Name() string       { return "TMDB" }
func (p *identityFallbackProvider) ForTypes() []string { return []string{"movie"} }
func (p *identityFallbackProvider) Search(_ context.Context, query SearchQuery) ([]SearchResult, error) {
	p.mu.Lock()
	p.queries = append(p.queries, query)
	p.mu.Unlock()
	if query.Title != "Alien Invasion" || query.Year != 2018 {
		return nil, nil
	}
	return []SearchResult{{
		Name: "Alien Invasion", Year: 2018, Provider: "tmdb",
		ProviderIDs: map[string]string{"tmdb": "4242"},
	}}, nil
}
func (p *identityFallbackProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	return &MetadataResult{
		HasMetadata: true, Title: "Alien Invasion", Year: 2018,
		ProviderIDs: map[string]string{"tmdb": req.ProviderIDs["tmdb"]},
	}, nil
}

func TestInitialMatchUsesAlternatePathIdentityAfterPrimaryMiss(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	provider := &identityFallbackProvider{}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{
			Title: "After the Lethargy", Year: 2018, Type: "movie",
			AlternateIdentities: []MatchIdentityHint{{Title: "Alien Invasion", Year: 2018, Source: "filename"}},
		},
		Mode: ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || !result.Updated || result.Decision == nil || result.Decision.Outcome != "matched" {
		t.Fatalf("result = %#v, want alternate-identity match", result)
	}
	item, err := harness.itemRepo.GetByID(context.Background(), result.ContentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "Alien Invasion" || item.TmdbID != "4242" {
		t.Fatalf("item = title %q tmdb %q", item.Title, item.TmdbID)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.queries) != 2 || provider.queries[1].Title != "Alien Invasion" {
		t.Fatalf("queries = %#v, want primary then filename fallback", provider.queries)
	}
}

func TestCompactAlternateMatchIdentitiesDeduplicatesAndBounds(t *testing.T) {
	t.Parallel()
	hints := &MatchHints{
		Title: "Primary", Year: 2020,
		AlternateIdentities: []MatchIdentityHint{
			{Title: "Primary", Year: 2020},
			{Title: "A", Year: 2020},
			{Title: "A", Year: 2020},
			{Title: "B", Year: 2021},
			{Title: "C", Year: 2022},
			{Title: "D", Year: 2023},
		},
	}

	got := compactAlternateMatchIdentities(hints)
	if len(got) != maxProviderIdentityFallbacks || got[0].Title != "A" || got[2].Title != "C" {
		t.Fatalf("compact identities = %#v", got)
	}
}

func TestInitialMatchDiagnosesTrustedIDInOppositeContentType(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{
			Title: "Are You Afraid of the Dark!", Year: 2019, Type: "movie", ImdbID: "tt10473306",
		},
		Mode: ModeInitialMatch,
	}, []Provider{&typeMismatchProvider{}})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != "trusted_id_type_mismatch" {
		t.Fatalf("result = %#v, want trusted-ID type mismatch", result)
	}
	if result.Decision.CandidateCount != 1 {
		t.Fatalf("decision = %+v, want one opposite-type candidate", result.Decision)
	}
}

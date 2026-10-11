//nolint:goconst // Repeated titles and provider IDs document each alias scenario.
package metadata

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// aliasProvider answers every search with results and serves detail aliases
// per TMDB ID, the way the TMDB plugin's search carries only the localized and
// original titles while its detail response lists alternative titles.
type aliasProvider struct {
	slug      string
	results   []SearchResult
	aliases   map[string][]string
	detailErr error
	idErrs    map[string]error

	mu          sync.Mutex
	detailCalls []string
}

func (p *aliasProvider) Slug() string       { return p.slug }
func (p *aliasProvider) Name() string       { return p.slug }
func (p *aliasProvider) ForTypes() []string { return []string{"movie", "series"} }
func (p *aliasProvider) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return p.results, nil
}

func (p *aliasProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	p.mu.Lock()
	p.detailCalls = append(p.detailCalls, req.ProviderIDs["tmdb"])
	p.mu.Unlock()
	if p.detailErr != nil {
		return nil, p.detailErr
	}
	if err := p.idErrs[req.ProviderIDs["tmdb"]]; err != nil {
		return nil, err
	}
	result := &MetadataResult{
		HasMetadata:          true,
		Title:                "12.12: The Day",
		OriginalTitle:        "서울의 봄",
		Year:                 2023,
		ProviderIDs:          map[string]string{"tmdb": req.ProviderIDs["tmdb"]},
		TitleAliasesComplete: true,
	}
	for _, alias := range p.aliases[req.ProviderIDs["tmdb"]] {
		result.TitleAliases = append(result.TitleAliases, TitleAlias{Title: alias})
	}
	return result, nil
}

func (p *aliasProvider) detailCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.detailCalls)
}

func seoulSpringSearchResult(tmdbID string, year int) SearchResult {
	return SearchResult{
		Name: "12.12: The Day", OriginalTitle: "서울의 봄", Year: year, Provider: "tmdb",
		ProviderIDs:  map[string]string{"tmdb": tmdbID},
		TitleAliases: []TitleAlias{{Title: "서울의 봄", Language: "ko", Kind: "original"}},
	}
}

func TestInitialMatchAcceptsResultWhoseDetailsListTheFolderTitle(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	provider := &aliasProvider{
		slug:    "tmdb",
		results: []SearchResult{seoulSpringSearchResult("919207", 2023)},
		aliases: map[string][]string{"919207": {"Seoul Spring", "首尔之春", "首爾之春"}},
	}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{Title: "首尔之春", Year: 2023, Type: "movie"},
		Mode:  ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != "matched" {
		t.Fatalf("result = %#v, want a match confirmed by the provider's alternative titles", result)
	}
	item, err := harness.itemRepo.GetByID(context.Background(), result.ContentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.TmdbID != "919207" {
		t.Fatalf("item tmdb = %q, want 919207", item.TmdbID)
	}
}

func TestInitialMatchLeavesUnrelatedSameYearResultUnmatched(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	provider := &aliasProvider{
		slug:    "tmdb",
		results: []SearchResult{seoulSpringSearchResult("919207", 2023)},
		aliases: map[string][]string{"919207": {"Seoul Spring"}},
	}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{Title: "首尔之春", Year: 2023, Type: "movie"},
		Mode:  ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result != nil && result.Decision != nil && result.Decision.Outcome == "matched" {
		t.Fatalf("outcome = matched, want unmatched when no provider title matches the folder")
	}
}

func TestValidateMatchByProviderTitleAliases(t *testing.T) {
	t.Parallel()
	hints := &MatchHints{Title: "首尔之春", Year: 2023, Type: "movie"}

	tests := []struct {
		name        string
		hints       *MatchHints
		candidates  []SearchResult
		aliases     map[string][]string
		detailErr   error
		idErrs      map[string]error
		sources     []string
		wantID      string
		wantErr     bool
		wantDetails int
	}{
		{
			name:        "single same-year result listing the title",
			hints:       hints,
			candidates:  []SearchResult{seoulSpringSearchResult("919207", 2023)},
			aliases:     map[string][]string{"919207": {"首尔之春"}},
			wantID:      "919207",
			wantDetails: 1,
		},
		{
			name:        "two same-year results both listing the title are ambiguous",
			hints:       hints,
			candidates:  []SearchResult{seoulSpringSearchResult("919207", 2023), seoulSpringSearchResult("111", 2023)},
			aliases:     map[string][]string{"919207": {"首尔之春"}, "111": {"首尔之春"}},
			wantDetails: 2,
		},
		{
			name:       "a different year is never fetched",
			hints:      hints,
			candidates: []SearchResult{seoulSpringSearchResult("919207", 2021)},
			aliases:    map[string][]string{"919207": {"首尔之春"}},
		},
		{
			name:       "no local year is never fetched",
			hints:      &MatchHints{Title: "首尔之春", Type: "movie"},
			candidates: []SearchResult{seoulSpringSearchResult("919207", 2023)},
			aliases:    map[string][]string{"919207": {"首尔之春"}},
		},
		{
			name:  "more same-year results than the cap are left alone",
			hints: hints,
			candidates: []SearchResult{
				seoulSpringSearchResult("1", 2023), seoulSpringSearchResult("2", 2023),
				seoulSpringSearchResult("3", 2023), seoulSpringSearchResult("4", 2023),
			},
			aliases: map[string][]string{"1": {"首尔之春"}},
		},
		{
			name:        "a detail error leaves the item unmatched",
			hints:       hints,
			candidates:  []SearchResult{seoulSpringSearchResult("919207", 2023)},
			detailErr:   errors.New("provider unavailable"),
			wantErr:     true,
			wantDetails: 1,
		},
		{
			name:  "an exact-title result means the search was ambiguous, not unrelated",
			hints: hints,
			candidates: []SearchResult{
				seoulSpringSearchResult("919207", 2023),
				{Name: "首尔之春", Year: 2023, ProviderIDs: map[string]string{"tmdb": "222"}},
			},
			aliases: map[string][]string{"919207": {"首尔之春"}},
		},
		{
			name:  "a rival the provider no longer knows doesn't block the match",
			hints: hints,
			candidates: []SearchResult{
				seoulSpringSearchResult("919207", 2023), seoulSpringSearchResult("404404", 2023),
			},
			aliases:     map[string][]string{"919207": {"首尔之春"}},
			idErrs:      map[string]error{"404404": errors.New("tmdb: HTTP 404: not found")},
			wantID:      "919207",
			wantDetails: 2,
		},
		{
			name:       "a title with no letters or digits is never fetched",
			hints:      &MatchHints{Title: "★", Year: 2023, Type: "movie"},
			candidates: []SearchResult{seoulSpringSearchResult("919207", 2023)},
			aliases:    map[string][]string{"919207": {"-"}},
		},
		{
			name:       "a local sidecar source never confirms a result",
			hints:      hints,
			candidates: []SearchResult{seoulSpringSearchResult("919207", 2023)},
			aliases:    map[string][]string{"919207": {"首尔之春"}},
			sources:    []string{"nfo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			slug := "tmdb"
			if len(tt.sources) > 0 {
				slug = tt.sources[0]
			}
			results := make([]SearchResult, 0, len(tt.candidates))
			for _, r := range tt.candidates {
				r.Provider = slug
				results = append(results, r)
			}
			provider := &aliasProvider{slug: slug, aliases: tt.aliases, detailErr: tt.detailErr, idErrs: tt.idErrs}
			candidates := NormalizeCandidates(results, "movie")

			winner, errs := validateMatchByProviderTitleAliases(context.Background(), tt.hints, candidates, []Provider{provider}, "en")
			gotID := ""
			if winner != nil {
				gotID = winner.ProviderIDs["tmdb"]
			}
			if gotID != tt.wantID {
				t.Fatalf("winner tmdb = %q, want %q", gotID, tt.wantID)
			}
			if (len(errs) > 0) != tt.wantErr {
				t.Fatalf("errors = %v, wantErr %v", errs, tt.wantErr)
			}
			if got := provider.detailCallCount(); got != tt.wantDetails {
				t.Fatalf("detail calls = %d, want %d", got, tt.wantDetails)
			}
			if winner != nil && winner.MatchedTitle != "首尔之春" {
				t.Fatalf("matched title = %q, want 首尔之春", winner.MatchedTitle)
			}
		})
	}
}

func TestValidateMatchByProviderTitleAliasesSkipsConflictingIDs(t *testing.T) {
	t.Parallel()
	provider := &aliasProvider{slug: "tmdb", aliases: map[string][]string{"919207": {"首尔之春"}}}
	candidates := NormalizeCandidates([]SearchResult{seoulSpringSearchResult("919207", 2023)}, "movie")
	candidates[0].ConflictingProviderIDKeys = []string{"tmdb"}

	winner, _ := validateMatchByProviderTitleAliases(context.Background(),
		&MatchHints{Title: "首尔之春", Year: 2023, Type: "movie"}, candidates, []Provider{provider}, "en")
	if winner != nil || provider.detailCallCount() != 0 {
		t.Fatalf("winner = %v, detail calls = %d; want no fetch for a candidate with conflicting IDs", winner, provider.detailCallCount())
	}
}

func TestValidateMatchByProviderTitleAliasesAsksEverySourceProvider(t *testing.T) {
	t.Parallel()
	// The first source in the chain lists no matching title; the second does.
	tvdb := &aliasProvider{slug: "tvdb"}
	tmdb := &aliasProvider{slug: "tmdb", aliases: map[string][]string{"919207": {"首尔之春"}}}
	candidates := NormalizeCandidates([]SearchResult{seoulSpringSearchResult("919207", 2023)}, "movie")
	candidates[0].Sources = []string{"tvdb", "tmdb"}

	winner, errs := validateMatchByProviderTitleAliases(context.Background(),
		&MatchHints{Title: "首尔之春", Year: 2023, Type: "movie"}, candidates, []Provider{tvdb, tmdb}, "en")
	if winner == nil || len(errs) != 0 {
		t.Fatalf("winner = %v, errs = %v; want the second source to confirm the title", winner, errs)
	}
	if tvdb.detailCallCount() != 1 || tmdb.detailCallCount() != 1 {
		t.Fatalf("detail calls tvdb=%d tmdb=%d, want 1 each", tvdb.detailCallCount(), tmdb.detailCallCount())
	}
}

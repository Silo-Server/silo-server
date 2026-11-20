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
	// partial marks IDs whose detail response has an incomplete title list.
	partial map[string]bool
	// noMetadata marks IDs whose detail response has HasMetadata false.
	noMetadata map[string]bool
	// years overrides the detail response's year per ID (default 2023).
	years map[string]int

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
		HasMetadata:          !p.noMetadata[req.ProviderIDs["tmdb"]],
		Title:                "12.12: The Day",
		OriginalTitle:        "서울의 봄",
		Year:                 2023,
		ProviderIDs:          map[string]string{"tmdb": req.ProviderIDs["tmdb"]},
		TitleAliasesComplete: !p.partial[req.ProviderIDs["tmdb"]],
	}
	if year, ok := p.years[req.ProviderIDs["tmdb"]]; ok {
		result.Year = year
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
			name:  "an exact-title result from years away is another work",
			hints: hints,
			candidates: []SearchResult{
				seoulSpringSearchResult("919207", 2023),
				{Name: "首尔之春", Year: 1990, ProviderIDs: map[string]string{"tmdb": "333"}},
			},
			aliases:     map[string][]string{"919207": {"首尔之春"}},
			wantID:      "919207",
			wantDetails: 1,
		},
		{
			name:  "an exact-title result within two years is a real rival",
			hints: hints,
			candidates: []SearchResult{
				seoulSpringSearchResult("919207", 2023),
				{Name: "首尔之春", Year: 2021, ProviderIDs: map[string]string{"tmdb": "333"}},
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

			winner, errs := validateMatchByProviderTitleAliases(context.Background(), tt.hints, candidates, []Provider{provider}, "en", nil)
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

// seoulSpringAliasTitle is the local title of 首尔之春 (2023) in the alias outcome cases.
const seoulSpringAliasTitle = "首尔之春"

type validateMatchByProviderTitleAliasesOutcomesCase struct {
	name       string
	providers  []*aliasProvider
	results    []SearchResult
	mutate     func([]MatchCandidate)
	wantWinner bool
	wantTMDB   string
	checkErrs  bool
	noFetch    bool
}

var validateMatchByProviderTitleAliasesOutcomesCases = func() []validateMatchByProviderTitleAliasesOutcomesCase {
	markRival := func(apply func(*MatchCandidate)) func([]MatchCandidate) {
		return func(candidates []MatchCandidate) {
			for i := range candidates {
				if candidates[i].ProviderIDs["tmdb"] == "111" {
					apply(&candidates[i])
				}
			}
		}
	}
	rivalPair := []SearchResult{seoulSpringSearchResult("111", 2023), seoulSpringSearchResult("222", 2023)}
	lone := []SearchResult{seoulSpringSearchResult("919207", 2023)}

	return []validateMatchByProviderTitleAliasesOutcomesCase{
		{
			name:      "skips a candidate with conflicting IDs without fetching",
			providers: []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"919207": {seoulSpringAliasTitle}}}},
			results:   lone,
			mutate:    func(c []MatchCandidate) { c[0].ConflictingProviderIDKeys = []string{"tmdb"} },
			noFetch:   true,
		},
		{
			// A title-only movie.nfo echoes the folder title; it is not a remote
			// exact-title rival and must not veto the alias check.
			name:       "ignores a title-only NFO candidate",
			providers:  []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"919207": {seoulSpringAliasTitle}}}},
			results:    []SearchResult{seoulSpringSearchResult("919207", 2023), {Name: seoulSpringAliasTitle, Year: 2023, Provider: "nfo"}},
			wantWinner: true, wantTMDB: "919207", checkErrs: true,
		},
		{
			// 222 lists the title, but rival 111 carries conflicting IDs, so its
			// details can't rule it out.
			name:      "leaves a conflicting rival unresolved",
			providers: []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"111": {seoulSpringAliasTitle}, "222": {seoulSpringAliasTitle}}}},
			results:   rivalPair,
			mutate:    markRival(func(c *MatchCandidate) { c.ConflictingProviderIDKeys = []string{"tvdb"} }),
		},
		{
			// 222 lists the title; rival 111's title list is partial, so its
			// alternative titles are unknown.
			name:      "needs complete rival titles",
			providers: []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"222": {seoulSpringAliasTitle}}, partial: map[string]bool{"111": true}}},
			results:   rivalPair,
		},
		{
			name:       "a partial list that contains the title still confirms a lone result",
			providers:  []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"919207": {seoulSpringAliasTitle}}, partial: map[string]bool{"919207": true}}},
			results:    lone,
			wantWinner: true,
		},
		{
			// Rival 111 came from two providers: tmdb's complete list lacks the
			// title, but tvdb's list is partial, so 111 isn't ruled out.
			name: "needs every source to rule out a rival",
			providers: []*aliasProvider{
				{slug: "tmdb", aliases: map[string][]string{"222": {seoulSpringAliasTitle}}},
				{slug: "tvdb", partial: map[string]bool{"111": true, "222": true}},
			},
			results: rivalPair,
			mutate:  markRival(func(c *MatchCandidate) { c.Sources = []string{"tmdb", "tvdb"} }),
		},
		{
			name:      "a response without metadata can't confirm a candidate",
			providers: []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"919207": {seoulSpringAliasTitle}}, noMetadata: map[string]bool{"919207": true}}},
			results:   lone,
		},
		{
			name:      "a response without metadata can't rule out a rival",
			providers: []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"222": {seoulSpringAliasTitle}}, noMetadata: map[string]bool{"111": true}}},
			results:   rivalPair,
		},
		{
			// The search dated 919207 to 2023, but its details say 2019.
			name:      "requires the detail year to agree",
			providers: []*aliasProvider{{slug: "tmdb", aliases: map[string][]string{"919207": {seoulSpringAliasTitle}}, years: map[string]int{"919207": 2019}}},
			results:   lone,
		},
	}
}()

func TestValidateMatchByProviderTitleAliasesSharesBudgetAcrossCalls(t *testing.T) {
	t.Parallel()
	hints := &MatchHints{Title: "首尔之春", Year: 2023, Type: "movie"}
	provider := &aliasProvider{slug: "tmdb", aliases: map[string][]string{"333": {"首尔之春"}}}
	budget := maxTitleAliasValidationCandidates

	// The first identity's two results don't list the title and spend two checks.
	first := NormalizeCandidates([]SearchResult{seoulSpringSearchResult("111", 2023), seoulSpringSearchResult("222", 2023)}, "movie")
	if winner, _ := validateMatchByProviderTitleAliases(context.Background(), hints, first, []Provider{provider}, "en", &budget); winner != nil {
		t.Fatalf("first call winner = %v, want none", winner.ProviderIDs)
	}
	// The second identity has two results but only one check left.
	second := NormalizeCandidates([]SearchResult{seoulSpringSearchResult("333", 2023), seoulSpringSearchResult("444", 2023)}, "movie")
	if winner, _ := validateMatchByProviderTitleAliases(context.Background(), hints, second, []Provider{provider}, "en", &budget); winner != nil {
		t.Fatalf("second call winner = %v, want none once the budget is short", winner.ProviderIDs)
	}
	if got := provider.detailCallCount(); got != 2 || budget != 1 {
		t.Fatalf("detail calls = %d, budget left = %d; want 2 and 1", got, budget)
	}
}

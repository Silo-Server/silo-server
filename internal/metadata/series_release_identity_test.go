package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

type seriesReleaseIdentityProvider struct {
	slug    string
	title   string
	year    int
	queries []SearchQuery
}

func (p *seriesReleaseIdentityProvider) Slug() string       { return p.slug }
func (p *seriesReleaseIdentityProvider) Name() string       { return "TMDB" }
func (p *seriesReleaseIdentityProvider) ForTypes() []string { return []string{"series"} }
func (p *seriesReleaseIdentityProvider) Search(_ context.Context, query SearchQuery) ([]SearchResult, error) {
	p.queries = append(p.queries, query)
	if query.Title != p.title || (query.Year != 0 && query.Year != p.year) {
		return nil, nil
	}
	return []SearchResult{{Name: p.title, Year: p.year, Provider: p.Slug(), ProviderIDs: map[string]string{"tmdb": "4242", "tvdb": "5353"}}}, nil
}
func (p *seriesReleaseIdentityProvider) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return &MetadataResult{HasMetadata: true, Title: p.title, Year: p.year, ProviderIDs: map[string]string{"tmdb": "4242"}}, nil
}

func TestInitialSeriesMatchUsesReleaseYearOnlyAfterFullTitleMiss(t *testing.T) {
	for _, tt := range []struct {
		name, path, title string
		year, searches    int
	}{
		{"release year", "/tv/Example.Show.2020.S01E01.1080p.WEB-DL.mkv", "Example Show", 2020, 2},
		{"numeric title", "/tv/Space.1999.S01E01.mkv", "Space 1999", 1975, 1},
		{"class year title", "/tv/Class.of.2007.S01E01.mkv", "Class of 2007", 2023, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			primary := naming.ParseFilename(tt.path, "series", "/tv")
			skeleton := &skeletonResult{Title: primary.Title, Year: primary.Year, Type: "series"}
			alternates := queuedMatchIdentityAlternates(&models.MediaFile{FilePath: tt.path}, skeleton, "/tv")
			if len(alternates) != 1 || alternates[0].Source != seriesReleaseYearHintSource {
				t.Fatalf("alternate identities = %+v", alternates)
			}
			provider := &seriesReleaseIdentityProvider{slug: "tmdb", title: tt.title, year: tt.year}
			second := &seriesReleaseIdentityProvider{slug: "tvdb", title: tt.title, year: tt.year}
			result, err := h.service.ProcessWithProviders(t.Context(), ProcessRequest{
				Hints: &MatchHints{Title: primary.Title, Year: primary.Year, Type: "series", AlternateIdentities: alternates},
				Mode:  ModeInitialMatch,
			}, []Provider{provider, second})
			if err != nil || result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeMatched {
				t.Fatalf("match result = %+v; error = %v", result, err)
			}
			if len(provider.queries) != tt.searches || provider.queries[0].Title != primary.Title {
				t.Fatalf("searches = %+v; want complete title first and %d searches", provider.queries, tt.searches)
			}
			if tt.searches == 2 && (provider.queries[1].Title != tt.title || provider.queries[1].Year != tt.year) {
				t.Fatalf("fallback search = %+v; want title=%q year=%d", provider.queries[1], tt.title, tt.year)
			}
			item, err := h.itemRepo.GetByID(t.Context(), result.ContentID)
			if err != nil || item == nil || item.Title != tt.title || item.Year != tt.year {
				t.Fatalf("matched item = %+v; error = %v", item, err)
			}
		})
	}
}

func TestInitialSeriesMatchKeepsRejectedNumericTitleCandidate(t *testing.T) {
	h := newTestHarness()
	provider := &seriesReleaseIdentityProvider{slug: "tmdb", title: "Class of 2007", year: 2023}
	result, err := h.service.ProcessWithProviders(t.Context(), ProcessRequest{
		Hints: &MatchHints{
			Title: "Class of 2007", Type: "series",
			AlternateIdentities: []MatchIdentityHint{{Title: "Class of", Year: 2007, Source: seriesReleaseYearHintSource}},
		},
		Mode: ModeInitialMatch,
	}, []Provider{provider})
	if err != nil || result == nil || result.Decision == nil || result.Decision.CandidateCount != 1 {
		t.Fatalf("match result = %+v; error = %v", result, err)
	}
	if len(provider.queries) != 1 || provider.queries[0].Title != "Class of 2007" {
		t.Fatalf("numeric title candidate was replaced by a year guess: %+v", provider.queries)
	}
}

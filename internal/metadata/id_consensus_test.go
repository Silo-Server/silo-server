//nolint:goconst // Repeated canonical IDs are the evidence under test.
package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type idConsensusPipelineProvider struct{}

func (p *idConsensusPipelineProvider) Slug() string       { return "tmdb" }
func (p *idConsensusPipelineProvider) Name() string       { return "TMDB" }
func (p *idConsensusPipelineProvider) ForTypes() []string { return []string{"series"} }

func (p *idConsensusPipelineProvider) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return []SearchResult{
		{
			Name: "A Teacher", Year: 2020, Provider: "tvdb",
			ProviderIDs: map[string]string{
				"imdb": "tt10680614", "tmdb": "103992", "tvdb": "352440",
			},
		},
		{
			Name: "A Teacher", Year: 2020, Provider: "tmdb",
			ProviderIDs: map[string]string{
				"imdb": "tt10680614", "tmdb": "103992", "tvdb": "473725",
			},
		},
	}, nil
}

func (p *idConsensusPipelineProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	if req.ProviderIDs["tvdb"] != "352440" {
		return nil, errUnexpectedQuarantinedProviderID
	}
	return &MetadataResult{
		HasMetadata: true,
		Title:       "A Teacher",
		Year:        2020,
		// A provider detail response may repeat its stale cross-reference. The
		// pipeline must not let it replace the native provider's confirmed ID.
		ProviderIDs: map[string]string{
			"imdb": "tt10680614", "tmdb": "103992", "tvdb": "473725",
		},
	}, nil
}

var errUnexpectedQuarantinedProviderID = &unexpectedQuarantinedProviderIDError{}

type unexpectedQuarantinedProviderIDError struct{}

func (*unexpectedQuarantinedProviderIDError) Error() string {
	return "quarantined provider ID reached metadata phase"
}

func TestInitialMatchPipelineResolvesConflictingConsensusIDFromOwningProvider(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	provider := &idConsensusPipelineProvider{}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{Title: "A Teacher", Type: "series"},
		Mode:  ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || !result.Updated || result.Decision == nil || result.Decision.Outcome != "matched" {
		t.Fatalf("result = %#v, want matched consensus", result)
	}
	item, err := harness.itemRepo.GetByID(context.Background(), result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.ImdbID != "tt10680614" || item.TmdbID != "103992" {
		t.Fatalf("persisted consensus IDs = imdb:%q tmdb:%q", item.ImdbID, item.TmdbID)
	}
	if item.TvdbID != "352440" {
		t.Fatalf("native TVDB ID = %q, want 352440", item.TvdbID)
	}
}

func TestRefreshPipelinesReplaceStoredCrossReferenceWithOwningProviderID(t *testing.T) {
	for name, mode := range map[string]RefreshMode{"scheduled": ModeScheduledRefresh, "manual": ModeManualRefresh} {
		mode := mode
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			harness := newTestHarness()
			const contentID = "series-refresh-consensus"
			if err := harness.itemRepo.Upsert(context.Background(), &models.MediaItem{
				ContentID: contentID, Type: "series", Title: "A Teacher", Year: 2020, Status: "matched",
				ImdbID: "tt10680614", TmdbID: "103992", TvdbID: "473725",
				Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}); err != nil {
				t.Fatalf("seed item: %v", err)
			}

			result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
				ContentID: contentID,
				Language:  "en",
				Mode:      mode,
			}, []Provider{&idConsensusPipelineProvider{}})
			if err != nil {
				t.Fatalf("ProcessWithProviders() error = %v", err)
			}
			if result == nil || !result.Updated {
				t.Fatalf("result = %#v, want updated refresh", result)
			}
			item, err := harness.itemRepo.GetByID(context.Background(), contentID)
			if err != nil {
				t.Fatalf("load refreshed item: %v", err)
			}
			if item.ImdbID != "tt10680614" || item.TmdbID != "103992" || item.TvdbID != "352440" {
				t.Fatalf("refreshed IDs = imdb:%q tmdb:%q tvdb:%q, want native TVDB ID 352440", item.ImdbID, item.TmdbID, item.TvdbID)
			}
		})
	}
}

func TestApplyCandidateProviderIDConsensusRemovesDisputedStoredID(t *testing.T) {
	ids := map[string]string{"imdb": "tt10680614", "tmdb": "103992", "tvdb": "473725"}
	winner := &MatchCandidate{
		ProviderIDs:               map[string]string{"imdb": "tt10680614", "tmdb": "103992", "tvdb": "473725"},
		ConflictingProviderIDKeys: []string{"tvdb"},
	}
	applyCandidateProviderIDConsensus(ids, winner, nil)
	if ids["tvdb"] != "" || ids["imdb"] != "tt10680614" || ids["tmdb"] != "103992" {
		t.Fatalf("consensus IDs = %#v", ids)
	}
}

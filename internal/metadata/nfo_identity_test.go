//nolint:goconst // Repeated titles and provider IDs keep NFO scenarios recognizable.
package metadata

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// localHintStubProvider mimics the built-in NFO provider's shape in the chain:
// it implements SearchProvider + MetadataProvider + IdentityHintProvider with
// canned data, exactly like a parsed sidecar NFO would supply.
type localHintStubProvider struct {
	mu            sync.Mutex
	hints         map[string]string
	searchResults []SearchResult
	metadata      *MetadataResult

	hintCalls     int
	searchCalls   int
	metadataCalls int
}

func (p *localHintStubProvider) Slug() string       { return "nfo" }
func (p *localHintStubProvider) Name() string       { return "NFO Files" }
func (p *localHintStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *localHintStubProvider) IdentityHints(_ context.Context, _ SearchQuery) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hintCalls++
	return copyMap(p.hints)
}

func (p *localHintStubProvider) Search(_ context.Context, _ SearchQuery) ([]SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.searchCalls++
	out := make([]SearchResult, len(p.searchResults))
	copy(out, p.searchResults)
	return out, nil
}

func (p *localHintStubProvider) GetMetadata(_ context.Context, _ MetadataRequest) (*MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metadataCalls++
	if p.metadata != nil {
		cp := *p.metadata
		cp.ProviderIDs = copyMap(p.metadata.ProviderIDs)
		return &cp, nil
	}
	return &MetadataResult{}, nil
}

// remoteStubProvider is a canned remote search+metadata provider that records
// the provider IDs it was asked to fetch metadata for.
type remoteStubProvider struct {
	mu            sync.Mutex
	slug          string
	searchResults []SearchResult
	metadata      *MetadataResult
	lastSearchIDs map[string]string
	lastMetaIDs   map[string]string
	metadataCalls int
}

func (p *remoteStubProvider) Slug() string       { return p.slug }
func (p *remoteStubProvider) Name() string       { return p.slug }
func (p *remoteStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *remoteStubProvider) Search(_ context.Context, query SearchQuery) ([]SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSearchIDs = copyMap(query.ProviderIDs)
	out := make([]SearchResult, len(p.searchResults))
	copy(out, p.searchResults)
	return out, nil
}

func (p *remoteStubProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metadataCalls++
	p.lastMetaIDs = copyMap(req.ProviderIDs)
	if p.metadata != nil {
		cp := *p.metadata
		cp.ProviderIDs = copyMap(p.metadata.ProviderIDs)
		return &cp, nil
	}
	return &MetadataResult{}, nil
}

func (p *remoteStubProvider) lastMetadataIDs() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyMap(p.lastMetaIDs)
}

func (p *remoteStubProvider) lastSearchProviderIDs() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyMap(p.lastSearchIDs)
}

// TestInitialMatch_MalformedIdentityDoesNotSuppressTitleSearch covers malformed
// trusted IDs, from an NFO or from the scanner: the match falls back to a title
// search instead of sending them to the remote provider.
func TestInitialMatch_MalformedIdentityDoesNotSuppressTitleSearch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		hints MatchHints
		nfo   *localHintStubProvider
	}{
		{
			name:  "NFO identity",
			hints: MatchHints{Title: "The Matrix", Year: 1999, Type: "movie"},
			nfo: &localHintStubProvider{
				hints:         map[string]string{"tmdb": "not-a-number", "imdb": "nm1234567"},
				searchResults: []SearchResult{{Name: "The Matrix", Year: 1999, Provider: "nfo", ProviderIDs: map[string]string{"tmdb": "not-a-number"}}},
				metadata:      &MetadataResult{HasMetadata: true, Title: "The Matrix"},
			},
		},
		{
			name:  "scanner identity",
			hints: MatchHints{Title: "The Matrix", Year: 1999, Type: "movie", TmdbID: "not-a-number", ImdbID: "nm1234567"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHarness()
			remote := &remoteStubProvider{
				slug: "tmdb",
				searchResults: []SearchResult{{
					Name: "The Matrix", Year: 1999, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "603", "imdb": "tt0133093"},
				}},
				metadata: &MetadataResult{HasMetadata: true, Title: "The Matrix", Year: 1999, ProviderIDs: map[string]string{"tmdb": "603", "imdb": "tt0133093"}},
			}
			providers := []Provider{remote}
			if tc.nfo != nil {
				providers = []Provider{tc.nfo, remote}
			}

			hints := tc.hints
			result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
				Hints: &hints,
				Mode:  ModeInitialMatch,
			}, providers)
			if err != nil {
				t.Fatalf("ProcessWithProviders() error = %v", err)
			}
			if result == nil || !result.Updated {
				t.Fatalf("result = %#v, want title-based remote match", result)
			}
			if ids := remote.lastSearchProviderIDs(); ids["tmdb"] != "" || ids["imdb"] != "" {
				t.Fatalf("remote search received malformed trusted IDs: %#v", ids)
			}
		})
	}
}

func seedMovieItem(t *testing.T, h *testHarness, contentID, title string, year int) {
	t.Helper()
	if err := h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID: contentID,
		Type:      "movie",
		Title:     title,
		Year:      year,
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
}

// A title-only NFO with no remote search results must still produce an
// accepted match: the item stays on its path-deterministic local: content id,
// is persisted as matched, and gains no durable provider IDs (#216).
func TestInitialMatch_TitleOnlyNFO_NoRemoteResults_MatchesUnderLocalID(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "local-abc123",
		Type:      "movie",
		Title:     "My Home Movie",
		Status:    "pending_match",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	nfo := &localHintStubProvider{
		searchResults: []SearchResult{{Name: "My Home Movie", Year: 2021, Provider: "nfo"}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "My Home Movie", Year: 2021, Overview: "Curated."},
	}
	remote := &remoteStubProvider{slug: "tmdb"}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "local-abc123",
		Hints: &MatchHints{
			Title: "My Home Movie",
			Year:  2021,
			Type:  "movie",
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}
	item, err := h.itemRepo.GetByID(ctx, "local-abc123")
	if err != nil {
		t.Fatalf("item not found under local id after title-only NFO match: %v", err)
	}
	if item.Status != "matched" {
		t.Errorf("item status = %q, want matched", item.Status)
	}
	if item.Title != "My Home Movie" {
		t.Errorf("item title = %q, want My Home Movie", item.Title)
	}
	if item.TmdbID != "" || item.TvdbID != "" || item.ImdbID != "" {
		t.Errorf("title-only NFO match must carry no provider ids, got tmdb=%q tvdb=%q imdb=%q",
			item.TmdbID, item.TvdbID, item.ImdbID)
	}
}

// The same title-only flow at the worker level: the queued movie file must
// leave the match queue (row deleted) with the item matched under local:.
func TestWorker_TitleOnlyNFO_MovieLeavesMatchQueueMatched(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

	nfo := &localHintStubProvider{
		searchResults: []SearchResult{{Name: "My Home Movie", Year: 2021, Provider: "nfo"}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "My Home Movie", Year: 2021},
	}
	remote := &remoteStubProvider{slug: "tmdb"}
	h.service.hooks.process = func(ctx context.Context, req ProcessRequest) (*ProcessResult, error) {
		return h.service.ProcessWithProviders(ctx, req, []Provider{nfo, remote})
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/My Home Movie (2021)/My Home Movie (2021).mkv",
	}
	movieRepo := newFakeMovieQueueRepo(file)

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.movieClaimer = movieRepo
	worker.processUnmatched(ctx)

	if _, deleted := movieRepo.deleted[file.ID]; !deleted {
		t.Fatalf("movie queue row not deleted; errors=%v", movieRepo.errors)
	}
	contentID := h.fileRepo.contentIDs[file.ID]
	if contentID == "" {
		t.Fatal("file was not linked to a content id")
	}
	if !strings.HasPrefix(contentID, "local-") {
		t.Errorf("content id = %q, want local- prefix", contentID)
	}
	item, err := h.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.Status != "matched" {
		t.Errorf("item status = %q, want matched", item.Status)
	}
}

// A curated <uniqueid> must anchor identity even when remote search-by-title
// returns nothing: the NFO's tmdb id is seeded as a trusted hint and the
// remote provider's GetMetadata runs with it (full enrichment by ID).
func TestInitialMatch_NFOUniqueIDAnchorsWhenRemoteSearchEmpty(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	nfo := &localHintStubProvider{
		hints:         map[string]string{"tmdb": "424242"},
		searchResults: []SearchResult{{Name: "Obscure Film", Year: 2019, ProviderIDs: map[string]string{"tmdb": "424242"}, Provider: "nfo"}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "Obscure Film", Year: 2019},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Obscure Film: Remote Cut", Year: 2019, Overview: "From TMDB.", ProviderIDs: map[string]string{"tmdb": "424242", "imdb": "tt0424242"}},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		Hints: &MatchHints{
			Title: "Obscure Film",
			Year:  2019,
			Type:  "movie",
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{remote, nfo})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}
	if got := remote.lastMetadataIDs()["tmdb"]; got != "424242" {
		t.Errorf("remote GetMetadata tmdb id = %q, want 424242 (NFO hint must anchor identity)", got)
	}
	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.TmdbID != "424242" {
		t.Errorf("item tmdb id = %q, want 424242", item.TmdbID)
	}
	if item.Overview != "From TMDB." {
		t.Errorf("item overview = %q, want remote enrichment applied", item.Overview)
	}
}

// When a title-only NFO coexists with a successful remote search, the remote
// match must win: no downgrade of a found movie to an unenriched local: item,
// and the winning candidate keeps the remote provider IDs.
func TestInitialMatch_TitleOnlyNFO_RemoteSearchWins(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	nfo := &localHintStubProvider{
		searchResults: []SearchResult{{Name: "Inception", Year: 2010, Provider: "nfo"}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "Inception", Year: 2010},
	}
	remote := &remoteStubProvider{
		slug:          "tmdb",
		searchResults: []SearchResult{{Name: "Inception", Year: 2010, ProviderIDs: map[string]string{"tmdb": "27205"}, Provider: "tmdb"}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "Inception", Year: 2010, ProviderIDs: map[string]string{"tmdb": "27205"}},
	}

	// NFO deliberately first in the chain: chain priority must not let the
	// ID-less local candidate beat the ID-bearing remote candidate.
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		Hints: &MatchHints{
			Title: "Inception",
			Year:  2010,
			Type:  "movie",
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}
	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.TmdbID != "27205" {
		t.Errorf("item tmdb id = %q, want 27205 (remote match must not be downgraded to local:)", item.TmdbID)
	}
}

// TestRefresh_StoredIDVersusNFOHint covers which TMDB ID a refresh fetches when
// the NFO names another one: a scheduled refresh keeps the stored ID, a manual
// refresh follows the NFO unless the NFO's ID is recorded dead.
func TestRefresh_StoredIDVersusNFOHint(t *testing.T) {
	for _, tc := range []struct {
		name, title, hintID, nfoTitle string
		remoteID, staleID, wantID     string
		mode                          RefreshMode
	}{
		{name: "scheduled refresh: stored ID beats the NFO hint", title: "Stable Movie", hintID: "999", nfoTitle: "Stable Movie", remoteID: "100", mode: ModeScheduledRefresh, wantID: "100"},
		{name: "manual refresh: NFO hint beats the stored ID", title: "Misidentified Movie", hintID: "200", nfoTitle: "Corrected Movie", remoteID: "200", mode: ModeManualRefresh, wantID: "200"},
		{name: "manual refresh: a dead NFO hint keeps the stored ID", title: "Stored Movie", hintID: "200", nfoTitle: "Stored Movie", remoteID: "100", staleID: "200", mode: ModeManualRefresh, wantID: "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			ctx := context.Background()
			seedMovieItem(t, h, "movie:tmdb:100", tc.title, 2018)
			h.itemRepo.items["movie:tmdb:100"].TmdbID = "100"
			if tc.staleID != "" {
				staleRepo := newFakeStaleIDRepo()
				staleRepo.set("movie:tmdb:100", &models.StaleMediaID{ContentID: "movie:tmdb:100", Provider: "tmdb", ProviderID: tc.staleID})
				h.service.staleIDRepo = staleRepo
			}

			nfo := &localHintStubProvider{
				hints:    map[string]string{"tmdb": tc.hintID},
				metadata: &MetadataResult{HasMetadata: true, Title: tc.nfoTitle},
			}
			remote := &remoteStubProvider{
				slug:     "tmdb",
				metadata: &MetadataResult{HasMetadata: true, Title: tc.nfoTitle, ProviderIDs: map[string]string{"tmdb": tc.remoteID}},
			}

			if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
				ContentID: "movie:tmdb:100",
				Language:  "en",
				Mode:      tc.mode,
			}, []Provider{nfo, remote}); err != nil {
				t.Fatalf("ProcessWithProviders: %v", err)
			}
			if got := remote.lastMetadataIDs()["tmdb"]; got != tc.wantID {
				t.Errorf("refresh tmdb id = %q, want %s", got, tc.wantID)
			}
		})
	}
}

// Manual Identify: the user's explicit identification wins and the NFO
// provider is skipped entirely — no hints, no metadata overlay.
func TestIdentify_SkipsNFOProviderEntirely(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:100", "Old Title", 2018)

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "999"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Stale NFO Title"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "User Chosen Title", ProviderIDs: map[string]string{"tmdb": "555"}},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:   "movie:tmdb:100",
		ProviderIDs: map[string]string{"tmdb": "555"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}
	if nfo.hintCalls != 0 || nfo.searchCalls != 0 || nfo.metadataCalls != 0 {
		t.Errorf("NFO provider consulted during identify: hints=%d search=%d metadata=%d, want all 0",
			nfo.hintCalls, nfo.searchCalls, nfo.metadataCalls)
	}
	if got := remote.lastMetadataIDs()["tmdb"]; got != "555" {
		t.Errorf("identify tmdb id = %q, want 555 (user identification wins)", got)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:100")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "User Chosen Title" {
		t.Errorf("item title = %q, want User Chosen Title (stale NFO title must not overlay identify)", item.Title)
	}
}

// Phase-2: NFO GetMetadata results merge fields but never inject provider-id
// keys that conflict with or extend a remotely-anchored identity.
func TestPhase2_NFOMetadataDoesNotInjectProviderIDs(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	nfo := &localHintStubProvider{
		metadata: &MetadataResult{
			HasMetadata: true,
			Title:       "Anchored Movie",
			ProviderIDs: map[string]string{"tmdb": "999", "imdb": "tt0000001"},
		},
	}
	remote := &remoteStubProvider{
		slug:          "tmdb",
		searchResults: []SearchResult{{Name: "Anchored Movie", Year: 2015, ProviderIDs: map[string]string{"tmdb": "42"}, Provider: "tmdb"}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "Anchored Movie", Year: 2015, ProviderIDs: map[string]string{"tmdb": "42"}},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		Hints: &MatchHints{
			Title: "Anchored Movie",
			Year:  2015,
			Type:  "movie",
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{remote, nfo})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.TmdbID != "42" {
		t.Errorf("item tmdb id = %q, want 42 (NFO metadata ids must not overwrite the anchor)", item.TmdbID)
	}
	if item.ImdbID != "" {
		t.Errorf("item imdb id = %q, want empty (NFO must not extend a remotely-anchored identity)", item.ImdbID)
	}
}

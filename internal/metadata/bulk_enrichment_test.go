package metadata

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeEnrichmentState is an in-memory enrichmentStateStore that applies the
// same selection rule as the SQL: an item is a candidate until it has a found
// outcome, or while its empty or failed outcome is not yet due again.
type fakeEnrichmentState struct {
	mu         sync.Mutex
	candidates []enrichmentCandidate
	outcomes   map[string]enrichmentOutcome // keyed by provider + "|" + content ID
	found      map[string][]string          // RecordFound calls by content ID
	now        func() time.Time
}

func newFakeEnrichmentState(candidates ...enrichmentCandidate) *fakeEnrichmentState {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ContentID < candidates[j].ContentID })
	return &fakeEnrichmentState{
		candidates: candidates,
		outcomes:   make(map[string]enrichmentOutcome),
		found:      make(map[string][]string),
		now:        time.Now,
	}
}

func (f *fakeEnrichmentState) settled(provider, contentID string) bool {
	outcome, ok := f.outcomes[provider+"|"+contentID]
	return ok && (outcome.NextCheckAt == nil || outcome.NextCheckAt.After(f.now()))
}

func (f *fakeEnrichmentState) Candidates(_ context.Context, query enrichmentCandidateQuery) ([]enrichmentCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var page []enrichmentCandidate
	for _, candidate := range f.candidates {
		if candidate.ContentID <= query.After || f.settled(query.Provider, candidate.ContentID) {
			continue
		}
		page = append(page, candidate)
		if len(page) == query.Limit {
			break
		}
	}
	return page, nil
}

func (f *fakeEnrichmentState) CountCandidates(_ context.Context, query enrichmentCandidateQuery) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, candidate := range f.candidates {
		if !f.settled(query.Provider, candidate.ContentID) {
			count++
		}
	}
	return count, nil
}

func (f *fakeEnrichmentState) RecordOutcomes(_ context.Context, provider string, outcomes []enrichmentOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, outcome := range outcomes {
		f.outcomes[provider+"|"+outcome.ContentID] = outcome
	}
	return nil
}

func (f *fakeEnrichmentState) RecordFound(_ context.Context, contentID string, providers []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.found[contentID] = append(f.found[contentID], providers...)
	return nil
}

func (f *fakeEnrichmentState) outcome(provider, contentID string) (enrichmentOutcome, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	outcome, ok := f.outcomes[provider+"|"+contentID]
	return outcome, ok
}

// bulkStubProvider answers GetMetadata from a per-item function and tracks how
// many lookups are in flight. With gate set, lookups are released in groups of
// gate: each waits until its whole group has arrived (or the call is
// canceled), the way a batching provider holds lookups to answer them
// together.
type bulkStubProvider struct {
	slug   string
	answer func(ctx context.Context, contentID string) (*MetadataResult, error)
	gate   int

	mu          sync.Mutex
	cond        *sync.Cond
	arrived     int
	inFlight    int
	maxInFlight int
	calls       []string
}

func newBulkStubProvider(slug string, gate int, answer func(ctx context.Context, contentID string) (*MetadataResult, error)) *bulkStubProvider {
	p := &bulkStubProvider{slug: slug, gate: gate, answer: answer}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bulkStubProvider) Slug() string       { return p.slug }
func (p *bulkStubProvider) Name() string       { return p.slug }
func (p *bulkStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *bulkStubProvider) GetMetadata(ctx context.Context, req MetadataRequest) (*MetadataResult, error) {
	contentID := req.ProviderIDs["silo"]
	p.mu.Lock()
	p.inFlight++
	p.maxInFlight = max(p.maxInFlight, p.inFlight)
	p.calls = append(p.calls, contentID)
	ordinal := p.arrived
	p.arrived++
	p.cond.Broadcast()
	stop := context.AfterFunc(ctx, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	if p.gate > 0 {
		groupEnd := (ordinal/p.gate + 1) * p.gate
		for p.arrived < groupEnd && ctx.Err() == nil {
			p.cond.Wait()
		}
	}
	p.mu.Unlock()
	stop()

	result, err := p.answer(ctx, contentID)

	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return result, err
}

func (p *bulkStubProvider) snapshot() (maxInFlight int, calls []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInFlight, append([]string(nil), p.calls...)
}

func bulkCandidates(n int) []enrichmentCandidate {
	candidates := make([]enrichmentCandidate, 0, n)
	for i := range n {
		id := fmt.Sprintf("movie:tmdb:%03d", i)
		candidates = append(candidates, enrichmentCandidate{
			ContentID:   id,
			Type:        "movie",
			ProviderIDs: map[string]string{"tmdb": fmt.Sprint(i), "silo": id},
		})
	}
	return candidates
}

func seedBulkItems(t *testing.T, h *testHarness, candidates []enrichmentCandidate) {
	t.Helper()
	for _, candidate := range candidates {
		seedMovieItem(t, h, candidate.ContentID, "Title "+candidate.ContentID, 2000)
	}
}

func foundResult(rating string) *MetadataResult {
	return &MetadataResult{HasMetadata: true, ContentRating: rating}
}

func TestBulkEnrichmentKeepsExactlyTheLimitInFlight(t *testing.T) {
	const limit = 5
	candidates := bulkCandidates(3 * limit)
	h := newTestHarness()
	seedBulkItems(t, h, candidates)
	store := newFakeEnrichmentState(candidates...)
	h.service.enrichmentState = store
	provider := newBulkStubProvider("mdblist", limit, func(context.Context, string) (*MetadataResult, error) {
		return foundResult("PG"), nil
	})

	report, err := h.service.runBulkEnrichment(context.Background(), []bulkEnrichmentTarget{{provider: provider, limit: limit}}, nil)
	if err != nil {
		t.Fatalf("runBulkEnrichment() error = %v", err)
	}
	maxInFlight, calls := provider.snapshot()
	if maxInFlight != limit {
		t.Fatalf("max lookups in flight = %d, want exactly %d", maxInFlight, limit)
	}
	if len(calls) != len(candidates) {
		t.Fatalf("lookups = %d, want %d", len(calls), len(candidates))
	}
	if got := report.Providers[0]; got.Found != len(candidates) || got.Pending != len(candidates) || got.Stopped != "" {
		t.Fatalf("report = %+v", got)
	}
	for _, candidate := range candidates {
		if outcome, ok := store.outcome("mdblist", candidate.ContentID); !ok || outcome.Outcome != enrichmentOutcomeFound || outcome.NextCheckAt != nil {
			t.Fatalf("outcome for %s = %+v (recorded %v), want found", candidate.ContentID, outcome, ok)
		}
		if item := h.itemRepo.items[candidate.ContentID]; item.ContentRating != "PG" {
			t.Fatalf("%s content rating = %q, want PG", candidate.ContentID, item.ContentRating)
		}
	}
}

func TestBulkEnrichmentStopsOnSpentQuotaAndResumes(t *testing.T) {
	const limit = 4
	candidates := bulkCandidates(3 * limit)
	h := newTestHarness()
	seedBulkItems(t, h, candidates)
	store := newFakeEnrichmentState(candidates...)
	h.service.enrichmentState = store

	// The quota runs out during the second page.
	var mu sync.Mutex
	answered := 0
	quotaLeft := limit + 1
	provider := newBulkStubProvider("mdblist", 0, func(context.Context, string) (*MetadataResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if answered >= quotaLeft {
			return nil, status.Error(codes.ResourceExhausted, "daily quota used up")
		}
		answered++
		return foundResult("R"), nil
	})
	target := bulkEnrichmentTarget{provider: provider, limit: limit}

	report, err := h.service.runBulkEnrichment(context.Background(), []bulkEnrichmentTarget{target}, nil)
	if err != nil {
		t.Fatalf("first runBulkEnrichment() error = %v", err)
	}
	first := report.Providers[0]
	if first.Stopped != "provider quota spent" || first.Found != quotaLeft || first.Empty != 0 || first.Failed != 0 {
		t.Fatalf("first report = %+v, want %d found and a quota stop", first, quotaLeft)
	}
	_, firstCalls := provider.snapshot()
	if len(firstCalls) != 2*limit {
		t.Fatalf("first pass made %d lookups, want %d (two pages, then stop)", len(firstCalls), 2*limit)
	}

	// The quota resets; the next pass asks only for what is still missing.
	mu.Lock()
	quotaLeft = len(candidates)
	mu.Unlock()
	report, err = h.service.runBulkEnrichment(context.Background(), []bulkEnrichmentTarget{target}, nil)
	if err != nil {
		t.Fatalf("second runBulkEnrichment() error = %v", err)
	}
	second := report.Providers[0]
	if second.Pending != len(candidates)-first.Found || second.Found != second.Pending || second.Stopped != "" {
		t.Fatalf("second report = %+v, want the %d remaining items found", second, len(candidates)-first.Found)
	}
	_, allCalls := provider.snapshot()
	seen := make(map[string]int)
	for _, id := range allCalls[len(firstCalls):] {
		seen[id]++
		if outcome, ok := store.outcome("mdblist", id); !ok || outcome.Outcome != enrichmentOutcomeFound {
			t.Fatalf("outcome for %s = %+v, want found", id, outcome)
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s looked up %d times in the second pass", id, count)
		}
	}
	if len(seen) != second.Pending {
		t.Fatalf("second pass looked up %d items, want %d", len(seen), second.Pending)
	}
}

func TestBulkEnrichmentRecordsEmptyAndFailedItems(t *testing.T) {
	candidates := bulkCandidates(3)
	h := newTestHarness()
	seedBulkItems(t, h, candidates)
	store := newFakeEnrichmentState(candidates...)
	h.service.enrichmentState = store
	provider := newBulkStubProvider("mdblist", 0, func(_ context.Context, contentID string) (*MetadataResult, error) {
		switch contentID {
		case candidates[0].ContentID:
			return foundResult("PG-13"), nil
		case candidates[1].ContentID:
			return nil, nil
		default:
			return nil, status.Error(codes.InvalidArgument, "bad id")
		}
	})

	before := time.Now()
	report, err := h.service.runBulkEnrichment(context.Background(), []bulkEnrichmentTarget{{provider: provider, limit: 10}}, nil)
	if err != nil {
		t.Fatalf("runBulkEnrichment() error = %v", err)
	}
	if got := report.Providers[0]; got.Found != 1 || got.Empty != 1 || got.Failed != 1 || got.Stopped != "" {
		t.Fatalf("report = %+v, want one each of found, empty and failed", got)
	}
	empty, _ := store.outcome("mdblist", candidates[1].ContentID)
	if empty.Outcome != enrichmentOutcomeEmpty || empty.NextCheckAt == nil || empty.NextCheckAt.Before(before.Add(bulkEnrichmentEmptyRecheck)) {
		t.Fatalf("empty outcome = %+v, want a recheck %v out", empty, bulkEnrichmentEmptyRecheck)
	}
	failed, _ := store.outcome("mdblist", candidates[2].ContentID)
	if failed.Outcome != enrichmentOutcomeFailed || failed.NextCheckAt == nil || failed.NextCheckAt.After(before.Add(bulkEnrichmentEmptyRecheck)) {
		t.Fatalf("failed outcome = %+v, want a recheck %v out", failed, bulkEnrichmentFailedRecheck)
	}
}

func TestBulkEnrichmentStopsCleanlyOnShutdown(t *testing.T) {
	const limit = 3
	candidates := bulkCandidates(2 * limit)
	h := newTestHarness()
	seedBulkItems(t, h, candidates)
	store := newFakeEnrichmentState(candidates...)
	h.service.enrichmentState = store

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Each lookup blocks until the pass is canceled, as a slow upstream
	// would; the server shuts down once a full page is in flight.
	provider := newBulkStubProvider("mdblist", limit, func(ctx context.Context, _ string) (*MetadataResult, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	})

	done := make(chan struct{})
	var report BulkEnrichmentReport
	var err error
	go func() {
		report, err = h.service.runBulkEnrichment(ctx, []bulkEnrichmentTarget{{provider: provider, limit: limit}}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pass did not stop after cancellation")
	}
	if err != nil {
		t.Fatalf("runBulkEnrichment() error = %v, want nil on shutdown", err)
	}
	if got := report.Providers[0]; got.Stopped != "shutting down" || got.Found+got.Empty+got.Failed != 0 {
		t.Fatalf("report = %+v, want a shutdown stop with nothing recorded", got)
	}
	if _, calls := provider.snapshot(); len(calls) != limit {
		t.Fatalf("lookups = %d, want one page of %d", len(calls), limit)
	}
	if count, _ := store.CountCandidates(context.Background(), enrichmentCandidateQuery{Provider: "mdblist"}); count != len(candidates) {
		t.Fatalf("pending after shutdown = %d, want all %d still pending", count, len(candidates))
	}
}

func TestBulkEnrichmentStopsPassClassification(t *testing.T) {
	stops := []error{
		status.Error(codes.ResourceExhausted, "quota"),
		status.Error(codes.Unavailable, "outage"),
		status.Error(codes.FailedPrecondition, "no api key"),
		status.Error(codes.Unauthenticated, "bad key"),
		status.Error(codes.Unknown, "plugin returned a plain error"),
		context.Canceled,
		fmt.Errorf("lookup: %w", context.DeadlineExceeded),
		errors.New("plugin installation 3 is not running"),
	}
	for _, err := range stops {
		if !bulkEnrichmentStopsPass(err) {
			t.Errorf("bulkEnrichmentStopsPass(%v) = false, want true", err)
		}
	}
	itemOnly := []error{
		status.Error(codes.InvalidArgument, "bad id"),
		status.Error(codes.NotFound, "no such title"),
		status.Error(codes.Internal, "decode"),
	}
	for _, err := range itemOnly {
		if bulkEnrichmentStopsPass(err) {
			t.Errorf("bulkEnrichmentStopsPass(%v) = true, want false", err)
		}
	}
}

type recordingVideoRepo struct {
	mu    sync.Mutex
	calls int
}

func (r *recordingVideoRepo) ReplaceByContentID(context.Context, string, []models.ItemVideo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return nil
}

func TestPersistEnrichmentIsNotARefresh(t *testing.T) {
	const contentID = "movie:tmdb:42"
	h := newTestHarness()
	videos := &recordingVideoRepo{}
	h.service.videoRepo = videos
	seedMovieItem(t, h, contentID, "Title", 1999)
	lastRefreshed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	matchedAt := lastRefreshed.Add(-time.Hour)
	imdb := 7.1
	item := h.itemRepo.items[contentID]
	item.TmdbID = "42"
	item.LastRefreshed = &lastRefreshed
	item.MatchedAt = &matchedAt
	item.RefreshFailures = 2
	episodesCheckedAt := lastRefreshed.Add(time.Hour)
	item.EpisodeMetadataIncomplete = true
	item.EpisodeMetadataLastCheckedAt = &episodesCheckedAt
	item.MetadataS3Path = "metadata/movie-tmdb-42.json"
	item.MetadataEtag = "etag-1"
	item.RatingIMDB = &imdb
	item.Studios = []string{"Stored Studio"}
	item.LockedFields = []int{int(FieldContentRating)}
	// Cached artwork, and a local backdrop still waiting to be cached (no
	// path yet, only its source), which a write without images must keep.
	item.PosterPath = "cache/items/movie-tmdb-42/poster.webp"
	item.PosterSourcePath = "tmdb://poster/abc.jpg"
	item.PosterThumbhash = "posterhash"
	item.BackdropSourcePath = "file:///media/movies/Example/fanart.jpg"
	item.BackdropThumbhash = "backdrophash"
	item.LogoPath = "tmdb://logo/logo.png"
	item.LogoSourcePath = "tmdb://logo/logo.png"
	wantArtwork := []string{item.PosterPath, item.PosterSourcePath, item.PosterThumbhash,
		item.BackdropPath, item.BackdropSourcePath, item.BackdropThumbhash, item.LogoPath, item.LogoSourcePath}

	result := &MetadataResult{
		HasMetadata:   true,
		ContentRating: "R",
		Runtime:       120,
		Ratings:       Ratings{IMDB: 9.9, RTCritic: 91},
		Studios:       []string{"Provider Studio"},
		Videos:        []RemoteVideo{{Provider: "mdblist", ProviderKey: "v", Site: "youtube", SiteKey: "yt"}},
	}
	candidate := enrichmentCandidate{ContentID: contentID, Type: "movie", ProviderIDs: map[string]string{"tmdb": "42"}}
	if err := h.service.persistEnrichment(context.Background(), candidate, "mdblist", result); err != nil {
		t.Fatalf("persistEnrichment() error = %v", err)
	}

	got := h.itemRepo.items[contentID]
	switch {
	case got.Runtime != 120:
		t.Errorf("runtime = %d, want 120 filled", got.Runtime)
	case got.RatingRTCritic == nil || *got.RatingRTCritic != 91:
		t.Errorf("rt critic = %v, want 91 filled", got.RatingRTCritic)
	case got.RatingIMDB == nil || *got.RatingIMDB != 7.1:
		t.Errorf("imdb = %v, want the stored 7.1 kept (fill-empty)", got.RatingIMDB)
	case got.ContentRating != "":
		t.Errorf("content rating = %q, want the lock to keep it empty", got.ContentRating)
	case !slices.Equal(got.Studios, []string{"Stored Studio", "Provider Studio"}):
		t.Errorf("studios = %v, want the provider's added to the stored", got.Studios)
	case got.LastRefreshed == nil || !got.LastRefreshed.Equal(lastRefreshed):
		t.Errorf("last refreshed = %v, want %v kept", got.LastRefreshed, lastRefreshed)
	case got.MatchedAt == nil || !got.MatchedAt.Equal(matchedAt):
		t.Errorf("matched at = %v, want %v kept", got.MatchedAt, matchedAt)
	case got.RefreshFailures != 2:
		t.Errorf("refresh failures = %d, want 2 kept", got.RefreshFailures)
	case got.Status != "matched":
		t.Errorf("status = %q, want matched", got.Status)
	case !got.EpisodeMetadataIncomplete || got.EpisodeMetadataLastCheckedAt == nil || !got.EpisodeMetadataLastCheckedAt.Equal(episodesCheckedAt):
		t.Errorf("episode state = (%v, %v), want (true, %v) kept", got.EpisodeMetadataIncomplete, got.EpisodeMetadataLastCheckedAt, episodesCheckedAt)
	case got.MetadataS3Path != "metadata/movie-tmdb-42.json" || got.MetadataEtag != "etag-1":
		t.Errorf("metadata object = (%q, %q), want the stored one kept", got.MetadataS3Path, got.MetadataEtag)
	}
	if videos.calls != 0 {
		t.Errorf("videos replaced %d times, want never from an enrichment write", videos.calls)
	}
	gotArtwork := []string{got.PosterPath, got.PosterSourcePath, got.PosterThumbhash,
		got.BackdropPath, got.BackdropSourcePath, got.BackdropThumbhash, got.LogoPath, got.LogoSourcePath}
	if !slices.Equal(gotArtwork, wantArtwork) {
		t.Errorf("artwork = %q, want the stored %q untouched", gotArtwork, wantArtwork)
	}
}

func TestPersistEnrichmentDoesNotRecreateADeletedItem(t *testing.T) {
	h := newTestHarness()
	candidate := enrichmentCandidate{ContentID: "movie:tmdb:gone", Type: "movie", ProviderIDs: map[string]string{"tmdb": "1"}}
	if err := h.service.persistEnrichment(context.Background(), candidate, "mdblist", foundResult("PG")); err == nil {
		t.Fatal("persistEnrichment() error = nil, want an error for a missing item")
	}
	if _, ok := h.itemRepo.items[candidate.ContentID]; ok {
		t.Fatal("persistEnrichment() created the missing item")
	}
}

func TestPersistEnrichmentKeepsTheStoredProviderIDs(t *testing.T) {
	const contentID = "movie:tmdb:42"
	h := newTestHarness()
	seedMovieItem(t, h, contentID, "Title", 1999)
	h.itemRepo.items[contentID].TmdbID = "42"
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(contentID, &models.MediaItemProviderID{
		ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "42",
	})
	h.service.providerIDRepo = providerRepo

	// IDs the item lacks, which could belong to other items.
	result := &MetadataResult{
		HasMetadata:   true,
		ContentRating: "PG",
		ProviderIDs:   map[string]string{"imdb": "tt0000999", "tvdb": "999", "mdblist": "m-1"},
	}
	candidate := enrichmentCandidate{ContentID: contentID, Type: "movie", ProviderIDs: map[string]string{"tmdb": "42"}}
	if err := h.service.persistEnrichment(context.Background(), candidate, "mdblist", result); err != nil {
		t.Fatalf("persistEnrichment() error = %v", err)
	}

	if got := providerRepo.lastReplace[contentID]; !maps.Equal(got, map[string]string{"tmdb": "42"}) {
		t.Errorf("persisted provider IDs = %v, want only the stored tmdb", got)
	}
	got := h.itemRepo.items[contentID]
	if got.ImdbID != "" || got.TvdbID != "" {
		t.Errorf("item IDs = (imdb %q, tvdb %q), want none added", got.ImdbID, got.TvdbID)
	}
	if got.ContentRating != "PG" {
		t.Errorf("content rating = %q, want PG filled", got.ContentRating)
	}
}

func newLookupPluginProvider(t *testing.T, client *fakePluginMetadataClient) *PluginProvider {
	t.Helper()
	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         "mdblist",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}
	provider.lookupProviderIDs = []string{"imdb", "tmdb"}
	return provider
}

func TestRefreshRecordsEnrichmentProvidersThatAnswered(t *testing.T) {
	const contentID = "movie:tmdb:100"
	cases := []struct {
		name      string
		client    *fakePluginMetadataClient
		wantFound bool
	}{
		{
			name:      "answered",
			client:    &fakePluginMetadataClient{response: &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{ItemType: "movie", ContentRating: "PG"}}},
			wantFound: true,
		},
		{name: "empty", client: &fakePluginMetadataClient{response: &pluginv1.GetMetadataResponse{}}},
		{name: "quota spent", client: &fakePluginMetadataClient{getMetadataErr: status.Error(codes.ResourceExhausted, "quota")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			store := newFakeEnrichmentState()
			h.service.enrichmentState = store
			seedMovieItem(t, h, contentID, "Title", 2018)
			h.itemRepo.items[contentID].TmdbID = "100"
			tmdb := &remoteStubProvider{
				slug:     "tmdb",
				metadata: &MetadataResult{HasMetadata: true, Title: "Title", ProviderIDs: map[string]string{"tmdb": "100"}},
			}

			if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
				ContentID: contentID, Language: "en", Mode: ModeScheduledRefresh,
			}, []Provider{tmdb, newLookupPluginProvider(t, tc.client)}); err != nil {
				t.Fatalf("ProcessWithProviders() error = %v, want the refresh to succeed", err)
			}
			got := store.found[contentID]
			if tc.wantFound && !slices.Equal(got, []string{"mdblist"}) {
				t.Fatalf("recorded found = %v, want [mdblist]", got)
			}
			if !tc.wantFound && len(got) != 0 {
				t.Fatalf("recorded found = %v, want nothing", got)
			}
		})
	}
}

package audiobooks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCacheRemotePosterSkipsAlreadyCachedPath(t *testing.T) {
	cacher := &fakeAudiobookImageCacher{}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "local/audiobooks/content-1/poster/original.webp",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 0 {
		t.Fatalf("CacheImage calls = %d, want 0", cacher.calls)
	}
	if result.PosterPath != "local/audiobooks/content-1/poster/original.webp" {
		t.Fatalf("PosterPath = %q", result.PosterPath)
	}
}

type fakeAudiobookImageCacher struct {
	calls int
	req   metadata.CacheImageRequest
}

func (f *fakeAudiobookImageCacher) CacheAudiobookCover(context.Context, []byte, string) (string, string, error) {
	return "", "", nil
}

func (f *fakeAudiobookImageCacher) CacheImage(_ context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	f.calls++
	f.req = req
	return &metadata.CacheImageResult{
		BasePath:     req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster",
		OriginalPath: req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster/original.test-revision.webp",
		Thumbhash:    "thumb",
		Ext:          ".webp",
	}, nil
}

type fakeAudiobookMetadataProvider struct {
	slug      string
	results   []metadata.SearchResult
	searchErr error
	result    *metadata.MetadataResult
	getErr    error
}

func (f *fakeAudiobookMetadataProvider) Slug() string       { return f.slug }
func (f *fakeAudiobookMetadataProvider) Name() string       { return f.slug }
func (f *fakeAudiobookMetadataProvider) ForTypes() []string { return []string{"audiobook"} }
func (f *fakeAudiobookMetadataProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return f.results, f.searchErr
}
func (f *fakeAudiobookMetadataProvider) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return f.result, f.getErr
}

func TestEnrichItemRetriesProviderErrorBeforeAuthorMismatch(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	providers := []metadata.Provider{
		&fakeAudiobookMetadataProvider{slug: "broken", searchErr: providerErr, getErr: providerErr},
		&fakeAudiobookMetadataProvider{
			slug:    "wrong-author",
			results: []metadata.SearchResult{{Name: "Shared Title", ProviderIDs: map[string]string{"wrong": "1"}}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				People: []models.ItemPerson{{
					Person: models.Person{Name: "Wrong Author"}, Kind: models.PersonKindAuthor,
				}},
			},
		},
	}
	e := &Enricher{
		resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
			return providers, nil
		},
	}

	err := e.enrichItem(context.Background(), enrichmentItemRow{
		ContentID: "shared", FolderID: 7, Title: "Shared Title", Author: "Right Author",
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("enrichment error = %v, want provider failure to remain retryable", err)
	}
}

type recordingAudiobookItemRepo struct {
	updates []*catalog.MetadataUpdate
}

func (r *recordingAudiobookItemRepo) UpdateMetadata(_ context.Context, _ string, upd *catalog.MetadataUpdate) error {
	r.updates = append(r.updates, upd)
	return nil
}

func (r *recordingAudiobookItemRepo) UpdateMetadataTx(_ context.Context, _ pgx.Tx, _ string, upd *catalog.MetadataUpdate) error {
	r.updates = append(r.updates, upd)
	return nil
}

func (r *recordingAudiobookItemRepo) ReplacePeopleKindsTx(context.Context, pgx.Tx, string, []models.PersonKind, []models.ItemPerson) error {
	return nil
}

// A provider's runtime describes whichever edition it matched, so it must not
// replace the length the scanner measured from the book's own files.
func TestPersistKeepsScannedRuntimeOverProviderRuntime(t *testing.T) {
	repo := &recordingAudiobookItemRepo{}
	e := &Enricher{itemRepo: repo}

	err := e.persist(context.Background(), enrichmentItemRow{ContentID: "book"}, nil, &metadata.MetadataResult{
		HasMetadata: true, Overview: "About the book", Runtime: 481,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("metadata updates = %d, want 1", len(repo.updates))
	}
	if got := repo.updates[0].Runtime; got != nil {
		t.Fatalf("update runtime = %d, want the scanned runtime left alone", *got)
	}
}

// countingProvider counts calls so a test can see the gate hold them back.
type countingProvider struct {
	fakeAudiobookMetadataProvider
	searches, fetches int
}

func (c *countingProvider) Search(ctx context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	c.searches++
	return c.fakeAudiobookMetadataProvider.Search(ctx, q)
}

func (c *countingProvider) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	c.fetches++
	return c.fakeAudiobookMetadataProvider.GetMetadata(ctx, req)
}

// Once a provider answers rate limited, the rest of the sweep leaves it alone
// for the provider's retry-after; an item whose whole chain is paused is
// deferred without a provider call. A bare 403 backs the item off but does not
// pause the provider, so a source that refuses everything cannot stall the
// sweep.
func TestProviderGatePausesARateLimitedProvider(t *testing.T) {
	retry, err := status.New(codes.ResourceExhausted, "slow down").WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(40 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		searchErr error
		wantPause time.Duration
	}{
		"retry-after honored":        {retry.Err(), 40 * time.Minute},
		"default pause":              {status.Error(codes.ResourceExhausted, "all 1 audiobook provider(s) failed: itunes (rate-limited)"), defaultProviderPause},
		"429 text":                   {errors.New("audnexus: HTTP 429"), defaultProviderPause},
		"untyped 403 does not pause": {status.Error(codes.Unknown, "itunes: HTTP 403"), 0},
		"transient does not pause":   {status.Error(codes.Unavailable, "down"), 0},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{slug: "abm", searchErr: tc.searchErr}}
			e := &Enricher{
				gate: providerGate{now: func() time.Time { return now }},
				resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
					return []metadata.Provider{provider}, nil
				},
			}
			item := enrichmentItemRow{ContentID: "book", FolderID: 1, Title: "Book", Author: "Author"}

			if err := e.enrichItem(context.Background(), item); err == nil {
				t.Fatal("first item: want the provider failure returned")
			}
			until, paused := e.gate.pausedUntil("abm", routeSearch)
			if tc.wantPause == 0 {
				if paused {
					t.Fatalf("provider paused until %v, want no pause", until)
				}
				return
			}
			if !paused || !until.Equal(now.Add(tc.wantPause)) {
				t.Fatalf("pause = (%v, %v), want until %v", until, paused, now.Add(tc.wantPause))
			}

			calls := provider.searches + provider.fetches
			if err := e.enrichItem(context.Background(), item); err != nil {
				t.Fatalf("second item: %v, want a quiet deferral", err)
			}
			if got := provider.searches + provider.fetches; got != calls {
				t.Fatalf("provider calls = %d after the pause, want %d", got, calls)
			}

			now = now.Add(tc.wantPause)
			if _, paused := e.gate.pausedUntil("abm", routeSearch); paused {
				t.Fatal("provider still paused after the retry-after passed")
			}
		})
	}
}

// A paused provider in a longer chain must not let the others' silence be
// recorded as a clean no-match, and an item whose only errors came from
// paused providers is deferred rather than charged a failure.
func TestPausedProviderKeepsItemOutOfNoMatch(t *testing.T) {
	paused := &fakeAudiobookMetadataProvider{slug: "paused"}
	failing := &fakeAudiobookMetadataProvider{slug: "failing", searchErr: errors.New("connection reset")}
	e := &Enricher{}
	e.gate.observe(context.Background(), "paused", routeSearch, status.Error(codes.ResourceExhausted, "busy"))
	item := enrichmentItemRow{ContentID: "book", FolderID: 1, Title: "Book"}

	pausedErr := &providerPausedError{slug: "paused", until: time.Now().Add(time.Minute)}
	if err := e.failOrDefer(context.Background(), item, []error{pausedErr}); err != nil {
		t.Fatalf("failOrDefer(paused only) = %v, want a quiet deferral", err)
	}

	e.resolveProviders = func(context.Context, int, string) ([]metadata.Provider, error) {
		return []metadata.Provider{paused, failing}, nil
	}
	err := e.enrichItem(context.Background(), item)
	if !errors.As(err, &pausedErr) {
		t.Fatalf("enrichItem = %v, want the paused provider reported with the real failure", err)
	}
	if got := classifyProviderError(err); got != EnrichmentErrorRateLimited {
		t.Fatalf("class = %q, want rate_limited", got)
	}
}

// A provider that keeps failing is paused as down after a short streak, the
// pause doubles while it stays down, and any answer clears it.
func TestProviderGatePausesAFailingProvider(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	g := &providerGate{now: func() time.Time { return now }}
	down := status.Error(codes.Unavailable, "down")
	ctx := context.Background()

	for range providerOutageStreak - 1 {
		g.observe(ctx, "abm", routeSearch, down)
	}
	if _, paused := g.pausedUntil("abm", routeSearch); paused {
		t.Fatal("paused before the failure streak was reached")
	}
	g.observe(ctx, "abm", routeSearch, down)
	if until, paused := g.pausedUntil("abm", routeSearch); !paused || !until.Equal(now.Add(providerOutagePause)) {
		t.Fatalf("first outage pause = (%v, %v), want %v", until, paused, providerOutagePause)
	}

	now = now.Add(providerOutagePause)
	for range providerOutageStreak {
		g.observe(ctx, "abm", routeSearch, down)
	}
	if until, _ := g.pausedUntil("abm", routeSearch); !until.Equal(now.Add(2 * providerOutagePause)) {
		t.Fatalf("second outage pause ends %v, want doubled to %v", until, now.Add(2*providerOutagePause))
	}

	// A per-item answer between transient failures breaks the streak.
	now = now.Add(2 * providerOutagePause)
	g.observe(ctx, "abm", routeSearch, down)
	g.observe(ctx, "abm", routeSearch, down)
	g.observe(ctx, "abm", routeSearch, status.Error(codes.NotFound, "no such ASIN"))
	g.observe(ctx, "abm", routeSearch, down)
	if _, paused := g.pausedUntil("abm", routeSearch); paused {
		t.Fatal("a NotFound answer did not break the failure streak")
	}

	// Item-level failures say nothing about the provider being down.
	g.observe(ctx, "abm", routeSearch, nil)
	for range providerOutageStreak {
		g.observe(ctx, "abm", routeSearch, status.Error(codes.NotFound, "no such ASIN"))
	}
	if _, paused := g.pausedUntil("abm", routeSearch); paused {
		t.Fatal("permanent per-item errors paused the provider")
	}
	for range providerOutageStreak {
		g.observe(ctx, "abm", routeSearch, down)
	}
	if until, _ := g.pausedUntil("abm", routeSearch); !until.Equal(now.Add(providerOutagePause)) {
		t.Fatalf("pause after a recovery ends %v, want back to %v", until, now.Add(providerOutagePause))
	}
}

// A book without provider IDs gets (nil, nil) from GetMetadata without any
// request. That must not reset the failure streak a failing Search builds,
// or an unavailable provider would never be paused.
func TestSkippedMetadataLookupKeepsTheFailureStreak(t *testing.T) {
	provider := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{
		slug: "abm", searchErr: status.Error(codes.Unavailable, "down"),
	}}
	e := &Enricher{resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
		return []metadata.Provider{provider}, nil
	}}
	item := enrichmentItemRow{ContentID: "book", FolderID: 1, Title: "Book"}
	for range providerOutageStreak {
		_ = e.enrichItem(context.Background(), item)
	}
	if _, paused := e.gate.pausedUntil("abm", routeSearch); !paused {
		t.Fatal("provider not paused after a streak of failed searches")
	}
}

// A book whose providers are all paused comes back when the first of them
// can answer, not the last.
func TestAllPausedReturnsTheEarliestPauseEnd(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	g := &providerGate{now: func() time.Time { return now }}
	ctx := context.Background()
	g.observe(ctx, "short", routeSearch, status.Error(codes.ResourceExhausted, "busy"))
	long, err := status.New(codes.ResourceExhausted, "busy").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	g.observe(ctx, "long", routeSearch, long.Err())
	providers := []metadata.Provider{
		&fakeAudiobookMetadataProvider{slug: "short"},
		&fakeAudiobookMetadataProvider{slug: "long"},
	}
	if until, ok := g.allPaused(providers); !ok || !until.Equal(now.Add(defaultProviderPause)) {
		t.Fatalf("allPaused = (%v, %v), want the short pause end %v", until, ok, now.Add(defaultProviderPause))
	}
	errs := []error{
		&providerPausedError{slug: "long", until: now.Add(48 * time.Hour)},
		&providerPausedError{slug: "short", until: now.Add(defaultProviderPause)},
	}
	if until, ok := pausedOnly(errs); !ok || !until.Equal(now.Add(defaultProviderPause)) {
		t.Fatalf("pausedOnly = (%v, %v), want the short pause end", until, ok)
	}
}

// A metadata request that returns a result proves the provider is up, so it
// breaks a streak of transient failures; failures separated by successes must
// not pause a healthy provider.
func TestSuccessfulMetadataLookupClearsTheFailureStreak(t *testing.T) {
	g := &providerGate{}
	ctx := context.Background()
	healthy := &fakeAudiobookMetadataProvider{slug: "abm", result: &metadata.MetadataResult{HasMetadata: true}}
	gated := g.wrap([]metadata.Provider{healthy})[0].(metadata.MetadataProvider)
	down := status.Error(codes.Unavailable, "down")

	for range 3 {
		for range providerOutageStreak - 1 {
			g.observe(ctx, "abm", routeMetadata, down)
		}
		if _, err := gated.GetMetadata(ctx, metadata.MetadataRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, paused := g.pausedUntil("abm", routeMetadata); paused {
		t.Fatal("intermittent failures between successful lookups paused the provider")
	}
}

// A source can lose its title search while lookups by ID still answer. Only
// the failing route is paused, so books the other route can match are still
// asked. Failing lookups pause the whole provider, and a rate-limit answer on
// one route closes both.
func TestProviderGatePausesOnlyTheFailingRoute(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	g := &providerGate{now: func() time.Time { return now }}
	ctx := context.Background()
	provider := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{
		slug:      "abm",
		searchErr: status.Error(codes.Unavailable, "search upstream down"),
		result:    &metadata.MetadataResult{HasMetadata: true},
	}}
	chain := g.wrap([]metadata.Provider{provider})
	search, lookup := chain[0].(metadata.SearchProvider), chain[0].(metadata.MetadataProvider)

	// Lookups answering between the failed searches do not hide the outage.
	for range providerOutageStreak {
		_, _ = search.Search(ctx, metadata.SearchQuery{})
		if _, err := lookup.GetMetadata(ctx, metadata.MetadataRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	var paused *providerPausedError
	if _, err := search.Search(ctx, metadata.SearchQuery{}); !errors.As(err, &paused) {
		t.Fatalf("search after the streak = %v, want the route paused", err)
	}
	if provider.searches != providerOutageStreak {
		t.Fatalf("searches = %d, want none after the pause began", provider.searches)
	}
	if _, err := lookup.GetMetadata(ctx, metadata.MetadataRequest{}); err != nil {
		t.Fatalf("lookup while search is paused = %v, want it still asked", err)
	}
	if _, all := g.allPaused(chain); all {
		t.Fatal("provider reported paused while its lookups still answer")
	}

	// The reverse outage closes both: a search could not fetch what it finds.
	down := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{
		slug: "abm", getErr: status.Error(codes.Unavailable, "down"),
	}}
	g = &providerGate{now: func() time.Time { return now }}
	chain = g.wrap([]metadata.Provider{down})
	for range providerOutageStreak {
		_, _ = chain[0].(metadata.MetadataProvider).GetMetadata(ctx, metadata.MetadataRequest{})
	}
	if _, err := chain[0].(metadata.SearchProvider).Search(ctx, metadata.SearchQuery{}); !errors.As(err, &paused) || down.searches != 0 {
		t.Fatalf("search while lookups are paused = %v after %d call(s), want it paused unasked", err, down.searches)
	}
	if until, all := g.allPaused(chain); !all || !until.Equal(now.Add(providerOutagePause)) {
		t.Fatalf("allPaused = (%v, %v), want the provider paused until %v", until, all, now.Add(providerOutagePause))
	}

	// An outage pause holds back only the items that met it; a rate limit
	// holds back every item of the chain.
	if until := g.latestRateLimit([]string{"abm"}); !until.IsZero() {
		t.Fatalf("latestRateLimit = %v for a route paused as down, want none", until)
	}
	if until := latestPauseMet([]error{errors.New("lookup failed"), paused}); !until.Equal(now.Add(providerOutagePause)) {
		t.Fatalf("latestPauseMet = %v, want the pause the item met", until)
	}
	limited := &providerGate{now: func() time.Time { return now }}
	limited.observe(ctx, "abm", routeSearch, status.Error(codes.ResourceExhausted, "busy"))
	if _, ok := limited.pausedUntil("abm", routeMetadata); !ok {
		t.Fatal("a rate-limit answer on search left lookups open")
	}
	if until := limited.latestRateLimit([]string{"abm"}); !until.Equal(now.Add(defaultProviderPause)) {
		t.Fatalf("latestRateLimit = %v, want %v", until, now.Add(defaultProviderPause))
	}
}

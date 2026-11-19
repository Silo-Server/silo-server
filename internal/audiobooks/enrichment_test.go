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

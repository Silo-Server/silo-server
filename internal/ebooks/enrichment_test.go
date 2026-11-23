package ebooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/bookid"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// newRunEnricher builds an Enricher that claims every queued job in one batch,
// loads items as the claimed rows, and enriches each row with enrich.
func newRunEnricher(queue *fakeEnrichmentQueue, workers int, items []enrichmentItemRow, enrich func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error)) *Enricher {
	return &Enricher{
		queue:     queue,
		batchSize: len(queue.jobs),
		workers:   workers,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return items, nil
		},
		enrichClaimedItemFn: enrich,
	}
}

func TestEnricherRunReleasesEveryLeaseOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{
			{ContentID: "first", Token: "first-token", Attempts: 1},
			{ContentID: "second", Token: "second-token", Attempts: 1},
		},
	}
	items := []enrichmentItemRow{{ContentID: "first"}, {ContentID: "second"}}
	e := newRunEnricher(queue, 1, items, func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
		cancel()
		return "", context.Canceled
	})

	_, err := e.Run(ctx, EnrichmentScopeIncremental)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	sort.Strings(queue.released)
	if got := strings.Join(queue.released, ","); got != "first,second" {
		t.Fatalf("released leases = %q, want first,second", got)
	}
	if queue.releaseSawCanceledContext {
		t.Fatal("lease release used the canceled run context")
	}
	if len(queue.failed) != 0 {
		t.Fatalf("cancellation recorded item failures: %v", queue.failed)
	}
}

func TestEnricherRunReleasesLeaseWhenCompletionLosesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeEnrichmentQueue{
		jobs:           []EnrichmentJob{{ContentID: "success", Token: "success-token", Attempts: 1}},
		completeCancel: cancel,
		completeErr:    context.Canceled,
	}
	e := newRunEnricher(queue, 1, []enrichmentItemRow{{ContentID: "success"}}, func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
		return EnrichmentOutcomeSuccess, nil
	})

	_, err := e.Run(ctx, EnrichmentScopeIncremental)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if got := strings.Join(queue.released, ","); got != "success" {
		t.Fatalf("released leases = %q, want success", got)
	}
	if queue.releaseSawCanceledContext {
		t.Fatal("lease release used the canceled transition context")
	}
}

func TestEnricherRunBoundsEachItemBelowTheLease(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{{ContentID: "slow", Token: "slow-token", Attempts: 1}},
	}
	e := newRunEnricher(queue, 1, []enrichmentItemRow{{ContentID: "slow"}}, func(ctx context.Context, _ enrichmentItemRow) (EnrichmentOutcome, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	e.itemTimeout = 20 * time.Millisecond

	started := time.Now()
	if _, err := e.Run(context.Background(), EnrichmentScopeIncremental); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("per-item timeout took %s", elapsed)
	}
	if got := queue.failed["slow"]; got != EnrichmentErrorTransient {
		t.Fatalf("timeout failure class = %q, want transient", got)
	}
	if len(queue.released) != 0 {
		t.Fatalf("timed-out work was released immediately due: %v", queue.released)
	}
	if defaultEnrichmentItemTimeout >= defaultEnrichmentLease/2 {
		t.Fatalf("default item timeout %s is not materially shorter than lease %s", defaultEnrichmentItemTimeout, defaultEnrichmentLease)
	}
}

func TestEnrichWithProvidersRejectsLostLeaseBeforePersistence(t *testing.T) {
	tests := []struct {
		name     string
		provider metadata.Provider
	}{
		{
			name: "matched metadata",
			provider: &fakeEbookMetadataProvider{
				slug: "provider",
				result: &metadata.MetadataResult{
					HasMetadata: true,
					Overview:    "remote overview",
				},
			},
		},
		{
			name:     "no match timestamp",
			provider: &fakeEbookMetadataProvider{slug: "provider"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := 0
			ctx := withEnrichmentClaimCheck(context.Background(), func(context.Context) error {
				checks++
				return ErrEnrichmentLeaseLost
			})
			e := &Enricher{}

			_, err := e.enrichWithProvidersOutcome(ctx, enrichmentItemRow{
				ContentID: "lost",
				FolderID:  7,
			}, []metadata.Provider{tt.provider})
			if !errors.Is(err, ErrEnrichmentLeaseLost) {
				t.Fatalf("enrich error = %v, want ErrEnrichmentLeaseLost", err)
			}
			if checks != 1 {
				t.Fatalf("claim ownership checks = %d, want 1", checks)
			}
		})
	}
}

func TestCollectEbookMetadataRejectsEachProviderAuthorBeforeMerging(t *testing.T) {
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{
			slug:    "wrong",
			results: []metadata.SearchResult{{Name: "Shared Title", ProviderIDs: map[string]string{"wrong": "1"}}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				Overview:    "wrong provider overview",
				People: []models.ItemPerson{{
					Person: models.Person{Name: "Wrong Author"}, Kind: models.PersonKindAuthor,
				}},
			},
		},
		&fakeEbookMetadataProvider{
			slug:    "right",
			results: []metadata.SearchResult{{Name: "Shared Title", ProviderIDs: map[string]string{"right": "2"}}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				People: []models.ItemPerson{{
					Person: models.Person{Name: "Right Author"}, Kind: models.PersonKindAuthor,
				}},
			},
		},
	}

	accumulator, _, _, authorMismatch := collectEbookMetadata(context.Background(), enrichmentItemRow{
		ContentID: "shared", Title: "Shared Title", Author: "Right Author",
	}, providers, nil)

	if !authorMismatch {
		t.Fatal("a provider with a contradictory author did not fail the item closed")
	}
	if accumulator.Overview != "" || accumulator.HasMetadata {
		t.Fatalf("contradictory provider metadata was merged before validation: %+v", accumulator)
	}
}

func TestEnrichWithProvidersRetriesProviderErrorBeforeAuthorMismatch(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{slug: "broken", searchErr: providerErr, getErr: providerErr},
		&fakeEbookMetadataProvider{
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

	e := &Enricher{}
	_, err := e.enrichWithProvidersOutcome(context.Background(), enrichmentItemRow{
		ContentID: "shared", FolderID: 7, Title: "Shared Title", Author: "Right Author",
	}, providers)
	if !errors.Is(err, providerErr) {
		t.Fatalf("enrichment error = %v, want provider failure to remain retryable", err)
	}
}

type fakeProviderIDOwner struct {
	ownerByID map[string]string // provider_id -> owning content id
	err       error
}

func (f *fakeProviderIDOwner) FindContentIDByProviderIDs(_ context.Context, ids map[string]string, _ string, exclude string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	for _, v := range ids {
		if owner, ok := f.ownerByID[v]; ok && owner != exclude {
			return owner, nil
		}
	}
	return "", nil
}

func TestMergeEbookAuthorCreditsPreservesOtherPeopleKinds(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{ID: 10, Name: "Old Author"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{ID: 20, Name: "Manual Writer"}, Kind: models.PersonKindWriter, SortOrder: 1, Character: "essay"},
		{Person: models.Person{ID: 30, Name: "Stale Narrator"}, Kind: models.PersonKindNarrator, SortOrder: 2},
	}
	authors := []models.ItemPerson{
		{Person: models.Person{ID: 40, Name: "Provider Author"}, Kind: models.PersonKindAuthor},
	}

	got := mergeEbookAuthorCredits(existing, authors)

	if len(got) != 2 {
		t.Fatalf("merged people len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Person.ID != 20 || got[0].Kind != models.PersonKindWriter || got[0].Character != "essay" || got[0].SortOrder != 0 {
		t.Fatalf("preserved non-author credit = %+v", got[0])
	}
	if got[1].Person.ID != 40 || got[1].Kind != models.PersonKindAuthor || got[1].SortOrder != 1 {
		t.Fatalf("provider author credit = %+v", got[1])
	}
}

func TestPreserveDurableEbookLocalMetadataAcrossRefreshes(t *testing.T) {
	item := enrichmentItemRow{
		Status: "matched",
		ProtectedFields: []string{
			"year", "overview", "release_date", "genres", "studios", "poster_path", "authors",
		},
	}
	result := &metadata.MetadataResult{
		HasMetadata: true,
		Year:        2022,
		Overview:    "Remote description",
		ReleaseDate: "2022-04-05",
		Genres:      []string{"Remote genre"},
		Studios:     []string{"Remote publisher"},
		PosterPath:  "https://example.test/remote.jpg",
		Tagline:     "Remote-only field",
		ProviderIDs: map[string]string{"isbn": "9780306406157"},
		People: []models.ItemPerson{
			{Person: models.Person{Name: "Remote Author"}, Kind: models.PersonKindAuthor},
		},
	}

	preserveEbookLocalMetadata(item, result)

	if result.Year != 0 || result.Overview != "" || result.ReleaseDate != "" ||
		len(result.Genres) != 0 || len(result.Studios) != 0 || result.PosterPath != "" ||
		len(result.People) != 0 {
		t.Fatalf("remote fields would overwrite scanner metadata: %+v", result)
	}
	if result.Tagline != "Remote-only field" {
		t.Fatalf("empty local field was not enrichable: tagline=%q", result.Tagline)
	}
	if result.ProviderIDs["isbn"] != "9780306406157" {
		t.Fatalf("provider identity was discarded: %v", result.ProviderIDs)
	}
}

func TestPreserveEbookLocalPosterDuringControlledRefresh(t *testing.T) {
	result := &metadata.MetadataResult{
		HasMetadata:     true,
		PosterPath:      "https://example.test/replacement.jpg",
		PosterThumbhash: "remote-thumb",
		BackdropPath:    "https://example.test/backdrop.jpg",
		LogoPath:        "https://example.test/logo.png",
		Overview:        "Corrected remote description",
	}

	preserveEbookLocalMetadata(enrichmentItemRow{
		Status:       "matched",
		PosterPath:   "local/ebooks/book/poster/original.webp",
		BackdropPath: "/books/book/backdrop.jpg",
		LogoPath:     "embedded/book/logo.png",
	}, result)

	if result.PosterPath != "" || result.PosterThumbhash != "" ||
		result.BackdropPath != "" || result.LogoPath != "" {
		t.Fatalf("controlled refresh would replace local poster: %+v", result)
	}
	if result.Overview != "Corrected remote description" {
		t.Fatalf("non-artwork refresh field was suppressed: %+v", result)
	}
}

func TestPreserveEbookMetadataHonorsGenericLockedFields(t *testing.T) {
	result := &metadata.MetadataResult{
		HasMetadata:   true,
		Title:         "Remote title",
		Overview:      "Remote overview",
		Year:          2024,
		ReleaseDate:   "2024-01-02",
		Runtime:       500,
		Genres:        []string{"Remote genre"},
		Studios:       []string{"Remote publisher"},
		ContentRating: "Teen",
		PosterPath:    "https://example.test/poster.jpg",
		BackdropPath:  "https://example.test/backdrop.jpg",
		LogoPath:      "https://example.test/logo.png",
		People: []models.ItemPerson{
			{Person: models.Person{Name: "Remote Author"}, Kind: models.PersonKindAuthor},
		},
	}
	item := enrichmentItemRow{LockedFields: []int{
		int(metadata.FieldName),
		int(metadata.FieldOverview),
		int(metadata.FieldGenres),
		int(metadata.FieldStudios),
		int(metadata.FieldCrew),
		int(metadata.FieldRuntime),
		int(metadata.FieldContentRating),
		int(metadata.FieldImages),
		int(metadata.FieldReleaseDates),
	}}

	preserveEbookLocalMetadata(item, result)

	if result.Title != "" || result.Overview != "" || result.Year != 0 || result.ReleaseDate != "" ||
		result.Runtime != 0 || len(result.Genres) != 0 || len(result.Studios) != 0 ||
		result.ContentRating != "" || result.PosterPath != "" || result.BackdropPath != "" ||
		result.LogoPath != "" || len(result.People) != 0 {
		t.Fatalf("locked metadata was not protected: %+v", result)
	}
}

func TestEnricherRunTransitionsClaimedJobsByOutcome(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{
			{ContentID: "success", Token: "success-token", Attempts: 1},
			{ContentID: "no-match", Token: "no-match-token", Attempts: 1},
			{ContentID: "skipped", Token: "skipped-token", Attempts: 1},
			{ContentID: "failed", Token: "failed-token", Attempts: 3},
		},
	}
	items := []enrichmentItemRow{
		{ContentID: "success"},
		{ContentID: "no-match"},
		{ContentID: "skipped"},
		{ContentID: "failed"},
	}
	e := newRunEnricher(queue, 2, items, func(_ context.Context, item enrichmentItemRow) (EnrichmentOutcome, error) {
		switch item.ContentID {
		case "success":
			return EnrichmentOutcomeSuccess, nil
		case "no-match":
			return EnrichmentOutcomeNoMatch, nil
		case "skipped":
			return EnrichmentOutcomeSkipped, nil
		default:
			return "", errors.New("provider unavailable")
		}
	})

	result, err := e.Run(context.Background(), EnrichmentScopeIncremental)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := EnrichmentRunResult{Claimed: 4, Enriched: 1, NoMatch: 1, Failed: 1, Deferred: 1}
	if result != want {
		t.Fatalf("Run() result = %+v, want %+v", result, want)
	}
	if queue.claimCalls != 1 {
		t.Fatalf("claim calls = %d, want 1", queue.claimCalls)
	}
	if queue.claimLimit != e.workers || queue.leaseDuration <= 0 {
		t.Fatalf("claim args: limit=%d lease=%s", queue.claimLimit, queue.leaseDuration)
	}
	for contentID, want := range map[string]EnrichmentOutcome{
		"success":  EnrichmentOutcomeSuccess,
		"no-match": EnrichmentOutcomeNoMatch,
		"skipped":  EnrichmentOutcomeSkipped,
	} {
		if got := queue.completed[contentID]; got != want {
			t.Fatalf("completed[%q] = %q, want %q", contentID, got, want)
		}
	}
	if got := queue.failed["failed"]; got != EnrichmentErrorTransient {
		t.Fatalf("failed class = %q, want transient", got)
	}
	for contentID, job := range queue.transitionedJobs {
		if job.Token != contentID+"-token" {
			t.Fatalf("transition for %q used token %q", contentID, job.Token)
		}
	}
}

func TestEnricherRunCountsUpstreamCancellationAsTransientFailure(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{{ContentID: "cancel-happy", Token: "cancel-token", Attempts: 1}},
	}
	e := newRunEnricher(queue, 1, []enrichmentItemRow{{ContentID: "cancel-happy"}}, func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
		return "", context.Canceled
	})

	result, err := e.Run(context.Background(), EnrichmentScopeIncremental)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := queue.failed["cancel-happy"]; got != EnrichmentErrorTransient {
		t.Fatalf("failure class = %q, want transient", got)
	}
	if result.Failed != 1 {
		t.Fatalf("Failed = %d, want 1; an uncounted claim blinds the no-progress circuit breaker", result.Failed)
	}
	if len(queue.released) != 0 {
		t.Fatalf("provider-canceled work was released into immediate reclaim churn: %v", queue.released)
	}
}

func TestEnricherRunDiscardsClaimedRowsThatAreNoLongerEligible(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{
			{ContentID: "became-manga", Token: "manga-token", Attempts: 1},
			{ContentID: "folderless", Token: "folderless-token", Attempts: 1},
		},
	}
	e := newRunEnricher(queue, 1, []enrichmentItemRow{{ContentID: "folderless", FolderID: 0}}, func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
		return EnrichmentOutcomeSkipped, nil
	})

	result, err := e.Run(context.Background(), EnrichmentScopeIncremental)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.Join(queue.discarded, ","); got != "became-manga" {
		t.Fatalf("discarded = %q, want became-manga", got)
	}
	if got := queue.completed["folderless"]; got != EnrichmentOutcomeSkipped {
		t.Fatalf("folderless outcome = %q, want skipped", got)
	}
	if len(queue.released) != 0 {
		t.Fatalf("ineligible rows were released back into immediate churn: %v", queue.released)
	}
	if result.Discarded != 1 {
		t.Fatalf("Discarded = %d, want 1", result.Discarded)
	}
	if result.Deferred != 1 {
		t.Fatalf("Deferred = %d, want 1 (the skipped completion only); a discarded row is terminal, not deferred for retry", result.Deferred)
	}
}

func TestEnricherRunInjectsExactClaimOwnershipCheck(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs:          []EnrichmentJob{{ContentID: "lost", Token: "exact-token", Attempts: 1}},
		claimCheckErr: ErrEnrichmentLeaseLost,
		failErr:       ErrEnrichmentLeaseLost,
	}
	e := newRunEnricher(queue, 1, []enrichmentItemRow{{ContentID: "lost"}}, func(ctx context.Context, _ enrichmentItemRow) (EnrichmentOutcome, error) {
		return "", requireEnrichmentClaim(ctx)
	})

	if _, err := e.Run(context.Background(), EnrichmentScopeIncremental); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if queue.claimCheckCalls != 1 {
		t.Fatalf("claim checks = %d, want 1", queue.claimCheckCalls)
	}
	if got := queue.transitionedJobs["lost"].Token; got != "exact-token" {
		t.Fatalf("ownership check token = %q, want exact-token", got)
	}
	if len(queue.completed) != 0 {
		t.Fatalf("lost claim completed: %v", queue.completed)
	}
}

func TestEnrichWithProvidersReturnsFailureWhenAllProvidersError(t *testing.T) {
	providerErr := errors.New("provider exploded")
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{slug: "p1", searchErr: providerErr, getErr: providerErr},
		&fakeEbookMetadataProvider{slug: "p2", searchErr: providerErr, getErr: providerErr},
	}

	e := &Enricher{}
	_, err := e.enrichWithProvidersOutcome(context.Background(), enrichmentItemRow{ContentID: "c1", FolderID: 7, Title: "t"}, providers)
	if err == nil {
		t.Fatal("enrichWithProviders = nil, want error so the failure cap engages instead of stamping")
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("enrichWithProviders error = %v, want wrapped provider error", err)
	}
}

type fakeEbookProviderIDRepository struct {
	rows       map[string][]*models.MediaItemProviderID
	err        error
	replaceErr error
	calls      [][]string
}

func (f *fakeEbookProviderIDRepository) GetByContentIDs(
	_ context.Context,
	contentIDs []string,
) (map[string][]*models.MediaItemProviderID, error) {
	f.calls = append(f.calls, append([]string(nil), contentIDs...))
	return f.rows, f.err
}

func (f *fakeEbookProviderIDRepository) ReplaceByContentIDWithWorkClaims(context.Context, string, map[string]string, []bookid.WorkClaims) error {
	return f.replaceErr
}

func (f *fakeEbookProviderIDRepository) FindContentIDByProviderIDs(
	context.Context,
	map[string]string,
	string,
	string,
) (string, error) {
	return "", nil
}

func TestPersistReturnsProviderIDFailure(t *testing.T) {
	replaceErr := errors.New("provider identity already belongs to another item")
	e := &Enricher{providerIDs: &fakeEbookProviderIDRepository{replaceErr: replaceErr}}
	ctx := withEnrichmentClaimCheck(context.Background(), func(context.Context) error { return nil })

	err := e.persist(ctx, "ebook-1", map[string]string{"isbn": "9780306406157"}, &metadata.MetadataResult{
		HasMetadata: true,
		Overview:    "remote overview",
	})

	if !errors.Is(err, replaceErr) {
		t.Fatalf("persist error = %v, want provider-ID failure %v", err, replaceErr)
	}
}

func TestEnricherLoadsProviderIDsInOneBatchAndSurfacesErrors(t *testing.T) {
	repo := &fakeEbookProviderIDRepository{
		rows: map[string][]*models.MediaItemProviderID{
			"ebook-1": {
				{ContentID: "ebook-1", ItemType: "ebook", Provider: "isbn", ProviderID: "9781982173456"},
			},
		},
	}
	e := &Enricher{providerIDs: repo}
	items := []enrichmentItemRow{{ContentID: "ebook-1"}, {ContentID: "ebook-2"}}

	if err := e.loadProviderIDs(context.Background(), []string{"ebook-1", "ebook-2"}, items); err != nil {
		t.Fatalf("loadProviderIDs() error = %v", err)
	}
	if len(repo.calls) != 1 || strings.Join(repo.calls[0], ",") != "ebook-1,ebook-2" {
		t.Fatalf("GetByContentIDs() calls = %#v, want one batch", repo.calls)
	}
	if got := items[0].ProviderIDs["isbn"]; got != "9781982173456" {
		t.Fatalf("items[0].ProviderIDs[isbn] = %q", got)
	}

	repo.err = errors.New("provider IDs unavailable")
	if err := e.loadProviderIDs(context.Background(), []string{"ebook-1"}, items[:1]); err == nil {
		t.Fatal("loadProviderIDs() error = nil, want repository error")
	}
}

type fakeEbookMetadataProvider struct {
	slug      string
	searchErr error
	results   []metadata.SearchResult
	getErr    error
	result    *metadata.MetadataResult
}

func (f *fakeEbookMetadataProvider) Slug() string       { return f.slug }
func (f *fakeEbookMetadataProvider) Name() string       { return f.slug }
func (f *fakeEbookMetadataProvider) ForTypes() []string { return []string{"ebook"} }
func (f *fakeEbookMetadataProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return f.results, f.searchErr
}
func (f *fakeEbookMetadataProvider) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return f.result, f.getErr
}

type fakeEnrichmentQueue struct {
	mu                        sync.Mutex
	jobs                      []EnrichmentJob
	claimCalls                int
	claimLimit                int
	leaseDuration             time.Duration
	claimScope                EnrichmentScope
	remaining                 int
	hasReady                  bool
	readyCountCalls           int
	hasReadyCalls             int
	completed                 map[string]EnrichmentOutcome
	failed                    map[string]EnrichmentErrorClass
	retryAfter                map[string]time.Duration
	released                  []string
	discarded                 []string
	transitionedJobs          map[string]EnrichmentJob
	claimCheckCalls           int
	claimCheckErr             error
	failErr                   error
	releaseSawCanceledContext bool
	completeCancel            context.CancelFunc
	completeErr               error
}

func (f *fakeEnrichmentQueue) ClaimBatch(_ context.Context, scope EnrichmentScope, limit int, leaseDuration time.Duration) ([]EnrichmentJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	f.claimLimit = limit
	f.leaseDuration = leaseDuration
	f.claimScope = scope
	return append([]EnrichmentJob(nil), f.jobs...), nil
}

func (f *fakeEnrichmentQueue) ReadyCount(_ context.Context, _ EnrichmentScope) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyCountCalls++
	return f.remaining, nil
}

func (f *fakeEnrichmentQueue) HasReady(_ context.Context, _ EnrichmentScope) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hasReadyCalls++
	return f.hasReady, nil
}

func (f *fakeEnrichmentQueue) Complete(_ context.Context, job EnrichmentJob, outcome EnrichmentOutcome, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.completed == nil {
		f.completed = make(map[string]EnrichmentOutcome)
	}
	f.completed[job.ContentID] = outcome
	f.recordTransition(job)
	if f.completeCancel != nil {
		f.completeCancel()
	}
	return f.completeErr
}

func (f *fakeEnrichmentQueue) Fail(_ context.Context, job EnrichmentJob, errorClass EnrichmentErrorClass, _ string, retryAfter time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed == nil {
		f.failed = make(map[string]EnrichmentErrorClass)
	}
	if f.retryAfter == nil {
		f.retryAfter = make(map[string]time.Duration)
	}
	f.failed[job.ContentID] = errorClass
	f.retryAfter[job.ContentID] = retryAfter
	f.recordTransition(job)
	return f.failErr
}

func (f *fakeEnrichmentQueue) Release(ctx context.Context, job EnrichmentJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseSawCanceledContext = f.releaseSawCanceledContext || ctx.Err() != nil
	f.released = append(f.released, job.ContentID)
	f.recordTransition(job)
	return nil
}

func (f *fakeEnrichmentQueue) Discard(_ context.Context, job EnrichmentJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discarded = append(f.discarded, job.ContentID)
	f.recordTransition(job)
	return nil
}

func (f *fakeEnrichmentQueue) CheckClaim(_ context.Context, job EnrichmentJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCheckCalls++
	f.recordTransition(job)
	return f.claimCheckErr
}

func (f *fakeEnrichmentQueue) recordTransition(job EnrichmentJob) {
	if f.transitionedJobs == nil {
		f.transitionedJobs = make(map[string]EnrichmentJob)
	}
	f.transitionedJobs[job.ContentID] = job
}

// An item with no author has no author check after GetMetadata, so a partial
// title match alone must not give it another book's metadata (#4154).
func TestEbookWithoutAnAuthorNeedsTheWholeTitle(t *testing.T) {
	for _, tc := range []struct {
		name, author, candidate string
		matched                 bool
	}{
		{name: "a one-word prefix", candidate: "Raw"},
		{name: "the same title", candidate: "Raw Stream", matched: true},
		{name: "a one-word prefix with an author", author: "Someone", candidate: "Raw", matched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providers := []metadata.Provider{&fakeEbookMetadataProvider{
				slug:    "openlibrary",
				results: []metadata.SearchResult{{Name: tc.candidate, ProviderIDs: map[string]string{"openlibrary": "1"}}},
			}}
			// The admitted provider IDs are what GetMetadata fetches by.
			_, ids, _, _ := collectEbookMetadata(context.Background(), enrichmentItemRow{
				ContentID: "c1", Title: "Raw Stream", Author: tc.author,
			}, providers, nil)
			if matched := ids["openlibrary"] == "1"; matched != tc.matched {
				t.Fatalf("%q for \"Raw Stream\" (author %q): matched = %v, want %v", tc.candidate, tc.author, matched, tc.matched)
			}
		})
	}
}

// A no-author ebook whose title carries a year the search drops is matched
// against the title it was searched with: "Dune (2019)" is searched as "Dune"
// and must accept "Dune", while a longer work still fails the title check.
func TestEbookWithoutAnAuthorMatchesTheSearchedTitle(t *testing.T) {
	for _, tc := range []struct {
		candidate string
		matched   bool
	}{
		{candidate: "Dune", matched: true},
		{candidate: "Dune Messiah"},
	} {
		t.Run(tc.candidate, func(t *testing.T) {
			providers := []metadata.Provider{&fakeEbookMetadataProvider{
				slug:    "openlibrary",
				results: []metadata.SearchResult{{Name: tc.candidate, ProviderIDs: map[string]string{"openlibrary": "1"}}},
			}}
			_, ids, _, _ := collectEbookMetadata(context.Background(), enrichmentItemRow{ContentID: "c1", Title: "Dune (2019)"}, providers, nil)
			if matched := ids["openlibrary"] == "1"; matched != tc.matched {
				t.Fatalf("%q for \"Dune (2019)\": matched = %v, want %v", tc.candidate, matched, tc.matched)
			}
		})
	}
}

// Two editions of one book enriched apart both keep the work id their record
// claims, stored with its evidence and source, beside their own ISBN. A
// record admission rejects contributes no claim.
func TestEbookEnrichmentStoresWorkClaimsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := withEnrichmentClaimCheck(t.Context(), func(context.Context) error { return nil })
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e := &Enricher{pool: pool, itemRepo: catalog.NewItemRepository(pool), providerIDs: catalog.NewProviderIDRepository(pool)}
	work := "OL" + strings.ToUpper(uuid.NewString()[:6]) + "W"

	for i := range 2 {
		contentID := "ebook-work-" + uuid.NewString()
		isbn := fmt.Sprintf("979%010d", time.Now().UnixNano()%10_000_000_000+int64(i))
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'ebook', 'Book', '', '{}'::text[])`, contentID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		})
		claims := func(source, key, value string) []bookid.WorkClaims {
			return []bookid.WorkClaims{{Source: source, Claims: []bookid.WorkClaim{{Provider: key, ProviderID: value, Evidence: bookid.EvidenceISBN}}}}
		}
		providers := []metadata.Provider{
			&fakeEbookMetadataProvider{slug: "books-metadata", result: &metadata.MetadataResult{
				HasMetadata: true, Overview: "Overview",
				ProviderIDs: map[string]string{"isbn": isbn},
				WorkClaims:  claims("books-metadata", bookid.KeyOpenLibraryWork, work),
			}},
			// Names another edition's ISBN, so admission rejects the record.
			&fakeEbookMetadataProvider{slug: "other", result: &metadata.MetadataResult{
				HasMetadata: true,
				ProviderIDs: map[string]string{"isbn": "9780306406157"},
				WorkClaims:  claims("other", bookid.KeyHardcover, "9"),
			}},
		}
		item := enrichmentItemRow{ContentID: contentID, Title: "Book", ProviderIDs: map[string]string{"isbn": isbn}}
		if outcome, err := e.enrichWithProvidersOutcome(ctx, item, providers); err != nil || outcome != EnrichmentOutcomeSuccess {
			t.Fatalf("enrich edition %d = %q, %v", i, outcome, err)
		}
		var got []string
		rows, err := pool.Query(ctx, `
			SELECT 'work:' || provider || '=' || provider_id || '/' || evidence || '/' || source FROM media_item_work_ids WHERE content_id = $1
			UNION ALL
			SELECT 'id:' || provider || '=' || provider_id FROM media_item_provider_ids WHERE content_id = $1
			ORDER BY 1`, contentID)
		if err == nil {
			got, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"id:isbn=" + isbn, "work:openlibrary_work=" + work + "/isbn/books-metadata"}; !slices.Equal(got, want) {
			t.Fatalf("edition %d rows = %q, want %q", i, got, want)
		}
	}
}

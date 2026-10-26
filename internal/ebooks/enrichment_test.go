package ebooks

import (
	"context"
	"errors"

	"sort"
	"strings"
	"sync"

	"testing"
	"time"

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

func TestCollectEbookMetadataSkipsProviderIDOwnedByAnotherItem(t *testing.T) {
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{
			slug:    "bookinfo",
			results: []metadata.SearchResult{{Name: "t", ProviderIDs: map[string]string{"bookinfo": "40817436"}}},
			result:  &metadata.MetadataResult{HasMetadata: true, Overview: "book one"},
		},
	}
	owner := &fakeProviderIDOwner{ownerByID: map[string]string{"40817436": "other-book"}}

	_, ids, errs, _ := collectEbookMetadata(context.Background(), enrichmentItemRow{ContentID: "c2", Title: "t"}, providers, owner)

	if len(errs) != 0 {
		t.Fatalf("unexpected provider errors: %v", errs)
	}
	if _, ok := ids["bookinfo"]; ok {
		t.Fatalf("provider id owned by another item was claimed: %v", ids)
	}
}

func TestCollectEbookMetadataDoesNotReintroduceOwnedCrossID(t *testing.T) {
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{
			slug: "bookinfo",
			results: []metadata.SearchResult{{
				Name: "t",
				ProviderIDs: map[string]string{
					"bookinfo":    "owned-id",
					"openlibrary": "free-id",
				},
			}},
			result: &metadata.MetadataResult{
				HasMetadata: true,
				Overview:    "usable metadata",
				ProviderIDs: map[string]string{
					"bookinfo":    "owned-id",
					"openlibrary": "free-id",
				},
			},
		},
	}
	owner := &fakeProviderIDOwner{ownerByID: map[string]string{"owned-id": "other-book"}}

	accumulator, ids, errs, authorMismatch := collectEbookMetadata(
		context.Background(),
		enrichmentItemRow{ContentID: "this-book", Title: "t"},
		providers,
		owner,
	)

	if len(errs) != 0 || authorMismatch {
		t.Fatalf("collect errors = %v, authorMismatch = %v", errs, authorMismatch)
	}
	if accumulator.Overview != "usable metadata" || ids["openlibrary"] != "free-id" {
		t.Fatalf("usable metadata/identity was lost: accumulator=%+v ids=%v", accumulator, ids)
	}
	if _, exists := ids["bookinfo"]; exists {
		t.Fatalf("metadata response reintroduced an owned cross-ID: %v", ids)
	}
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

func TestMergeEnrichmentProviderIDsKeepsExistingIDs(t *testing.T) {
	dst := &metadata.MetadataResult{ProviderIDs: map[string]string{"isbn": "9780306406157"}}
	src := &metadata.MetadataResult{ProviderIDs: map[string]string{"isbn": "new", "openlibrary": "OL1M", "empty": ""}}

	mergeEnrichmentProviderIDs(dst, src)

	if got := dst.ProviderIDs["isbn"]; got != "9780306406157" {
		t.Fatalf("isbn = %q, want original", got)
	}
	if got := dst.ProviderIDs["openlibrary"]; got != "OL1M" {
		t.Fatalf("openlibrary = %q, want OL1M", got)
	}
	if _, exists := dst.ProviderIDs["empty"]; exists {
		t.Fatal("empty provider ID should not be merged")
	}
}

func TestFilterEbookProviderIDsDropsAsinAliases(t *testing.T) {
	got := filterEbookProviderIDs(map[string]string{
		"ASIN":           "B00TEST",
		"audibleASIN":    "B00AUDIO",
		"audible-asin":   "B00AUDIO2",
		"audible_asin":   "B00AUDIO3",
		" ISBN ":         " 9780306406157 ",
		" OpenLibraryID": " OL1M ",
	})

	if got["asin"] != "" || got["audibleasin"] != "" || got["audible-asin"] != "" || got["audible_asin"] != "" {
		t.Fatalf("ASIN aliases should be filtered, got %+v", got)
	}
	if got["isbn"] != "9780306406157" || got["openlibraryid"] != "OL1M" {
		t.Fatalf("filtered IDs = %+v, want ISBN and OpenLibraryID only", got)
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

// A locally complete ebook is the one terminal outcome that consults no
// provider, so it is also the one path that used to skip the status
// promotion entirely. Without it the item keeps media_items.status =
// 'pending' forever and the library page reports a fully described book as
// unmatched.
func TestEnrichClaimedItemPromotesLocallyCompleteItem(t *testing.T) {
	var promoted []string
	e := &Enricher{
		markMatchedLocallyFn: func(_ context.Context, contentID string) error {
			promoted = append(promoted, contentID)
			return nil
		},
	}

	outcome, err := e.enrichClaimedItem(context.Background(), locallyCompleteEbookRow())
	if err != nil {
		t.Fatalf("enrichClaimedItem() error = %v", err)
	}
	if outcome != EnrichmentOutcomeSuccess {
		t.Fatalf("outcome = %q, want %q", outcome, EnrichmentOutcomeSuccess)
	}
	if len(promoted) != 1 || promoted[0] != "ebook-local-1" {
		t.Fatalf("promoted content ids = %v, want [ebook-local-1]", promoted)
	}
}

func locallyCompleteEbookRow() enrichmentItemRow {
	return enrichmentItemRow{
		ContentID:  "ebook-local-1",
		FolderID:   7,
		Title:      "A Book",
		Author:     "An Author",
		Overview:   "A useful description.",
		PosterPath: "/library/A Book/cover.jpg",
		Status:     "pending",
	}
}

func TestCleanEbookSearchTitle(t *testing.T) {
	cases := []struct {
		title, author, want string
	}{
		{"Exit Strategy_ The Murderbot Di - Martha Wells", "Martha Wells", "Exit Strategy The Murderbot Di"},
		{"LTB.067_-_Micky_Maus_Superstar", "", "LTB.067 - Micky Maus Superstar"},
		{"Club Dark Lace_ Complete Dark Lace", "", "Club Dark Lace Complete Dark Lace"},
		{"All of Us - A. F. Carter", "a. f. carter", "All of Us"},
		// A " - <token>" that is not the trailing author must be preserved.
		{"Alice - Bob and Carol", "Bob", "Alice - Bob and Carol"},
		{"Plain Title", "Some Author", "Plain Title"},
		{"  spaced   out  ", "", "spaced out"},
		// Series/volume markers are dropped: they are retail furniture that no
		// provider catalog indexes, so carrying them into the query matched
		// nothing. Volumes are told apart after the search instead, by
		// metadata.BestMatch scoring against the raw title.
		{"Just One Night (The Raven Brothers Book 4)", "", "Just One Night"},
		{"Mistborn (The Mistborn Saga #1)", "", "Mistborn"},
		// Two volumes of one series now issue the same query. That is the point:
		// the query finds the work, and the volume check rejects the wrong book.
		{"The Wheel of Time (Book 1)", "", "The Wheel of Time"},
		{"The Wheel of Time (Book 2)", "", "The Wheel of Time"},
		// Stacked markers peel rather than leaving a stray group behind.
		{"Just One Night (The Raven Brothers Book 4) (2019)", "", "Just One Night"},
		// A marker word without volume syntax is a title, not furniture: "book"
		// alone must not condemn the parenthetical.
		{"Markus Zusak Reader (The Book Thief)", "", "Markus Zusak Reader (The Book Thief)"},
		{"Anthology (Complete Series)", "", "Anthology (Complete Series)"},
		{"White Out [Badlands Thriller]", "", "White Out [Badlands Thriller]"},
		{"Salem's Lot (2019)", "", "Salem's Lot"},
		{"Title (A Story of 1969)", "", "Title (A Story of 1969)"},
		{"The Hobbit (Illustrated)", "", "The Hobbit (Illustrated)"},
		// Retail edition suffixes are furniture too: providers index the work,
		// not the storefront's packaging of it.
		{"White Fang (AmazonClassics Edition)", "", "White Fang"},
		{"Treasure Island (AmazonClassics Edition)", "", "Treasure Island"},
		{"Frankenstein (Penguin Classics)", "", "Frankenstein"},
		{"The Shell Game (Kindle Single)", "", "The Shell Game"},
		{"Ordinary Grace (A Novel)", "", "Ordinary Grace"},
		// ...but only as a suffix category, not a keyword hunt: an edition
		// word mid-parenthetical is a title.
		{"Rules (First Edition Club)", "", "Rules (First Edition Club)"},
		{"Exit Strategy_ Murderbot Di - Martha Wells (Book 4)", "Martha Wells", "Exit Strategy Murderbot Di"},
	}
	for _, tc := range cases {
		if got := cleanEbookSearchTitle(tc.title, tc.author); got != tc.want {
			t.Errorf("cleanEbookSearchTitle(%q,%q)=%q want %q", tc.title, tc.author, got, tc.want)
		}
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

func (f *fakeEbookProviderIDRepository) ReplaceByContentID(context.Context, string, map[string]string) error {
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

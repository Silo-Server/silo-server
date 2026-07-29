package ebooks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestEbookContentType(t *testing.T) {
	if got := ebookContentType(); got != "ebook" {
		t.Fatalf("ebookContentType() = %q, want ebook", got)
	}
}

func TestNewEnricherPreservesNilProviderIDRepository(t *testing.T) {
	e := NewEnricher(nil, nil, nil, nil, nil, nil)
	if e.providerIDs != nil {
		t.Fatal("providerIDs is a non-nil typed interface for a nil repository")
	}
}

func TestFilterEbookPeopleKeepsAuthorsOnly(t *testing.T) {
	people := []models.ItemPerson{
		{Person: models.Person{Name: "Author One"}, Kind: models.PersonKindAuthor, SortOrder: 7},
		{Person: models.Person{Name: "Narrator One"}, Kind: models.PersonKindNarrator, SortOrder: 8},
		{Person: models.Person{Name: "Writer One"}, Kind: models.PersonKindWriter, SortOrder: 9},
		{Person: models.Person{Name: "Author Two"}, Kind: models.PersonKindAuthor, SortOrder: 10},
	}

	got := filterEbookPeople(people)

	if len(got) != 2 {
		t.Fatalf("filtered people len = %d, want 2: %+v", len(got), got)
	}
	for i, p := range got {
		if p.Kind != models.PersonKindAuthor {
			t.Fatalf("filtered[%d].Kind = %v, want author", i, p.Kind)
		}
		if p.SortOrder != i {
			t.Fatalf("filtered[%d].SortOrder = %d, want %d", i, p.SortOrder, i)
		}
	}
	if got[0].Person.Name != "Author One" || got[1].Person.Name != "Author Two" {
		t.Fatalf("filtered author order = %+v", got)
	}
}

func TestEbookEnrichWorkersFromEnv(t *testing.T) {
	t.Setenv("SILO_EBOOK_ENRICH_WORKERS", "12")
	if got := ebookEnrichWorkers(); got != 12 {
		t.Fatalf("ebookEnrichWorkers() = %d, want 12", got)
	}

	t.Setenv("SILO_EBOOK_ENRICH_WORKERS", "0")
	if got := ebookEnrichWorkers(); got != defaultEnrichWorkers {
		t.Fatalf("ebookEnrichWorkers() with zero = %d, want default %d", got, defaultEnrichWorkers)
	}

	t.Setenv("SILO_EBOOK_ENRICH_WORKERS", "5000")
	if got := ebookEnrichWorkers(); got != 5000 {
		t.Fatalf("ebookEnrichWorkers() = %d, want 5000", got)
	}

	t.Setenv("SILO_EBOOK_ENRICH_WORKERS", "9999")
	if got := ebookEnrichWorkers(); got != maxEnrichWorkers {
		t.Fatalf("ebookEnrichWorkers() capped = %d, want %d", got, maxEnrichWorkers)
	}
}

func TestEnricherRunFansOut(t *testing.T) {
	const wantWorkers = 4
	const itemCount = 16

	items := make([]enrichmentItemRow, itemCount)
	for i := range items {
		items[i] = enrichmentItemRow{ContentID: "test", Title: "t"}
	}

	var inFlight int32
	var maxInFlight int32
	var signaled int32
	var wg sync.WaitGroup
	wg.Add(wantWorkers)
	gate := make(chan struct{})

	enrich := func(ctx context.Context, item enrichmentItemRow) error {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			prev := atomic.LoadInt32(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, cur) {
				break
			}
		}
		if atomic.AddInt32(&signaled, 1) <= wantWorkers {
			wg.Done()
		}
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
		}
		return nil
	}

	go func() {
		wg.Wait()
		close(gate)
	}()

	e := &Enricher{workers: wantWorkers, batchSize: itemCount}
	e.runBatch(context.Background(), items, enrich, nil)

	if got := atomic.LoadInt32(&maxInFlight); got < wantWorkers {
		t.Errorf("max in-flight = %d, want >= %d", got, wantWorkers)
	}
}

func TestRunBatchRecordsFailuresForFailedItemsOnly(t *testing.T) {
	items := []enrichmentItemRow{
		{ContentID: "ok-1"},
		{ContentID: "bad-1"},
		{ContentID: "ok-2"},
		{ContentID: "bad-2"},
	}

	enrich := func(_ context.Context, item enrichmentItemRow) error {
		if strings.HasPrefix(item.ContentID, "bad") {
			return errors.New("provider exploded")
		}
		return nil
	}

	var mu sync.Mutex
	var recorded []string
	record := func(_ context.Context, item enrichmentItemRow) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, item.ContentID)
	}

	e := &Enricher{workers: 2, batchSize: len(items)}
	enriched := e.runBatch(context.Background(), items, enrich, record)

	if enriched != 2 {
		t.Fatalf("enriched = %d, want 2", enriched)
	}
	sort.Strings(recorded)
	if strings.Join(recorded, ",") != "bad-1,bad-2" {
		t.Fatalf("recorded failures = %v, want exactly the failing items", recorded)
	}
}

func TestRunBatchSkipsFailureRecordingOnCancellation(t *testing.T) {
	items := []enrichmentItemRow{{ContentID: "cancelled-1"}}

	enrich := func(context.Context, enrichmentItemRow) error {
		return fmt.Errorf("search aborted: %w", context.Canceled)
	}

	var recorded int32
	record := func(context.Context, enrichmentItemRow) {
		atomic.AddInt32(&recorded, 1)
	}

	e := &Enricher{workers: 1, batchSize: len(items)}
	if enriched := e.runBatch(context.Background(), items, enrich, record); enriched != 0 {
		t.Fatalf("enriched = %d, want 0", enriched)
	}
	if got := atomic.LoadInt32(&recorded); got != 0 {
		t.Fatalf("failure recordings = %d, want 0: cancellation must not count against the cap", got)
	}
}

func TestEnrichmentQueriesKeepEbookAndAudiobookMetadataSeparate(t *testing.T) {
	if !strings.Contains(enqueueEnrichmentJobQuery, "mi.type = 'ebook'") {
		t.Fatalf("enqueue query must target ebooks:\n%s", enqueueEnrichmentJobQuery)
	}
	if !strings.Contains(enqueueEnrichmentJobQuery, "NOT EXISTS") ||
		!strings.Contains(enqueueEnrichmentJobQuery, "manga_chapters") ||
		!strings.Contains(enqueueEnrichmentJobQuery, "chapter_content_id") {
		t.Fatalf("enqueue query must exclude manga chapters:\n%s", enqueueEnrichmentJobQuery)
	}
	if strings.Contains(loadEnrichmentItemsQuery, "narrator") ||
		strings.Contains(loadEnrichmentItemsQuery, "asin") {
		t.Fatalf("ebook load query must not introduce audiobook-only fields:\n%s", loadEnrichmentItemsQuery)
	}
	if !strings.Contains(loadEnrichmentItemsQuery, "ip.kind = 7") {
		t.Fatalf("ebook load query must load author credits only:\n%s", loadEnrichmentItemsQuery)
	}
	for _, field := range []string{"locked_fields", "backdrop_path", "logo_path"} {
		if !strings.Contains(loadEnrichmentItemsQuery, field) {
			t.Fatalf("ebook load query must load %s for protection decisions:\n%s", field, loadEnrichmentItemsQuery)
		}
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
	e := &Enricher{
		queue:     queue,
		batchSize: len(items),
		workers:   2,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return items, nil
		},
		enrichClaimedItemFn: func(_ context.Context, item enrichmentItemRow) (EnrichmentOutcome, error) {
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
		},
	}

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

func TestEnricherRunCountsRateLimitedFailureAsDeferred(t *testing.T) {
	const retryAfter = 45 * time.Minute
	limited, err := status.New(codes.ResourceExhausted, "provider quota exhausted").WithDetails(
		&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)},
	)
	if err != nil {
		t.Fatalf("attach retry info: %v", err)
	}
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{{ContentID: "rate-limited", Token: "token", Attempts: 1}},
	}
	e := &Enricher{
		queue:     queue,
		batchSize: 1,
		workers:   1,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return []enrichmentItemRow{{ContentID: "rate-limited"}}, nil
		},
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			return "", limited.Err()
		},
	}

	result, runErr := e.Run(context.Background(), EnrichmentScopeIncremental)
	if runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	if result.Failed != 0 || result.Deferred != 1 {
		t.Fatalf("Run() result = %+v, want one deferred and no failures", result)
	}
	if got := queue.failed["rate-limited"]; got != EnrichmentErrorRateLimited {
		t.Fatalf("failure class = %q, want rate_limited", got)
	}
	if got := queue.retryAfter["rate-limited"]; got != retryAfter {
		t.Fatalf("retry after = %s, want %s", got, retryAfter)
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
	e := &Enricher{
		queue:     queue,
		batchSize: len(items),
		workers:   1,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return items, nil
		},
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			cancel()
			return "", context.Canceled
		},
	}

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
	e := &Enricher{
		queue:     queue,
		batchSize: 1,
		workers:   1,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return []enrichmentItemRow{{ContentID: "success"}}, nil
		},
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			return EnrichmentOutcomeSuccess, nil
		},
	}

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

func TestEnricherRunDiscardsClaimedRowsThatAreNoLongerEligible(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{
			{ContentID: "became-manga", Token: "manga-token", Attempts: 1},
			{ContentID: "folderless", Token: "folderless-token", Attempts: 1},
		},
	}
	e := &Enricher{
		queue:     queue,
		batchSize: 2,
		workers:   1,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return []enrichmentItemRow{{ContentID: "folderless", FolderID: 0}}, nil
		},
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			return EnrichmentOutcomeSkipped, nil
		},
	}

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

func TestEnricherRunCountsUpstreamCancellationAsTransientFailure(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{{ContentID: "cancel-happy", Token: "cancel-token", Attempts: 1}},
	}
	e := &Enricher{
		queue:     queue,
		batchSize: 1,
		workers:   1,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return []enrichmentItemRow{{ContentID: "cancel-happy"}}, nil
		},
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			return "", context.Canceled
		},
	}

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

func TestEnricherRunBoundsEachItemBelowTheLease(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs: []EnrichmentJob{{ContentID: "slow", Token: "slow-token", Attempts: 1}},
	}
	e := &Enricher{
		queue:       queue,
		batchSize:   1,
		workers:     1,
		itemTimeout: 20 * time.Millisecond,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return []enrichmentItemRow{{ContentID: "slow"}}, nil
		},
		enrichClaimedItemFn: func(ctx context.Context, _ enrichmentItemRow) (EnrichmentOutcome, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}

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

func TestEnricherRunClaimsAtMostOneJobPerWorker(t *testing.T) {
	queue := &fakeEnrichmentQueue{}
	e := &Enricher{
		queue:     queue,
		batchSize: 50,
		workers:   4,
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			return EnrichmentOutcomeSuccess, nil
		},
	}

	if _, err := e.Run(context.Background(), EnrichmentScopeIncremental); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if queue.claimLimit != 4 {
		t.Fatalf("claim limit = %d, want worker count 4", queue.claimLimit)
	}
	if queue.leaseDuration != defaultEnrichmentLease {
		t.Fatalf("lease duration = %s, want %s", queue.leaseDuration, defaultEnrichmentLease)
	}
}

func TestEnricherRunInjectsExactClaimOwnershipCheck(t *testing.T) {
	queue := &fakeEnrichmentQueue{
		jobs:          []EnrichmentJob{{ContentID: "lost", Token: "exact-token", Attempts: 1}},
		claimCheckErr: ErrEnrichmentLeaseLost,
		failErr:       ErrEnrichmentLeaseLost,
	}
	e := &Enricher{
		queue:     queue,
		batchSize: 1,
		workers:   1,
		loadClaimedItemsFn: func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error) {
			return []enrichmentItemRow{{ContentID: "lost"}}, nil
		},
		enrichClaimedItemFn: func(ctx context.Context, _ enrichmentItemRow) (EnrichmentOutcome, error) {
			return "", requireEnrichmentClaim(ctx)
		},
	}

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

func TestEnrichItemSkipsItemWithoutLibraryFolder(t *testing.T) {
	// Membership rows are inserted after the item upsert, so a scan-window
	// race can claim an item before its folder link exists. The item must be
	// skipped (retried next sweep), never stamped or counted as a failure.
	e := &Enricher{}
	err := e.enrichItem(context.Background(), enrichmentItemRow{ContentID: "no-folder", Title: "t"})
	if !errors.Is(err, errEnrichmentSkipped) {
		t.Fatalf("enrichItem error = %v, want errEnrichmentSkipped", err)
	}
}

func TestEnrichWithProvidersSkipsWhenNoProvidersConfigured(t *testing.T) {
	e := &Enricher{}
	err := e.enrichWithProviders(context.Background(), enrichmentItemRow{ContentID: "c1", FolderID: 7}, nil)
	if !errors.Is(err, errEnrichmentSkipped) {
		t.Fatalf("enrichWithProviders error = %v, want errEnrichmentSkipped", err)
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

func TestEnrichWithProvidersReturnsFailureWhenAllProvidersError(t *testing.T) {
	providerErr := errors.New("provider exploded")
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{slug: "p1", searchErr: providerErr, getErr: providerErr},
		&fakeEbookMetadataProvider{slug: "p2", searchErr: providerErr, getErr: providerErr},
	}

	e := &Enricher{}
	err := e.enrichWithProviders(context.Background(), enrichmentItemRow{ContentID: "c1", FolderID: 7, Title: "t"}, providers)
	if err == nil {
		t.Fatal("enrichWithProviders = nil, want error so the failure cap engages instead of stamping")
	}
	if errors.Is(err, errEnrichmentSkipped) {
		t.Fatalf("enrichWithProviders error = %v, want a recordable failure, not a skip", err)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("enrichWithProviders error = %v, want wrapped provider error", err)
	}
}

func TestEnrichWithProvidersStampsWhenProvidersAnswerWithNoMatch(t *testing.T) {
	// Providers reachable, genuinely nothing found: nil means the no-match
	// path ran and the item was stamped so it is not re-claimed every sweep.
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{slug: "p1"},
	}

	e := &Enricher{}
	err := e.enrichWithProviders(context.Background(), enrichmentItemRow{ContentID: "c1", FolderID: 7, Title: "t"}, providers)
	if err != nil {
		t.Fatalf("enrichWithProviders = %v, want nil for a genuine no-match", err)
	}
}

func TestEnrichWithProvidersReturnsContextErrorOverProviderFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{slug: "p1", searchErr: ctx.Err(), getErr: ctx.Err()},
	}

	e := &Enricher{}
	err := e.enrichWithProviders(ctx, enrichmentItemRow{ContentID: "c1", FolderID: 7}, providers)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("enrichWithProviders error = %v, want context.Canceled so cancellation never counts against the cap", err)
	}
}

func TestCollectEbookMetadataAccumulatesProviderErrors(t *testing.T) {
	searchErr := errors.New("search down")
	getErr := errors.New("metadata down")
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{slug: "broken", searchErr: searchErr, getErr: getErr},
		&fakeEbookMetadataProvider{
			slug:    "working",
			results: []metadata.SearchResult{{ProviderIDs: map[string]string{"openlibrary": "OL1M"}}},
			result:  &metadata.MetadataResult{HasMetadata: true, Overview: "found"},
		},
	}

	accumulator, ids, errs := collectEbookMetadata(context.Background(), enrichmentItemRow{ContentID: "c1", Title: "t"}, providers, nil)

	if len(errs) != 2 || !errors.Is(errs[0], searchErr) || !errors.Is(errs[1], getErr) {
		t.Fatalf("provider errors = %v, want both broken-provider errors", errs)
	}
	if accumulator.Overview != "found" {
		t.Fatalf("accumulator overview = %q, want metadata from the working provider", accumulator.Overview)
	}
	if !accumulator.HasMetadata {
		t.Fatal("accumulator HasMetadata = false after a provider returned metadata")
	}
	if ids["openlibrary"] != "OL1M" {
		t.Fatalf("accumulated IDs = %v, want search-result openlibrary ID", ids)
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
			results: []metadata.SearchResult{{ProviderIDs: map[string]string{"bookinfo": "40817436"}}},
			result:  &metadata.MetadataResult{HasMetadata: true, Overview: "book one"},
		},
	}
	owner := &fakeProviderIDOwner{ownerByID: map[string]string{"40817436": "other-book"}}

	_, ids, errs := collectEbookMetadata(context.Background(), enrichmentItemRow{ContentID: "c2", Title: "t"}, providers, owner)

	if len(errs) != 0 {
		t.Fatalf("unexpected provider errors: %v", errs)
	}
	if _, ok := ids["bookinfo"]; ok {
		t.Fatalf("provider id owned by another item was claimed: %v", ids)
	}
}

func TestCollectEbookMetadataSurfacesOwnershipCheckError(t *testing.T) {
	checkErr := errors.New("db down")
	providers := []metadata.Provider{
		&fakeEbookMetadataProvider{
			slug:    "bookinfo",
			results: []metadata.SearchResult{{ProviderIDs: map[string]string{"bookinfo": "40817436"}}},
		},
	}
	owner := &fakeProviderIDOwner{err: checkErr}

	_, ids, errs := collectEbookMetadata(context.Background(), enrichmentItemRow{ContentID: "c2", Title: "t"}, providers, owner)

	if len(errs) != 1 || !errors.Is(errs[0], checkErr) {
		t.Fatalf("provider errors = %v, want the ownership-check error", errs)
	}
	if _, ok := ids["bookinfo"]; ok {
		t.Fatalf("provider id claimed despite failed ownership check: %v", ids)
	}
}

func TestRunBatchDoesNotRecordFailuresForSkippedItems(t *testing.T) {
	items := []enrichmentItemRow{{ContentID: "skipped-1"}}

	enrich := func(context.Context, enrichmentItemRow) error {
		return fmt.Errorf("%w: no providers", errEnrichmentSkipped)
	}

	var recorded int32
	record := func(context.Context, enrichmentItemRow) {
		atomic.AddInt32(&recorded, 1)
	}

	e := &Enricher{workers: 1, batchSize: len(items)}
	if enriched := e.runBatch(context.Background(), items, enrich, record); enriched != 0 {
		t.Fatalf("enriched = %d, want 0", enriched)
	}
	if got := atomic.LoadInt32(&recorded); got != 0 {
		t.Fatalf("failure recordings = %d, want 0: skips must not count against the cap", got)
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

func TestCacheRemotePosterCachesProviderURL(t *testing.T) {
	cacher := &fakeEbookImageCacher{}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "https://example.test/book.jpg",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 1 {
		t.Fatalf("CacheImage calls = %d, want 1", cacher.calls)
	}
	if cacher.req.ProviderID != ebookMetadataImageProviderID {
		t.Fatalf("ProviderID = %q, want %q", cacher.req.ProviderID, ebookMetadataImageProviderID)
	}
	if cacher.req.ContentType != "ebooks" || cacher.req.ContentID != "content-1" {
		t.Fatalf("cache target = %q/%q", cacher.req.ContentType, cacher.req.ContentID)
	}
	if result.PosterPath != "ebook-metadata/ebooks/content-1/poster/original.webp" {
		t.Fatalf("PosterPath = %q", result.PosterPath)
	}
	if result.PosterThumbhash != "thumb" {
		t.Fatalf("PosterThumbhash = %q", result.PosterThumbhash)
	}
}

func TestCacheRemotePosterSkipsNilCacher(t *testing.T) {
	e := &Enricher{}
	result := &metadata.MetadataResult{
		PosterPath: "https://example.test/book.jpg",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if result.PosterPath != "https://example.test/book.jpg" {
		t.Fatalf("PosterPath = %q, want provider URL preserved", result.PosterPath)
	}
}

func TestCacheRemotePosterSkipsTypedNilCacher(t *testing.T) {
	var cacher *fakeEbookImageCacher
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "https://example.test/book.jpg",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if result.PosterPath != "https://example.test/book.jpg" {
		t.Fatalf("PosterPath = %q, want provider URL preserved", result.PosterPath)
	}
}

func TestCacheRemotePosterSkipsAlreadyCachedPath(t *testing.T) {
	cacher := &fakeEbookImageCacher{}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "local/ebooks/content-1/poster/original.webp",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 0 {
		t.Fatalf("CacheImage calls = %d, want 0", cacher.calls)
	}
	if result.PosterPath != "local/ebooks/content-1/poster/original.webp" {
		t.Fatalf("PosterPath = %q", result.PosterPath)
	}
}

func TestCacheRemotePosterPreservesProviderURLOnCacheError(t *testing.T) {
	cacher := &fakeEbookImageCacher{err: errors.New("cache failed")}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "https://example.test/book.jpg",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 1 {
		t.Fatalf("CacheImage calls = %d, want 1", cacher.calls)
	}
	if result.PosterPath != "https://example.test/book.jpg" {
		t.Fatalf("PosterPath = %q, want provider URL preserved", result.PosterPath)
	}
}

func TestCacheRemotePosterPreservesProviderURLOnNilCacheResult(t *testing.T) {
	cacher := &fakeEbookImageCacher{returnNil: true}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "https://example.test/book.jpg",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 1 {
		t.Fatalf("CacheImage calls = %d, want 1", cacher.calls)
	}
	if result.PosterPath != "https://example.test/book.jpg" {
		t.Fatalf("PosterPath = %q, want provider URL preserved", result.PosterPath)
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

func TestMergeEnrichmentProviderIDsDropsAsinIDs(t *testing.T) {
	dst := &metadata.MetadataResult{ProviderIDs: map[string]string{"isbn": "9780306406157"}}
	src := &metadata.MetadataResult{
		ProviderIDs: map[string]string{
			"ASIN":         "B00TEST",
			"audible_asin": "B00AUDIO",
			"openlibrary":  "OL1M",
		},
	}

	mergeEnrichmentProviderIDs(dst, src)

	if _, exists := dst.ProviderIDs["ASIN"]; exists {
		t.Fatal("ASIN provider ID should not be merged for ebooks")
	}
	if _, exists := dst.ProviderIDs["audible_asin"]; exists {
		t.Fatal("audible_asin provider ID should not be merged for ebooks")
	}
	if got := dst.ProviderIDs["openlibrary"]; got != "OL1M" {
		t.Fatalf("openlibrary = %q, want OL1M", got)
	}
}

func TestBuildEbookSearchQueryUsesFilteredScannerISBN(t *testing.T) {
	item := enrichmentItemRow{
		Title:    "Tagged Ebook",
		Year:     2024,
		Language: "en",
		ProviderIDs: map[string]string{
			" ISBN ":       " 9780306406157 ",
			"ASIN":         "B00TEST",
			"audible_asin": "B00AUDIO",
		},
	}

	query, ids := buildEbookSearchQuery(item)

	if query.Title != "Tagged Ebook" || query.Year != 2024 || query.Language != "en" || query.ContentType != "ebook" {
		t.Fatalf("query basics = %+v", query)
	}
	if got := query.ProviderIDs["isbn"]; got != "9780306406157" {
		t.Fatalf("query isbn = %q, want scanner ISBN", got)
	}
	if _, exists := query.ProviderIDs["ASIN"]; exists {
		t.Fatal("query should not include ASIN for ebooks")
	}
	if got := ids["isbn"]; got != "9780306406157" {
		t.Fatalf("accumulated isbn = %q, want scanner ISBN", got)
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

func TestBuildEbookMetadataRequestCarriesAccumulatedISBN(t *testing.T) {
	req := buildEbookMetadataRequest(map[string]string{
		" ISBN ":      " 9780306406157 ",
		"openlibrary": "OL1M",
	}, "fr")

	if req.ContentType != "ebook" || req.Language != "fr" {
		t.Fatalf("request basics = %+v", req)
	}
	if got := req.ProviderIDs["isbn"]; got != "9780306406157" {
		t.Fatalf("request isbn = %q, want normalized key/value", got)
	}
	if got := req.ProviderIDs["openlibrary"]; got != "OL1M" {
		t.Fatalf("request openlibrary = %q, want OL1M", got)
	}
}

type fakeEbookProviderIDRepository struct {
	rows  map[string][]*models.MediaItemProviderID
	err   error
	calls [][]string
}

func (f *fakeEbookProviderIDRepository) GetByContentIDs(
	_ context.Context,
	contentIDs []string,
) (map[string][]*models.MediaItemProviderID, error) {
	f.calls = append(f.calls, append([]string(nil), contentIDs...))
	return f.rows, f.err
}

func (f *fakeEbookProviderIDRepository) ReplaceByContentID(context.Context, string, map[string]string) error {
	return nil
}

func (f *fakeEbookProviderIDRepository) FindContentIDByProviderIDs(
	context.Context,
	map[string]string,
	string,
	string,
) (string, error) {
	return "", nil
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

func TestPreserveEbookMetadataAllowsProviderOwnedRefreshReplacement(t *testing.T) {
	result := &metadata.MetadataResult{
		HasMetadata: true,
		Year:        2022,
		Overview:    "Corrected remote description",
	}

	preserveEbookLocalMetadata(enrichmentItemRow{
		Status: "matched",
	}, result)

	if result.Year != 2022 || result.Overview != "Corrected remote description" {
		t.Fatalf("controlled refresh fields were suppressed: %+v", result)
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

func TestPreserveEbookLocalPosterAllowsProviderOwnedReplacement(t *testing.T) {
	for _, current := range []string{
		"",
		"https://provider.test/old.jpg",
		"ebook-metadata/ebooks/book/poster/original.webp",
	} {
		result := &metadata.MetadataResult{
			HasMetadata:     true,
			PosterPath:      "https://provider.test/new.jpg",
			PosterThumbhash: "new-thumb",
		}

		preserveEbookLocalMetadata(enrichmentItemRow{
			Status:     "matched",
			PosterPath: current,
		}, result)

		if result.PosterPath == "" || result.PosterThumbhash == "" {
			t.Fatalf("provider-owned poster %q was not replaceable: %+v", current, result)
		}
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

type fakeEbookImageCacher struct {
	calls     int
	req       metadata.CacheImageRequest
	err       error
	returnNil bool
}

func (f *fakeEbookImageCacher) CacheImage(_ context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	f.calls++
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	if f.returnNil {
		return nil, nil
	}
	return &metadata.CacheImageResult{
		BasePath:  req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster",
		Thumbhash: "thumb",
		Ext:       ".webp",
	}, nil
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

func TestEnricherRunLimitedEnforcesClaimLimitBelowWorkerCount(t *testing.T) {
	queue := &fakeEnrichmentQueue{}
	e := &Enricher{
		queue:     queue,
		batchSize: 50,
		workers:   50,
		enrichClaimedItemFn: func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error) {
			return EnrichmentOutcomeSuccess, nil
		},
	}

	if _, err := e.RunLimited(context.Background(), EnrichmentScopeLegacy, 3); err != nil {
		t.Fatalf("RunLimited() error = %v", err)
	}
	if queue.claimLimit != 3 {
		t.Fatalf("claim limit = %d, want hard caller cap 3", queue.claimLimit)
	}
	if queue.readyCountCalls != 0 {
		t.Fatalf("per-batch exact ready counts = %d, want 0", queue.readyCountCalls)
	}
	if queue.hasReadyCalls != 1 {
		t.Fatalf("bounded has-ready checks = %d, want 1", queue.hasReadyCalls)
	}
}

func TestEbookHasCompleteLocalMetadata(t *testing.T) {
	complete := enrichmentItemRow{
		Title:      "A Book",
		Author:     "An Author",
		Overview:   "A useful description.",
		PosterPath: "/library/A Book/cover.jpg",
	}
	if !ebookHasCompleteLocalMetadata(complete) {
		t.Fatal("complete embedded metadata was not recognized")
	}
	if missing := ebookMissingMetadataFields(complete); len(missing) != 0 {
		t.Fatalf("complete embedded metadata missing fields = %v", missing)
	}

	for name, tc := range map[string]struct {
		field  string
		mutate func(*enrichmentItemRow)
	}{
		"title":       {field: "title", mutate: func(item *enrichmentItemRow) { item.Title = "" }},
		"author":      {field: "author", mutate: func(item *enrichmentItemRow) { item.Author = "" }},
		"description": {field: "description", mutate: func(item *enrichmentItemRow) { item.Overview = "" }},
		"cover":       {field: "cover", mutate: func(item *enrichmentItemRow) { item.PosterPath = "" }},
	} {
		t.Run(name, func(t *testing.T) {
			item := complete
			tc.mutate(&item)
			if ebookHasCompleteLocalMetadata(item) {
				t.Fatalf("metadata missing %s was treated as complete", name)
			}
			missing := ebookMissingMetadataFields(item)
			if len(missing) != 1 || missing[0] != tc.field {
				t.Fatalf("missing fields = %v, want [%s]", missing, tc.field)
			}
		})
	}
}

func (f *fakeEnrichmentQueue) recordTransition(job EnrichmentJob) {
	if f.transitionedJobs == nil {
		f.transitionedJobs = make(map[string]EnrichmentJob)
	}
	f.transitionedJobs[job.ContentID] = job
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
		// Series/volume markers are kept (unwrapped) so distinct volumes search
		// distinctly instead of collapsing onto one provider work.
		{"Just One Night (The Raven Brothers Book 4)", "", "Just One Night The Raven Brothers Book 4"},
		{"Mistborn (The Mistborn Saga #1)", "", "Mistborn The Mistborn Saga #1"},
		{"The Wheel of Time (Book 1)", "", "The Wheel of Time Book 1"},
		{"The Wheel of Time (Book 2)", "", "The Wheel of Time Book 2"},
		{"White Out [Badlands Thriller]", "", "White Out [Badlands Thriller]"},
		{"Salem's Lot (2019)", "", "Salem's Lot"},
		{"The Hobbit (Illustrated)", "", "The Hobbit (Illustrated)"},
		{"Exit Strategy_ Murderbot Di - Martha Wells (Book 4)", "Martha Wells", "Exit Strategy Murderbot Di"},
	}
	for _, tc := range cases {
		if got := cleanEbookSearchTitle(tc.title, tc.author); got != tc.want {
			t.Errorf("cleanEbookSearchTitle(%q,%q)=%q want %q", tc.title, tc.author, got, tc.want)
		}
	}
}

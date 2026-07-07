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
)

func TestEbookContentType(t *testing.T) {
	if got := ebookContentType(); got != "ebook" {
		t.Fatalf("ebookContentType() = %q, want ebook", got)
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

	t.Setenv("SILO_EBOOK_ENRICH_WORKERS", "999")
	if got := ebookEnrichWorkers(); got != defaultEnrichBatchSize {
		t.Fatalf("ebookEnrichWorkers() capped = %d, want %d", got, defaultEnrichBatchSize)
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

func TestClaimBatchQueryAppliesFailureBackoffAndCap(t *testing.T) {
	// Pin the starvation guard: failing items are deprioritized, not retried
	// at the head of every sweep, and capped items are never re-claimed.
	if !strings.Contains(claimBatchQuery, "LEFT JOIN ebook_enrichment_state ees ON ees.content_id = mi.content_id") {
		t.Fatalf("claimBatchQuery must read dedicated ebook enrichment failure state:\n%s", claimBatchQuery)
	}
	if !strings.Contains(claimBatchQuery, "COALESCE(ees.failures, 0) < $2") {
		t.Fatalf("claimBatchQuery must exclude items at the failure cap:\n%s", claimBatchQuery)
	}
	if !strings.Contains(claimBatchQuery, "ORDER BY COALESCE(ees.failures, 0) ASC, mi.created_at ASC") {
		t.Fatalf("claimBatchQuery must claim least-failed items first:\n%s", claimBatchQuery)
	}
	if strings.Contains(claimBatchQuery, "refresh_failures") {
		t.Fatalf("claimBatchQuery must not read media_items.refresh_failures (owned by the metadata refresh-debt system):\n%s", claimBatchQuery)
	}
	if enrichFailureCap < 1 {
		t.Fatalf("enrichFailureCap = %d, want >= 1", enrichFailureCap)
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

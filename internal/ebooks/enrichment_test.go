package ebooks

import (
	"context"
	"errors"
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
	e.runBatch(context.Background(), items, enrich)

	if got := atomic.LoadInt32(&maxInFlight); got < wantWorkers {
		t.Errorf("max in-flight = %d, want >= %d", got, wantWorkers)
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

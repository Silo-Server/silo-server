package audiobooks

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// TestEnricherRunFansOut verifies that runBatch processes a claimed batch with
// multiple workers in flight at once. The test installs a fake enrichItem path
// that blocks until N concurrent calls are observed; if runBatch is still
// serial, the test will time out and fail the max-in-flight assertion.
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
		// First wantWorkers callers release the gate; later ones don't block.
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

func TestNewEnricherUsesConfiguredBatchSize(t *testing.T) {
	t.Setenv("SILO_AUDIOBOOK_ENRICH_BATCH_SIZE", "123")
	t.Setenv("SILO_AUDIOBOOK_ENRICH_WORKERS", "8")

	e := NewEnricher(nil, nil, nil, nil, nil, nil)

	if e.batchSize != 123 {
		t.Fatalf("batchSize = %d, want 123", e.batchSize)
	}
	if e.workers != 8 {
		t.Fatalf("workers = %d, want 8", e.workers)
	}
}

func TestAudiobookEnrichWorkersCapsAtBatchSize(t *testing.T) {
	t.Setenv("SILO_AUDIOBOOK_ENRICH_WORKERS", "8")

	if got := audiobookEnrichWorkers(3); got != 3 {
		t.Fatalf("audiobookEnrichWorkers(3) = %d, want 3", got)
	}
}

func TestAudiobookEnrichBatchSizeIgnoresInvalidEnv(t *testing.T) {
	t.Setenv("SILO_AUDIOBOOK_ENRICH_BATCH_SIZE", "nope")

	if got := audiobookEnrichBatchSize(); got != defaultEnrichBatchSize {
		t.Fatalf("audiobookEnrichBatchSize() = %d, want %d", got, defaultEnrichBatchSize)
	}
}

func TestCacheRemotePosterCachesProviderURL(t *testing.T) {
	cacher := &fakeAudiobookImageCacher{}
	e := &Enricher{imageCacher: cacher}
	result := &metadata.MetadataResult{
		PosterPath: "https://m.media-amazon.com/images/I/example.jpg",
	}

	e.cacheRemotePoster(context.Background(), "content-1", result)

	if cacher.calls != 1 {
		t.Fatalf("CacheImage calls = %d, want 1", cacher.calls)
	}
	if cacher.req.ProviderID != audiobookMetadataImageProviderID {
		t.Fatalf("ProviderID = %q, want %q", cacher.req.ProviderID, audiobookMetadataImageProviderID)
	}
	if cacher.req.ContentType != "audiobooks" || cacher.req.ContentID != "content-1" {
		t.Fatalf("cache target = %q/%q", cacher.req.ContentType, cacher.req.ContentID)
	}
	if result.PosterPath != "audiobook-metadata/audiobooks/content-1/poster/original.webp" {
		t.Fatalf("PosterPath = %q", result.PosterPath)
	}
	if result.PosterThumbhash != "thumb" {
		t.Fatalf("PosterThumbhash = %q", result.PosterThumbhash)
	}
}

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

func (f *fakeAudiobookImageCacher) CacheAudiobookCover(context.Context, []byte, string) (string, string, string, error) {
	return "", "", "", nil
}

func (f *fakeAudiobookImageCacher) CacheImage(_ context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	f.calls++
	f.req = req
	return &metadata.CacheImageResult{
		BasePath:  req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster",
		Thumbhash: "thumb",
		Ext:       ".webp",
	}, nil
}

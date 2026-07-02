package jellycompat

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// equivalencePageContent is a ContentService double that serves a fixed mixed
// (movie + episode) browse page plus full item details. It can be toggled to
// fail the batched GetItemDetailsByIDs call so the handler falls back to the
// per-item GetItemDetail path, letting tests assert the two paths produce
// identical output.
type equivalencePageContent struct {
	stubContentService
	page         []upstreamListItem
	details      map[string]*upstreamItemDetail
	failBatch    bool
	batchCalls   int
	perItemCalls int
}

func (c *equivalencePageContent) BrowseItems(context.Context, *Session, url.Values) (*upstreamBrowseResponse, error) {
	items := make([]upstreamListItem, len(c.page))
	copy(items, c.page)
	return &upstreamBrowseResponse{Items: items, Total: len(items)}, nil
}

func (c *equivalencePageContent) GetItemDetail(_ context.Context, _ *Session, id string, _ *int) (*upstreamItemDetail, error) {
	c.perItemCalls++
	d, ok := c.details[id]
	if !ok {
		return nil, &HTTPError{StatusCode: 404, Message: "not found"}
	}
	cp := *d
	return &cp, nil
}

func (c *equivalencePageContent) GetItemDetailsByIDs(_ context.Context, _ *Session, ids []string, _ *int) (map[string]*upstreamItemDetail, error) {
	c.batchCalls++
	if c.failBatch {
		return nil, &HTTPError{StatusCode: 500, Message: "batch unavailable"}
	}
	out := make(map[string]*upstreamItemDetail, len(ids))
	for _, id := range ids {
		if d, ok := c.details[id]; ok {
			cp := *d
			out[id] = &cp
		}
	}
	return out, nil
}

func newEquivalenceContent(failBatch bool) *equivalencePageContent {
	movie := &upstreamItemDetail{
		ContentID: "movie-1",
		Type:      "movie",
		Title:     "Test Movie",
		Versions: []catalog.FileVersion{{
			FileID:    101,
			Container: "mkv",
			Duration:  6000,
			AddedAt:   time.Unix(1700000000, 0).UTC(),
		}},
		Cast: []catalog.CastCredit{{Name: "Actor One", Character: "Hero", PersonID: "1"}},
	}
	episode := &upstreamItemDetail{
		ContentID:     "episode-1",
		Type:          "episode",
		Title:         "Test Episode",
		SeriesID:      "series-1",
		SeasonNumber:  intPtr(1),
		EpisodeNumber: intPtr(2),
		Versions: []catalog.FileVersion{{
			FileID:    202,
			Container: "mp4",
			Duration:  1800,
			AddedAt:   time.Unix(1700000100, 0).UTC(),
		}},
	}
	return &equivalencePageContent{
		failBatch: failBatch,
		page: []upstreamListItem{
			{ContentID: "movie-1", Type: "movie", Title: "Test Movie", Status: "matched"},
			{ContentID: "episode-1", Type: "episode", Title: "Test Episode", Status: "matched", SeriesID: "series-1", SeasonNumber: intPtr(1), EpisodeNumber: intPtr(2)},
		},
		details: map[string]*upstreamItemDetail{
			"movie-1":   movie,
			"episode-1": episode,
		},
	}
}

// TestHandleBrowseItems_BatchDetailMatchesPerItem pins the batched detail-field
// path: when the client requests detail-level Fields (MediaSources, etc.) over a
// mixed movie+episode page, the response must be byte-for-byte identical whether
// the details came from the batched GetItemDetailsByIDs path or the per-item
// GetItemDetail fallback — and must carry the load-bearing MediaSources.
func TestHandleBrowseItems_BatchDetailMatchesPerItem(t *testing.T) {
	codec := NewResourceIDCodec()
	query := itemsQuery{
		needsDetailFields: true,
		requestedFields: map[string]bool{
			"mediasources": true,
			"mediastreams": true,
			"people":       true,
			"chapters":     true,
		},
	}

	run := func(failBatch bool) (string, *equivalencePageContent) {
		content := newEquivalenceContent(failBatch)
		h := &ItemsHandler{
			content:  content,
			userData: &mockUserDataService{},
			codec:    codec,
			mapper:   newMapper(codec, &config.Config{}),
			images:   NewImageCache(time.Hour, time.Now),
		}
		req := httptest.NewRequest("GET", "/Users/test/Items?Fields=MediaSources", nil)
		ctx := context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.handleBrowseItems(rec, req, &Session{StreamAppUserID: 1, ProfileID: "profile-1"}, query)
		if rec.Code != 200 {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String(), content
	}

	batchBody, batchContent := run(false)
	perItemBody, perItemContent := run(true)

	if batchBody != perItemBody {
		t.Fatalf("batched and per-item detail output differ:\nbatch:   %s\nperItem: %s", batchBody, perItemBody)
	}

	// The batch run must use the batched path once and never fall back to
	// per-item fetches; the failBatch run must attempt the batch once and only
	// then fall back to per-item fetches. Asserting batchCalls==1 on the
	// fallback run guards against a regression that skips GetItemDetailsByIDs
	// entirely and goes straight to per-item lookups (which would still produce
	// identical output but silently lose the batched fast path).
	if batchContent.batchCalls != 1 || batchContent.perItemCalls != 0 {
		t.Fatalf("batch run: batchCalls=%d perItemCalls=%d; want 1 and 0", batchContent.batchCalls, batchContent.perItemCalls)
	}
	if perItemContent.batchCalls != 1 || perItemContent.perItemCalls != 2 {
		t.Fatalf("fallback run: batchCalls=%d perItemCalls=%d; want 1 and 2 (batch attempted once, then one per-item fetch per page item)", perItemContent.batchCalls, perItemContent.perItemCalls)
	}

	// Sanity: the detail fields must actually be present (regression guard for
	// dropping MediaSources from the batched path).
	if !strings.Contains(batchBody, "\"MediaSources\"") {
		t.Fatalf("batched output missing MediaSources: %s", batchBody)
	}
}

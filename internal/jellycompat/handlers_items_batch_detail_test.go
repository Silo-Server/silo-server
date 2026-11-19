package jellycompat

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

func (c *equivalencePageContent) GetItemDetailsByIDs(_ context.Context, _ *Session, ids []string, _ *int, _ detailBatchOptions) (map[string]*upstreamItemDetail, error) {
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

// TestBatchListItemDetailsSkipsPerItemFallbackAfterCancel: once the request is
// canceled, a failed batch is not retried one item at a time on the same
// context. TestHandleBrowseItems_BatchDetailMatchesPerItem covers the
// fallback on a live request.
func TestBatchListItemDetailsSkipsPerItemFallbackAfterCancel(t *testing.T) {
	content := newEquivalenceContent(true)
	h := &ItemsHandler{content: content}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	details := h.batchListItemDetails(ctx, &Session{StreamAppUserID: 1, ProfileID: "profile-1"}, []string{"movie-1", "episode-1"}, nil, nil)
	if content.batchCalls != 1 || content.perItemCalls != 0 || len(details) != 0 {
		t.Fatalf("batchCalls=%d perItemCalls=%d details=%d after cancel; want 1, 0 and 0", content.batchCalls, content.perItemCalls, len(details))
	}
}

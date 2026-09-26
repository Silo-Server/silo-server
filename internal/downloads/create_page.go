package downloads

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type ManagedCreateExpectation struct {
	ID       string
	Revision int
}

type CreatePage struct {
	Items   []*Download
	BatchID string
	Skipped []SkippedDownload
	Next    *catalog.EpisodePagePosition
}

// CreateSeriesPage uses the original series/season permission, quality, file
// selection, quota and registration lifecycle with bounded catalog selection.
// The client's batch identity joins newly registered pages. Reused managed rows
// retain their existing batch/bytes unless their revision is explicitly supplied.
func (s *Service) CreateSeriesPage(ctx context.Context, userID int, req CreateRequest, season *int, after *catalog.EpisodePagePosition, limit int, filter catalog.AccessFilter) (CreatePage, error) {
	pager, ok := s.episodeRepo.(EpisodePageResolver)
	if !ok {
		return CreatePage{}, ErrSubscriptionsUnavailable
	}
	if req.BatchID == "" || limit < 1 || limit > 100 || (season != nil && (*season < 0 || *season > maxSeasonNumber)) {
		return CreatePage{}, fmt.Errorf("batch identity and a page limit of 1 to 100 are required")
	}
	if req.ExpectedEntries == nil {
		req.ExpectedEntries = map[string]ManagedCreateExpectation{}
	}
	var next *catalog.EpisodePagePosition
	rows, batch, skipped, err := s.createSeriesScoped(ctx, userID, req, filter, func(ctx context.Context) ([]*models.Episode, error) {
		episodes, more, err := pager.ListDownloadEpisodesPage(ctx, req.ContentID, season, after, limit)
		if err == nil && more && len(episodes) > 0 {
			last := episodes[len(episodes)-1]
			next = &catalog.EpisodePagePosition{SeasonNumber: last.SeasonNumber, EpisodeNumber: last.EpisodeNumber, ContentID: last.ContentID}
		}
		return episodes, err
	})
	return CreatePage{Items: rows, BatchID: batch, Skipped: skipped, Next: next}, err
}

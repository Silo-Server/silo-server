package sections

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ListOverlaySummaries returns each card's best accessible file badges without
// transferring every episode's track metadata for series cards.
func (f *Fetcher) ListOverlaySummaries(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	return catalog.ListOverlaySummaries(ctx, f.pool, contentIDs, filter)
}

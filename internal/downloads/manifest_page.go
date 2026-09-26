package downloads

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type ManifestPage struct {
	Items   []*OfflineManifest
	Skipped []SkippedManifest
	Next    *RegistryPosition
}

// PageBatchManifests bounds registry selection before building any manifests.
// Skipped rows still advance the cursor, including a page with no visible items.
func (s *Service) PageBatchManifests(ctx context.Context, userID int, profileID, deviceID, batchID string, after *RegistryPosition, limit int, filter catalog.AccessFilter) (ManifestPage, error) {
	if limit < 1 || limit > 10 || batchID == "" {
		return ManifestPage{}, fmt.Errorf("manifest page requires a batch and a limit of 1 to 10")
	}
	if _, err := s.enabledConfig(ctx); err != nil {
		return ManifestPage{}, err
	}
	if profileID == "" || deviceID == "" {
		return ManifestPage{}, ErrProfileRequired
	}
	if s.manifest == nil {
		return ManifestPage{}, ErrManifestUnavailable
	}
	rows, err := s.repo.listRegistryPage(ctx, userID, profileID, deviceID, batchID, after, limit+1)
	if err != nil {
		return ManifestPage{}, err
	}
	if len(rows) == 0 && after == nil {
		return ManifestPage{}, ErrNotFound
	}
	page := ManifestPage{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.Next = &RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	page.Items, page.Skipped, err = s.buildBatchManifestRows(ctx, rows, filter)
	return page, err
}

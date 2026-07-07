package downloads

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// mapManifestSource serves per-content-id details and counts lookups, so tests
// can assert both access-denial handling and the per-batch series cache.
type mapManifestSource struct {
	details map[string]*catalog.ItemDetail
	calls   map[string]int
}

func (m *mapManifestSource) GetItemDetail(_ context.Context, contentID string, _ catalog.AccessFilter) (*catalog.ItemDetail, error) {
	m.calls[contentID]++
	if d, ok := m.details[contentID]; ok {
		return d, nil
	}
	return nil, catalog.ErrItemNotFound
}

// TestBuildBatchManifestsSkipsAndCachesSeries pins two batch behaviors: one
// bad entry (deleted/access-filtered item) is reported in skipped[] instead of
// failing the whole batch, and the shared series detail is resolved once for
// the batch rather than once per episode.
func TestBuildBatchManifestsSkipsAndCachesSeries(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)

	batchID := fmt.Sprintf("batch-%d", time.Now().UnixNano())
	now := time.Now()
	mkRow := func(n int, episodeID string) string {
		id := fmt.Sprintf("dl-batch-%d-%d", n, now.UnixNano())
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA,
			MediaFileID: f.fileID, ContentID: f.contentID, EpisodeID: episodeID,
			BatchID: batchID, Kind: KindQueued, Status: StatusReady,
			Format: FormatOriginal, FileSize: 1024, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create row %d: %v", n, err)
		}
		return id
	}
	ep1 := mkRow(1, "ep-c1")
	ep2 := mkRow(2, "ep-c2")
	gone := mkRow(3, "ep-gone")

	src := &mapManifestSource{
		details: map[string]*catalog.ItemDetail{
			"ep-c1":    {Type: "episode", Title: "E1", SeriesID: "series-1"},
			"ep-c2":    {Type: "episode", Title: "E2", SeriesID: "series-1"},
			"series-1": {Type: "series", Title: "The Show"},
		},
		calls: map[string]int{},
	}
	svc := NewService(f.repo, nil, nil, nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetOfflineDeps(src, nil, nil)

	manifests, skipped, err := svc.BuildBatchManifests(ctx, f.userID, f.profileA, f.deviceA, batchID, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("BuildBatchManifests: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("manifests = %d, want 2", len(manifests))
	}
	got := map[string]bool{manifests[0].DownloadID: true, manifests[1].DownloadID: true}
	if !got[ep1] || !got[ep2] {
		t.Fatalf("manifest ids = %v, want %s and %s", got, ep1, ep2)
	}
	if len(skipped) != 1 || skipped[0].DownloadID != gone || skipped[0].Reason != "not_found" {
		t.Fatalf("skipped = %+v, want [{%s not_found}]", skipped, gone)
	}
	if calls := src.calls["series-1"]; calls != 1 {
		t.Fatalf("series detail resolved %d times for one batch, want 1", calls)
	}
}

package recommendations

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Because You Watched rows come one per anchor, most recent first, up to the
// number asked for. An anchor whose row filters empty gives way to the next,
// and each row names its anchor.
func TestGetBecauseYouWatchedRowsReadsSeveralAnchors(t *testing.T) {
	store := anchorStore("first", "second", "third")
	store.progress = append(store.progress, userstore.WatchProgress{ProfileID: "p1", MediaItemID: "seen", Completed: true, UpdatedAt: "2026-08-01T00:00:00Z"})
	refresher := &countingReadRefresher{}
	r := &Reader{
		repo: &fakeReaderRepo{personal: map[string][]ScoredItem{
			RecTypeBecauseWatched + "|first":  {{MediaItemID: "seen"}},
			RecTypeBecauseWatched + "|second": scoredRun("second", 5),
			RecTypeBecauseWatched + "|third":  scoredRun("third", 5),
		}},
		refresh: refresher,
		signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: store}).WithUserStoreOutsidePostgres(true),
	}
	ctx := t.Context()

	rows, err := r.GetBecauseYouWatchedRows(ctx, 7, "p1", 2, 3, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].AnchorItemID != "second" || rows[1].AnchorItemID != "third" {
		t.Fatalf("rows = %+v, want the second and third anchors", rows)
	}
	for _, row := range rows {
		if row.Type != RecTypeBecauseWatched || !slices.Equal(scoredIDs(row.Items), scoredIDs(scoredRun(row.AnchorItemID, 3))) {
			t.Fatalf("row %s = %s %v, want its first 3 items", row.AnchorItemID, row.Type, scoredIDs(row.Items))
		}
	}

	if rows, err := r.GetBecauseYouWatchedRows(ctx, 7, "p1", 1, 20, catalog.AccessFilter{}); err != nil || len(rows) != 1 || rows[0].AnchorItemID != "second" {
		t.Fatalf("one row = %+v, %v; want the second anchor's", rows, err)
	}
	if rows, err := r.GetBecauseYouWatchedRows(ctx, 7, "p1", 0, 20, catalog.AccessFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("no rows asked = %+v, %v", rows, err)
	}
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none: every anchor read had a cached row", refresher.calls)
	}
}

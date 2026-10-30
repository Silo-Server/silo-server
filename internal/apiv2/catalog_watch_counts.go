package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// Section catalog-watch-counts: the viewer's episode watch counts for a batch
// of series, for an unwatched-episode badge on series cards. Cards carry only
// the played flag, which can stop at a series' first unwatched episode; exact
// counts read every available episode of every series, so they cost far more
// and a page of cards does not pay for them. A client that shows the badge asks
// for the series it draws.

const opQueryCatalogWatchCounts = "queryCatalogWatchCounts"

// CatalogWatchCountsInput is the queryCatalogWatchCounts request.
type CatalogWatchCountsInput struct {
	Body CatalogWatchCountsQuery
}

// CatalogWatchCountsQuery is the queryCatalogWatchCounts body. The 250-series
// bound matches the cap jellycompat puts on its own per-page series rollup.
type CatalogWatchCountsQuery struct {
	SeriesIDs []ID `json:"series_ids" minItems:"1" maxItems:"250" doc:"Series to count, by catalog identifier; a repeated ID counts once"`
}

// CatalogWatchCount is one series' episode watch counts.
type CatalogWatchCount struct {
	SeriesID ID          `json:"series_id" doc:"The series" example:"series:severance"`
	UserData WatchRollup `json:"user_data" doc:"The viewer's progress over the series' available episodes, as item detail reports it: watched_count, unplayed_count (in-progress episodes included), in_progress_count and played"`
}

// CatalogWatchCountCollection is the queryCatalogWatchCounts response.
type CatalogWatchCountCollection struct {
	Collection[CatalogWatchCount]
}

// CatalogWatchCountsOutput is the queryCatalogWatchCounts response.
type CatalogWatchCountsOutput struct {
	Body CatalogWatchCountCollection
}

func registerCatalogWatchCounts(reg *Registry) {
	op := humaOp(http.MethodPost, Prefix+"/catalog/watch-counts", opQueryCatalogWatchCounts, "catalog",
		"The acting profile's episode watch counts for up to 250 series, in request order, for unwatched-episode badges on series cards. An ID that is not a series, that the viewer may not see, or whose series has no available episodes is left out rather than answered with zero counts.")
	op.DefaultStatus = http.StatusOK
	operation := viewerOperation(op)
	operation.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, operation, reg.queryCatalogWatchCounts)
}

func (reg *Registry) queryCatalogWatchCounts(ctx context.Context, in *CatalogWatchCountsInput) (*CatalogWatchCountsOutput, error) {
	svc, p := reg.catalogItems()
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, imagesize.Request{}, "", "")
	if p != nil {
		return nil, p
	}
	seen := make(map[ID]bool, len(in.Body.SeriesIDs))
	ids := make([]string, 0, len(in.Body.SeriesIDs))
	for _, id := range in.Body.SeriesIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, string(id))
		}
	}
	counts, err := svc.SeriesWatchCounts(ctx, viewer, ids)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]CatalogWatchCount, 0, len(counts))
	for _, id := range ids {
		if rollup := watchRollupOf(counts[id]); rollup != nil {
			items = append(items, CatalogWatchCount{SeriesID: ID(id), UserData: *rollup})
		}
	}
	return &CatalogWatchCountsOutput{Body: CatalogWatchCountCollection{Collection: NewCollection(items)}}, nil
}

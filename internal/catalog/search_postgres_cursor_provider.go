package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const postgresCursorConfig = "postgres-relevance-v1"

func (p *PostgresSearchProvider) searchCursorPage(ctx context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error) {
	options := SearchCursorOptions{Definition: req.Definition, GroupByWork: req.GroupByWork}
	var after *SearchCursor
	if cursor := req.Continuation; cursor != nil {
		if cursor.Provider != SearchProviderPostgres || cursor.Config != postgresCursorConfig {
			return nil, ErrCatalogCursorChanged
		}
		after = cursor.Postgres
	}
	if req.Seek != nil {
		var err error
		after, err = p.itemRepo.SeekSearchCursor(ctx, req.Query, req.ItemTypes, *req.Seek, after, req.Access, options)
		if errors.Is(err, pgx.ErrNoRows) {
			return &CatalogSearchResult{Provider: SearchProviderPostgres, Mode: searchModeKeyword, CursorScope: req.Continuation}, nil
		}
		if err != nil {
			return nil, err
		}
	}
	page, err := p.itemRepo.SearchCursorPage(ctx, req.Query, req.ItemTypes, req.Limit, after, req.Access, !req.SkipTotal, options)
	if err != nil {
		return nil, err
	}
	wrap := func(cursor *SearchCursor) *CatalogSearchCursor {
		if cursor == nil {
			return nil
		}
		return &CatalogSearchCursor{Provider: SearchProviderPostgres, Config: postgresCursorConfig, Postgres: cursor}
	}
	return &CatalogSearchResult{Items: page.Items, Total: page.Total, TotalExact: page.TotalExact,
		HasMore: page.HasMore, Next: wrap(page.Next), CursorScope: wrap(page.Scope), Provider: SearchProviderPostgres, Mode: searchModeKeyword}, nil
}

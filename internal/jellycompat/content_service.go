package jellycompat

import (
	"context"
	"net/url"
)

// ContentService provides catalog data for the compat layer.
type ContentService interface {
	ListUserLibraries(ctx context.Context, session *Session) ([]upstreamUserLibrary, error)
	BrowseItems(ctx context.Context, session *Session, params url.Values) (*upstreamBrowseResponse, error)
	SearchItems(ctx context.Context, session *Session, opts SearchItemsOptions) (*upstreamBrowseResponse, error)
	GetItemDetail(ctx context.Context, session *Session, contentID string, libraryID *int) (*upstreamItemDetail, error)
	// GetItemDetailsByIDs is the batched form of GetItemDetail for a page of
	// content IDs. The returned map is keyed by content ID; ids that cannot be
	// resolved to a detail (excluded media type, access-filtered, not found) are
	// absent so callers fall back to list-level rendering, exactly as they do on
	// a per-item GetItemDetail error.
	GetItemDetailsByIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]*upstreamItemDetail, error)
	ListSeasons(ctx context.Context, session *Session, seriesID string, libraryID *int) ([]upstreamSeason, error)
	GetSeason(ctx context.Context, session *Session, seriesID string, seasonNumber int, libraryID *int) (*upstreamSeason, error)
	ListEpisodes(ctx context.Context, session *Session, seriesID string, seasonNumber int, libraryID *int) ([]upstreamEpisode, error)
	ListEpisodesBySeasonID(ctx context.Context, session *Session, seasonID string, libraryID *int) ([]upstreamEpisode, error)
	ListItemFilters(ctx context.Context, session *Session, params url.Values) (*upstreamItemFiltersResponse, error)
	// EnrichSeriesUserData fills the aggregated Played/UnplayedItemCount rollup
	// for series rows in a list response, in place. A series never has a progress
	// row of its own, so this rollup is the only source of that state. Callers
	// pass the same session as the request so the counts are scoped to the
	// caller's profile. Rows that are not series, or that already carry UserData,
	// are left untouched.
	EnrichSeriesUserData(ctx context.Context, session *Session, items []upstreamListItem)
}

type SearchItemsOptions struct {
	Query     string
	ItemTypes []string
	Limit     int
	Offset    int
	LibraryID *int
	SkipTotal bool
}

// UserDataService provides favorites/progress/watched operations.
type UserDataService interface {
	ListFavorites(ctx context.Context, session *Session, limit, offset int) ([]upstreamListItem, error)
	ListFavoritesByMediaItems(ctx context.Context, session *Session, mediaItemIDs []string) (map[string]bool, error)
	IsFavorite(ctx context.Context, session *Session, contentID string) (bool, error)
	AddFavorite(ctx context.Context, session *Session, contentID string) error
	RemoveFavorite(ctx context.Context, session *Session, contentID string) error
	ListProgress(ctx context.Context, session *Session, status string, limit, offset int) ([]upstreamProgress, error)
	// ListProgressFiltered narrows a progress status page to the requested item
	// types and/or library, pushing the predicate into SQL so the watched-items
	// path no longer scans the profile's entire completed set. Callers still
	// apply access/parental exclusions over the hydrated rows.
	ListProgressFiltered(ctx context.Context, session *Session, status string, types []string, libraryID *int, limit, offset int) ([]upstreamProgress, error)
	// FilterResumeProgress drops in-progress entries that Continue Watching
	// surfaces should hide: entries the user dismissed from the row and
	// episodes superseded by a later-completed episode in the same series.
	// It mirrors the first-party sections fetcher so both surfaces agree.
	FilterResumeProgress(ctx context.Context, session *Session, entries []upstreamProgress) ([]upstreamProgress, error)
	ListProgressByMediaItems(ctx context.Context, session *Session, mediaItemIDs []string) (map[string]*upstreamProgress, error)
	GetProgress(ctx context.Context, session *Session, contentID string) (*upstreamProgress, error)
	MarkPlayed(ctx context.Context, session *Session, contentID string) error
	MarkPlayedBatch(ctx context.Context, session *Session, contentIDs []string) error
	MarkUnplayed(ctx context.Context, session *Session, contentID string) error
	MarkUnplayedBatch(ctx context.Context, session *Session, contentIDs []string) error
}

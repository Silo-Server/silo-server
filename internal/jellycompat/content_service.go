package jellycompat

import (
	"context"
	"net/url"
)

// ContentService provides catalog data for the compat layer.
type ContentService interface {
	ListUserLibraries(ctx context.Context, session *Session) ([]upstreamUserLibrary, error)
	BrowseItems(ctx context.Context, session *Session, params url.Values) (*upstreamBrowseResponse, error)
	SearchItems(ctx context.Context, session *Session, query string, itemTypes []string, limit, offset int, libraryID *int) (*upstreamBrowseResponse, error)
	GetItemDetail(ctx context.Context, session *Session, contentID string, libraryID *int) (*upstreamItemDetail, error)
	ListSeasons(ctx context.Context, session *Session, seriesID string, libraryID *int) ([]upstreamSeason, error)
	GetSeason(ctx context.Context, session *Session, seriesID string, seasonNumber int, libraryID *int) (*upstreamSeason, error)
	ListEpisodes(ctx context.Context, session *Session, seriesID string, seasonNumber int, libraryID *int) ([]upstreamEpisode, error)
	ListEpisodesBySeasonID(ctx context.Context, session *Session, seasonID string, libraryID *int) ([]upstreamEpisode, error)
	ListItemFilters(ctx context.Context, session *Session, params url.Values) (*upstreamItemFiltersResponse, error)
}

// UserDataService provides favorites/progress/watched operations.
type UserDataService interface {
	ListFavorites(ctx context.Context, session *Session, limit, offset int) ([]upstreamListItem, error)
	ListFavoritesByMediaItems(ctx context.Context, session *Session, mediaItemIDs []string) (map[string]bool, error)
	IsFavorite(ctx context.Context, session *Session, contentID string) (bool, error)
	AddFavorite(ctx context.Context, session *Session, contentID string) error
	RemoveFavorite(ctx context.Context, session *Session, contentID string) error
	ListProgress(ctx context.Context, session *Session, status string, limit, offset int) ([]upstreamProgress, error)
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

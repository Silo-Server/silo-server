package userstore

import (
	"context"
	"errors"
	"time"
)

// JellycompatProgressEdit is one explicit leaf edit. History is the play an
// explicit played mark records, added only when the leaf is not already
// completed; ClearWatched implements an explicit unplayed mark, which keeps the
// item's history listed.
// Progress, history, and an optional favorite mutation commit or roll back together.
type JellycompatProgressEdit struct {
	MediaItemID                      string
	PositionSeconds, DurationSeconds float64
	Completed                        bool
	EventAt                          time.Time
	History                          *WatchHistoryEntry
	ClearWatched                     bool
	IsFavorite                       *bool
}

type JellycompatProgressEditor interface {
	ApplyJellycompatProgress(context.Context, string, JellycompatProgressEdit) error
}

// JellycompatPlayedByDateLister lists a profile's completed progress rows in
// play-date order (event_at, the date Jellyfin clients see as LastPlayedDate),
// oldest first when ascending, with ties broken by media item ID in the same
// direction. Like ListProgressFiltered, it hides rows hidden from history and
// may pre-filter by item type and library; a store without catalog tables
// ignores types and libraryID and leaves that to the caller.
type JellycompatPlayedByDateLister interface {
	ListJellycompatPlayedByDate(ctx context.Context, profileID string, types []string, libraryID *int, ascending bool, limit, offset int) ([]WatchProgress, error)
}

// ErrJellycompatPlayedByDateUnsupported reports a store that cannot list
// completed progress by play date.
var ErrJellycompatPlayedByDateUnsupported = errors.New("listing played progress by date is unsupported")

// ListJellycompatPlayedByDate lists store's completed progress in play-date
// order, or returns ErrJellycompatPlayedByDateUnsupported when store has no
// such listing.
func ListJellycompatPlayedByDate(ctx context.Context, store UserStore, profileID string, types []string, libraryID *int, ascending bool, limit, offset int) ([]WatchProgress, error) {
	lister, ok := store.(JellycompatPlayedByDateLister)
	if !ok {
		return nil, ErrJellycompatPlayedByDateUnsupported
	}
	return lister.ListJellycompatPlayedByDate(ctx, profileID, types, libraryID, ascending, limit, offset)
}

// JellycompatParentEdit commits a parent's favorite and its children's watch
// state together. Targets and history use the existing batch-mark semantics.
type JellycompatParentEdit struct {
	MediaItemID string
	IsFavorite  bool
	Played      bool
	Targets     []MarkWatchedTarget
	History     []WatchHistoryEntry
}

type JellycompatParentEditor interface {
	ApplyJellycompatParent(context.Context, string, JellycompatParentEdit) error
}

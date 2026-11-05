package userstore

import "context"

// ItemUserFlags is a profile's list memberships and played state for one item.
type ItemUserFlags struct {
	Favorite      bool
	InWatchlist   bool
	NotInterested bool
	Played        bool
}

// ItemUserFlagStore reads, in one round trip, what ListFavoritesByMediaItems,
// ListWatchlistByMediaItems and ListNotInterestedByMediaItems report for
// mediaItemIDs and whether ListProgressWithCompletedHistory reports each of
// playedIDs completed. Items with no flag set may be absent. ok is false when
// the backing store cannot answer, and the caller makes those reads instead; a
// wrapper forwards the backing store's answer.
type ItemUserFlagStore interface { //nolint:iface // implemented by pgstore, asserted by the item user-state resolver
	ItemUserFlags(ctx context.Context, profileID string, mediaItemIDs, playedIDs []string) (flags map[string]ItemUserFlags, ok bool, err error)
}

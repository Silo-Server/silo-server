package userstore

import "github.com/jackc/pgx/v5"

// ItemViewerStateQuery names the item whose viewer state item detail shows
// and the parts of it to read.
type ItemViewerStateQuery struct {
	ProfileID   string
	MediaItemID string
	// Progress adds the item's watch progress with completed history folded
	// in, as GetProgressWithCompletedHistory returns it.
	Progress bool
	// VersionChoice adds the series playback preference stored under the
	// item's own ID, where playback start records a movie's chosen version.
	VersionChoice bool
	// Marks adds whether the item is a favorite, on the watchlist, or marked
	// not interested.
	Marks bool
	// PlaybackPreferences adds the audio and subtitle preference rows stored
	// under the item's ID.
	PlaybackPreferences bool
}

// ItemViewerState is the viewer's own state for one item. Each field holds
// what the single-item read returns: Progress what
// GetProgressWithCompletedHistory does, VersionChoice what
// GetSeriesPlaybackPreference does, the flags what IsFavorite, InWatchlist
// and GetNotInterested report, and the preferences what GetAudioPreference
// and GetSubtitlePreference return.
type ItemViewerState struct {
	Progress           *WatchProgress
	VersionChoice      *SeriesPlaybackPreference
	IsFavorite         bool
	InWatchlist        bool
	NotInterested      bool
	AudioPreference    *AudioPreference
	SubtitlePreference *SubtitlePreference
}

// ItemViewerStateQueuer is implemented by stores whose state lives beside
// the PostgreSQL catalog (see CatalogSQLProvider). It queues an item's
// viewer-state reads on a caller's batch, so they share the caller's round
// trip: the caller sends the batch on the catalog pool, and closing its
// results without error fills state. It reports false, queuing nothing, when
// the store cannot; a wrapper must forward the backing store's answer.
type ItemViewerStateQueuer interface {
	QueueItemViewerState(b *pgx.Batch, q ItemViewerStateQuery, state *ItemViewerState) bool
}

// QueueItemViewerState queues store's reads for q on b when store keeps its
// state beside the catalog, so b can be sent on the catalog pool. It reports
// false, queuing nothing, otherwise; the caller then reads one at a time.
func QueueItemViewerState(store UserStore, b *pgx.Batch, q ItemViewerStateQuery, state *ItemViewerState) bool {
	queuer, ok := store.(ItemViewerStateQueuer)
	return ok && HasCatalogSQLState(store) && queuer.QueueItemViewerState(b, q, state)
}

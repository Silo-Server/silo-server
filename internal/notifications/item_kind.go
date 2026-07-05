package notifications

// Availability-fact tables for flat item kinds. Movies keep the dedicated
// table they launched with; every later kind shares item_availability, which
// carries a kind discriminator column.
const (
	movieAvailabilityTable = "movie_availability"
	itemAvailabilityTable  = "item_availability"
)

// flatItemKind describes one non-episode release-event kind: a single
// media_items row becomes newly available and is announced broadcast-only
// (server channels), with no per-profile fanout. Movies were the first flat
// kind; audiobooks and ebooks followed. Adding another kind means one entry
// here, one AvailabilityKinds flag wired in the library-ingest executor, one
// notify_new_* channel column, and a migration widening the release_events
// kind CHECK constraints.
type flatItemKind struct {
	// Kind is the release_events.kind / seed-state value, and doubles as the
	// dedupe-key prefix ("audiobook:{library_id}:{item_id}").
	Kind string
	// ItemType is the media_items.type value the kind announces. It doubles
	// as the display noun in copy ("New audiobook available on Silo").
	ItemType string
	// AvailabilityTable receives the one-way "first became available in this
	// library" facts for the kind.
	AvailabilityTable string
	// WantsToggle reads the channel's notify_new_* toggle for the kind.
	WantsToggle func(ServerChannel) bool
	// Selected reads the ingest scope's AvailabilityKinds flag for the kind.
	Selected func(AvailabilityKinds) bool
}

// flatItemKinds is the ordered registry of every flat release-event kind.
// Order affects only fixture/sample ordering.
var flatItemKinds = []flatItemKind{
	{
		Kind:              EventKindMovie,
		ItemType:          mediaTypeMovie,
		AvailabilityTable: movieAvailabilityTable,
		WantsToggle:       func(c ServerChannel) bool { return c.NotifyNewMovies },
		Selected:          func(k AvailabilityKinds) bool { return k.Movies },
	},
	{
		Kind:              EventKindAudiobook,
		ItemType:          mediaTypeAudiobook,
		AvailabilityTable: itemAvailabilityTable,
		WantsToggle:       func(c ServerChannel) bool { return c.NotifyNewAudiobooks },
		Selected:          func(k AvailabilityKinds) bool { return k.Audiobooks },
	},
	{
		Kind:              EventKindEbook,
		ItemType:          mediaTypeEbook,
		AvailabilityTable: itemAvailabilityTable,
		WantsToggle:       func(c ServerChannel) bool { return c.NotifyNewEbooks },
		Selected:          func(k AvailabilityKinds) bool { return k.Ebooks },
	},
}

// flatKindByString looks up a registry entry by event kind. ok is false for
// episode and for unknown kinds (a newer node's kind string reaching an old
// node), so callers skip rather than misrender what they cannot describe.
func flatKindByString(kind string) (flatItemKind, bool) {
	for _, k := range flatItemKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return flatItemKind{}, false
}

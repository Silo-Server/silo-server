package notifications

import (
	"cmp"
	"slices"
	"strings"
)

// EvaluateRecipient applies the eligibility rules for one candidate recipient
// of one release event. Returns the matched reason flags and whether a
// delivery should be created.
//
// Rules (docs/architecture/notifications.md, "Eligibility rules"): a new
// episode notifies only when it becomes the profile's Next Up.
//   - the row must be a Next Up candidate: the series shows in Next Up on
//     Home and no episode of it is partly watched
//   - the episode must be the exact next expected episode; when the catalog
//     knew no unstarted episode after the anchor (an open cursor), the first
//     episode keyed at or past the cursor counts
//   - suppress once last_notified_episode_key reaches the cursor: one
//     notification per Next Up position, so a batch drop of E06-E08 notifies
//     for E06 only
//   - profile preferences are a hard gate
//
// Favorites, the watchlist and Continue Watching never notify for episodes.
func EvaluateRecipient(interest SeriesInterest, prefs Preferences, episodeKey int) (ReasonFlags, bool) {
	flags := ActiveReasons(interest, prefs)
	if !flags.NextUp {
		return ReasonFlags{}, false
	}
	next := *interest.NextExpectedEpisodeKey
	if interest.LastNotifiedEpisodeKey != nil && *interest.LastNotifiedEpisodeKey >= next {
		return ReasonFlags{}, false
	}
	if episodeKey != next && (!interest.NextExpectedOpen || episodeKey < next) {
		return ReasonFlags{}, false
	}
	return flags, true
}

// ActiveReasons is the part of the eligibility rules that does not depend on
// a release event: the interest reasons the profile's preferences let notify.
// A Next Up reason counts only once the row knows the episode it waits for;
// EvaluateRecipient then matches it against each event's episode key.
func ActiveReasons(interest SeriesInterest, prefs Preferences) ReasonFlags {
	if !prefs.Enabled {
		return ReasonFlags{}
	}
	return ReasonFlags{
		NextUp: interest.NextUpCandidate && prefs.NotifyNextUp && interest.NextExpectedEpisodeKey != nil,
	}
}

// PartitionEventsByKind splits claimed events into episode events (which fan
// out to profiles) and everything else (flat item kinds — movies, audiobooks,
// ebooks — which only feed the server-channel broadcast sweep). Order is
// preserved within each partition. The common all-episode batch returns the
// input slice unchanged.
func PartitionEventsByKind(events []ReleaseEvent) (episodes, others []ReleaseEvent) {
	allEpisodes := true
	for _, event := range events {
		if normalizeEventKind(event.Kind) != EventKindEpisode {
			allEpisodes = false
			break
		}
	}
	if allEpisodes {
		return events, nil
	}
	episodes = make([]ReleaseEvent, 0, len(events))
	others = make([]ReleaseEvent, 0)
	for _, event := range events {
		if normalizeEventKind(event.Kind) == EventKindEpisode {
			episodes = append(episodes, event)
		} else {
			others = append(others, event)
		}
	}
	return episodes, others
}

// SortFanoutEvents orders claimed episode events by library, series and
// ascending episode key. Fanout raises last_notified_episode_key as it
// processes each event, so with an open Next Up cursor the lowest arriving
// episode must be evaluated first: it is the one that becomes Next Up.
func SortFanoutEvents(events []ReleaseEvent) {
	slices.SortStableFunc(events, func(a, b ReleaseEvent) int {
		return cmp.Or(
			cmp.Compare(a.LibraryID, b.LibraryID),
			strings.Compare(a.SeriesID, b.SeriesID),
			cmp.Compare(a.EpisodeKey, b.EpisodeKey),
		)
	})
}

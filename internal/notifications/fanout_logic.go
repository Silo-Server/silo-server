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
// Rules (docs/architecture/notifications.md, "Eligibility rules"):
//   - favorite / watchlist / continue_watching notify on any newly available
//     episode of the series
//   - next_up notifies only when the episode is at or beyond the profile's
//     next_expected_episode_key
//   - suppress when last_notified_episode_key >= episode_key
//   - profile preferences are a hard gate: disabled reasons cannot match
func EvaluateRecipient(interest SeriesInterest, prefs Preferences, episodeKey int) (ReasonFlags, bool) {
	if !prefs.Enabled {
		return ReasonFlags{}, false
	}
	if interest.LastNotifiedEpisodeKey != nil && *interest.LastNotifiedEpisodeKey >= episodeKey {
		return ReasonFlags{}, false
	}
	flags := ActiveReasons(interest, prefs)
	flags.NextUp = flags.NextUp && episodeKey >= *interest.NextExpectedEpisodeKey
	return flags, flags.Any()
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
		Favorite:         interest.Favorite && prefs.NotifyFavorites,
		Watchlist:        interest.Watchlist && prefs.NotifyWatchlist,
		ContinueWatching: interest.ContinueWatching && prefs.NotifyContinueWatching,
		NextUp:           interest.NextUpCandidate && prefs.NotifyNextUp && interest.NextExpectedEpisodeKey != nil,
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

// sortForFanout orders episode events so each (library, series) group runs
// in ascending episode_key order. Fanout raises last_notified_episode_key as
// it processes each event inside one transaction, so a higher key processed
// first would make EvaluateRecipient skip every lower-key event of the group.
func sortForFanout(events []ReleaseEvent) {
	slices.SortStableFunc(events, func(a, b ReleaseEvent) int {
		return cmp.Or(
			cmp.Compare(a.LibraryID, b.LibraryID),
			strings.Compare(a.SeriesID, b.SeriesID),
			cmp.Compare(a.EpisodeKey, b.EpisodeKey),
		)
	})
}

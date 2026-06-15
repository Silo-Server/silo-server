package notifications

import (
	"sort"
)

// EvaluateRecipient applies the eligibility rules for one candidate recipient
// of one release event. Returns the matched reason flags and whether a
// delivery should be created.
//
// Rules (docs/superpowers/plans/notifications/01, "Eligibility Rules"):
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
	flags := ReasonFlags{
		Favorite:         interest.Favorite && prefs.NotifyFavorites,
		Watchlist:        interest.Watchlist && prefs.NotifyWatchlist,
		ContinueWatching: interest.ContinueWatching && prefs.NotifyContinueWatching,
		NextUp: interest.NextUpCandidate && prefs.NotifyNextUp &&
			interest.NextExpectedEpisodeKey != nil && episodeKey >= *interest.NextExpectedEpisodeKey,
	}
	return flags, flags.Any()
}

// PartitionEventsByKind splits claimed events into episode events (which fan
// out to profiles) and everything else (movie events, which only feed the
// server-channel broadcast sweep). Order is preserved within each partition.
// The common all-episode batch returns the input slice unchanged.
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

// ApplyBurstCap groups claimed events by (library, series) and bounds fanout
// per group: only the maxPerSeries events with the highest episode_key fan
// out; the rest are suppressed (processed without deliveries). This bounds
// the blast radius of bulk additions (back-catalog season packs) to seeded
// libraries. The cap is per claim batch and therefore approximate across
// batches, which is acceptable.
func ApplyBurstCap(events []ReleaseEvent, maxPerSeries int) (fanout, suppressed []ReleaseEvent) {
	if maxPerSeries <= 0 {
		maxPerSeries = 1
	}
	type groupKey struct {
		libraryID int
		seriesID  string
	}
	groups := make(map[groupKey][]ReleaseEvent)
	order := make([]groupKey, 0)
	for _, event := range events {
		key := groupKey{event.LibraryID, event.SeriesID}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], event)
	}

	fanout = make([]ReleaseEvent, 0, len(events))
	suppressed = make([]ReleaseEvent, 0)
	for _, key := range order {
		group := groups[key]
		sort.Slice(group, func(i, j int) bool {
			return group[i].EpisodeKey > group[j].EpisodeKey
		})
		keep := min(maxPerSeries, len(group))
		suppressed = append(suppressed, group[keep:]...)
		// Emit the kept events in ascending key order: fanout raises
		// last_notified_episode_key as it processes each event, so a higher
		// key processed first would make EvaluateRecipient suppress every
		// remaining lower-key event in the group.
		for i := keep - 1; i >= 0; i-- {
			fanout = append(fanout, group[i])
		}
	}
	return fanout, suppressed
}

package notifications

// EvaluateRecipient applies the eligibility rules for one candidate recipient
// of one episode release event. Returns the matched reason flags and whether a
// delivery should be created.
//
// Rules (docs/architecture/notifications.md, "Eligibility rules"): a profile
// is told about an episode only when it is the next one after what the
// profile has watched.
//   - the master toggle and notify_next_up are on
//   - the interest row is a Next Up candidate with a cursor
//     (next_expected_episode_key), which InterestUpdater.RecomputeSeries
//     keeps; fanout never moves it
//   - the episode is the first listed one at or after the cursor:
//     cursor <= episodeKey, and the catalog lists no episode of the series,
//     with or without a file, with a key in [cursor, episodeKey).
//     highestBelow is the highest listed key below episodeKey, nil for none,
//     so the second half is cursor > highestBelow. A listed episode whose
//     file has not arrived holds its place: E07 landing before E06 stays
//     silent and E06 notifies when it lands. Keys are
//     season * 1_000_000 + episode, so a season premiere never equals the
//     finale's key + 1 and the test cannot be key equality.
//   - last_notified_episode_key is unset or below episodeKey
//
// Mutes are left out by ListActiveBySeries, a series drop or Next Up
// dismissal clears next_up_candidate, and the fanout worker checks access.
func EvaluateRecipient(interest SeriesInterest, prefs Preferences, episodeKey int, highestBelow *int) (ReasonFlags, bool) {
	if !ActiveReasons(interest, prefs).NextUp || !nextUpMatches(interest, episodeKey, highestBelow) {
		return ReasonFlags{}, false
	}
	return ReasonFlags{NextUp: true}, true
}

// nextUpMatches is the part of EvaluateRecipient that reads only the interest
// row and the event, so fanout can drop candidates before it loads their
// preferences.
func nextUpMatches(interest SeriesInterest, episodeKey int, highestBelow *int) bool {
	if !interest.NextUpCandidate || interest.NextExpectedEpisodeKey == nil {
		return false
	}
	cursor := *interest.NextExpectedEpisodeKey
	if cursor > episodeKey || (highestBelow != nil && cursor <= *highestBelow) {
		return false
	}
	return interest.LastNotifiedEpisodeKey == nil || *interest.LastNotifiedEpisodeKey < episodeKey
}

// ActiveReasons is the part of the eligibility rules that does not depend on
// a release event: the interest reasons the profile's preferences let notify.
// A Next Up reason counts only once the row knows the episode it waits for;
// EvaluateRecipient then matches it against each event's episode key, and the
// notified-series list against the episodes already available.
func ActiveReasons(interest SeriesInterest, prefs Preferences) ReasonFlags {
	return ReasonFlags{
		NextUp: prefs.Enabled && prefs.NotifyNextUp && interest.NextUpCandidate && interest.NextExpectedEpisodeKey != nil,
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

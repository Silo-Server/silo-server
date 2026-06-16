package metadata

import (
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	RefreshDebtReasonEpisodeIncomplete      int64 = 1
	RefreshDebtReasonStaleProviderID        int64 = 2
	RefreshDebtReasonRefreshFailure         int64 = 4
	RefreshDebtReasonCoreMetadataIncomplete int64 = 8
	RefreshDebtReasonProviderIDIncomplete   int64 = 16
)

const (
	RefreshTargetItem    = "item"
	RefreshTargetSeason  = "season"
	RefreshTargetEpisode = "episode"
)

func NormalizeRefreshTargetType(targetType string) string {
	switch strings.ToLower(strings.TrimSpace(targetType)) {
	case "", RefreshTargetItem:
		return RefreshTargetItem
	case RefreshTargetSeason:
		return RefreshTargetSeason
	case RefreshTargetEpisode:
		return RefreshTargetEpisode
	default:
		return ""
	}
}

func hasRefreshDebtReason(mask, reason int64) bool {
	return mask&reason != 0
}

const (
	// refreshDebtEpisodeTerminalAttempts is the number of fruitless attempts after
	// which episode-incomplete debt is treated as "the provider has no data" and given
	// up on: demoted off the priority-300 band and re-checked only rarely. We give up
	// fast — with the stepped backoff below (1d, 1d, 3d, ...) attempt 3 lands after
	// ~5 days of trying. This is safe because operators can force an immediate re-check
	// anytime via the library "refresh incomplete" / per-item refresh endpoints, and
	// because media_items.episode_metadata_incomplete is left untouched so those paths
	// still find the item.
	refreshDebtEpisodeTerminalAttempts = 3
	// refreshDebtTerminalPriority sorts terminal debt below the default band (100) so it
	// never front-runs legitimately-due content.
	refreshDebtTerminalPriority = 50
	// refreshDebtTerminalDelay is the rare automatic safety re-check for terminal debt,
	// so late-arriving provider data is eventually picked up without operator action.
	refreshDebtTerminalDelay = 90 * 24 * time.Hour
)

// isTerminalEpisodeDebt reports that an episode-incomplete debt row has exhausted its
// attempts and almost certainly cannot be improved by another provider fetch. The signal
// is purely the persistent attempt_count on the row: resolved rows are deleted, so a row
// that still exists with a high attempt count has been re-processed many times without
// ever completing.
func isTerminalEpisodeDebt(reasonMask int64, attemptCount int) bool {
	return attemptCount >= refreshDebtEpisodeTerminalAttempts &&
		hasRefreshDebtReason(reasonMask, RefreshDebtReasonEpisodeIncomplete)
}

// effectiveRefreshDebtPriority demotes terminal episode-incomplete debt off the priority-300
// band. If the row also carries a still-fixable reason it falls to that reason's band (not
// the floor), so a series with real core/provider-id debt keeps refreshing at the right
// cadence; pure episode-incomplete debt falls to the terminal floor.
func effectiveRefreshDebtPriority(reasonMask int64, attemptCount int) int {
	if isTerminalEpisodeDebt(reasonMask, attemptCount) {
		if demoted := reasonMask &^ RefreshDebtReasonEpisodeIncomplete; demoted != 0 {
			return refreshDebtPriority(demoted)
		}
		return refreshDebtTerminalPriority
	}
	return refreshDebtPriority(reasonMask)
}

func refreshDebtPriority(reasonMask int64) int {
	switch {
	case hasRefreshDebtReason(reasonMask, RefreshDebtReasonEpisodeIncomplete):
		return 300
	case hasRefreshDebtReason(reasonMask, RefreshDebtReasonStaleProviderID):
		return 250
	case hasRefreshDebtReason(reasonMask, RefreshDebtReasonProviderIDIncomplete):
		return 240
	case hasRefreshDebtReason(reasonMask, RefreshDebtReasonRefreshFailure):
		return 200
	case hasRefreshDebtReason(reasonMask, RefreshDebtReasonCoreMetadataIncomplete):
		return 150
	default:
		return 100
	}
}

func nextRefreshDelay(reasonMask int64, attemptCount int) time.Duration {
	// Only pure episode-incomplete debt is parked on the rare terminal cadence. If the row
	// also carries a still-fixable reason, that reason keeps driving the normal backoff (the
	// priority demotion in effectiveRefreshDebtPriority handles the queue ordering).
	if isTerminalEpisodeDebt(reasonMask, attemptCount) &&
		reasonMask&^RefreshDebtReasonEpisodeIncomplete == 0 {
		return refreshDebtTerminalDelay
	}
	if hasRefreshDebtReason(reasonMask, RefreshDebtReasonEpisodeIncomplete) {
		switch {
		case attemptCount <= 1:
			return 24 * time.Hour
		case attemptCount == 2:
			return 3 * 24 * time.Hour
		case attemptCount == 3:
			return 7 * 24 * time.Hour
		case attemptCount == 4:
			return 14 * 24 * time.Hour
		default:
			return 30 * 24 * time.Hour
		}
	}

	switch {
	case attemptCount <= 1:
		return 24 * time.Hour
	case attemptCount == 2:
		return 3 * 24 * time.Hour
	case attemptCount == 3:
		return 7 * 24 * time.Hour
	case attemptCount == 4:
		return 14 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

func nextRefreshAtForDebt(reasonMask int64, attemptCount int, now time.Time) time.Time {
	return now.Add(nextRefreshDelay(reasonMask, attemptCount))
}

// logRefreshDebtTerminal emits a one-time notice when an episode-incomplete debt row first
// crosses into the terminal give-up state, so the demotion is observable in logs rather
// than silent. Logging on the exact transition attempt keeps it to a single line per row.
func logRefreshDebtTerminal(targetType, contentID string, reasonMask int64, attemptCount int) {
	if attemptCount == refreshDebtEpisodeTerminalAttempts &&
		isTerminalEpisodeDebt(reasonMask, attemptCount) {
		slog.Warn("metadata: episode-incomplete refresh debt reached terminal attempts; demoting off top priority",
			"target_type", NormalizeRefreshTargetType(targetType),
			"content_id", contentID,
			"attempt_count", attemptCount,
			"reason_mask", reasonMask,
		)
	}
}

func refreshDebtReasonsForItem(item *models.MediaItem) int64 {
	if item == nil {
		return 0
	}

	var reasonMask int64
	if hasCoreMetadataRefreshDebt(item) {
		reasonMask |= RefreshDebtReasonCoreMetadataIncomplete
	}
	if hasProviderIDRefreshDebt(item) {
		reasonMask |= RefreshDebtReasonProviderIDIncomplete
	}
	if item.RefreshFailures > 0 && strings.EqualFold(strings.TrimSpace(item.Status), "matched") {
		reasonMask |= RefreshDebtReasonRefreshFailure
	}
	return reasonMask
}

func hasProviderIDRefreshDebt(item *models.MediaItem) bool {
	if item == nil || !strings.EqualFold(strings.TrimSpace(item.Status), "matched") {
		return false
	}
	if strings.TrimSpace(item.TmdbID) != "" {
		return false
	}
	return strings.TrimSpace(item.TvdbID) != "" || strings.TrimSpace(item.ImdbID) != ""
}

func hasCoreMetadataRefreshDebt(item *models.MediaItem) bool {
	if item == nil || !strings.EqualFold(strings.TrimSpace(item.Status), "matched") {
		return false
	}
	hasRatings := item.RatingIMDB != nil ||
		item.RatingTMDB != nil ||
		item.RatingRTCritic != nil ||
		item.RatingRTAudience != nil

	return strings.TrimSpace(item.Overview) == "" ||
		strings.TrimSpace(item.PosterPath) == "" ||
		strings.TrimSpace(item.BackdropPath) == "" ||
		!hasRatings
}

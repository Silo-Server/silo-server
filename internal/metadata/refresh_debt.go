package metadata

import (
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

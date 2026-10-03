package metadata

import (
	"regexp"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const EpisodeRecentStillWindow = 45 * 24 * time.Hour

var episodeProvisionalTitlePattern = regexp.MustCompile(`(?i)^(tba|tbd)$`)

func isEpisodeProvisionalTitle(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	return episodeProvisionalTitlePattern.MatchString(title)
}

func episodeHasProviderMatch(ep *models.Episode) bool {
	if ep == nil || strings.EqualFold(strings.TrimSpace(ep.MetadataSource), "scanner_fallback") {
		return false
	}
	return strings.TrimSpace(ep.TmdbID) != "" ||
		strings.TrimSpace(ep.TvdbID) != "" ||
		strings.TrimSpace(ep.ImdbID) != ""
}

// EpisodeHasActionableMetadataDebt returns true when another provider refresh
// can reasonably be expected to improve the episode metadata.
func EpisodeHasActionableMetadataDebt(ep *models.Episode, now time.Time) bool {
	if ep == nil {
		return false
	}
	if strings.TrimSpace(ep.Title) == "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(ep.MetadataSource), "scanner_fallback") {
		return true
	}
	if !episodeHasProviderMatch(ep) {
		return true
	}
	recentOrUndated := ep.AirDate == nil || !ep.AirDate.Before(now.Add(-EpisodeRecentStillWindow))
	// "TBA"/"TBD" is only a likely placeholder while the episode is undated or
	// recent; an older provider-matched episode can genuinely carry that title.
	if recentOrUndated && isEpisodeProvisionalTitle(ep.Title) {
		return true
	}
	if ep.AirDate != nil && recentOrUndated && strings.TrimSpace(ep.StillPath) == "" {
		return true
	}
	return false
}

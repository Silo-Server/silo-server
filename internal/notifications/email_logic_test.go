package notifications

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// emailEpisodeRow builds an episode.available row for one profile.
func emailEpisodeRow(id, profileID, episodeID string, season, episode int) DeliveryRow {
	seriesID := "series-123"
	return DeliveryRow{
		Delivery: Delivery{
			ID:          id,
			ProfileID:   profileID,
			SeriesID:    &seriesID,
			EpisodeID:   &episodeID,
			Type:        DeliveryTypeEpisodeAvailable,
			ReasonFlags: []byte(`{"next_up":true}`),
			CreatedAt:   time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
		SeriesTitle:   "Severance",
		EpisodeTitle:  fmt.Sprintf("Episode %d", episode),
		SeasonNumber:  &season,
		EpisodeNumber: &episode,
	}
}

func TestComposeNotificationEmailEscapesHTML(t *testing.T) {
	row := emailEpisodeRow("01A", "p1", "ep-1", 2, 3)
	row.SeriesTitle = `<script>alert("x")</script>`
	content := composeNotificationEmail(EmailModePerEpisode, []DeliveryRow{row}, emailComposeOptions{})
	if strings.Contains(content.HTML, "<script>") {
		t.Fatalf("series title not escaped:\n%s", content.HTML)
	}
}

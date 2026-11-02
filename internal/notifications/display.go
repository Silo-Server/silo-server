package notifications

import (
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/titleutil"
)

// NotificationDisplay is the compact, user-facing display metadata clients use
// when they need a native notification title/body without fetching a full inbox
// page.
type NotificationDisplay struct {
	DeliveryID string `json:"delivery_id"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	Category   string `json:"category"`
	URL        string `json:"url"`
}

// BuildNotificationDisplay renders a delivery into native-notification display
// metadata. Keep this in sync with inbox/websocket semantics by only deriving
// from DeliveryRow.
func BuildNotificationDisplay(row DeliveryRow) NotificationDisplay {
	display := NotificationDisplay{
		DeliveryID: row.ID,
		Title:      genericNotificationTitle,
		Category:   "notification",
		URL:        "/notifications",
	}
	switch row.Type {
	case DeliveryTypeEpisodeAvailable:
		display.Category = "episode_available"
		display.Title = episodeDisplayTitle(row)
		display.Body = episodeDisplayBody(row)
		if row.SeriesID != nil && *row.SeriesID != "" {
			display.ThreadID = "series:" + *row.SeriesID
		}
		if row.EpisodeID != nil && *row.EpisodeID != "" {
			display.URL = "/item/" + *row.EpisodeID
		}
	case DeliveryTypeRequestFulfilled:
		flags := parseRequestFlags(row.ReasonFlags)
		display.Category = "request_fulfilled"
		display.Title = "Your request is now available"
		display.Body = "Your media request has arrived in the library."
		if flags.Follower {
			display.Title = followedTitleAvailable
			display.Body = "A title you asked to hear about has arrived in the library."
		}
		if row.SeriesTitle != "" {
			display.Title = row.SeriesTitle + " is now available"
		}
		if flags.RequestID != "" {
			display.ThreadID = "request:" + flags.RequestID
		} else if row.SeriesID != nil && *row.SeriesID != "" {
			display.ThreadID = "item:" + *row.SeriesID
		}
		if row.SeriesID != nil && *row.SeriesID != "" {
			display.URL = "/item/" + *row.SeriesID
		}
	case DeliveryTypeRequestApproved:
		flags := parseRequestFlags(row.ReasonFlags)
		display.Category = "request_approved"
		display.Title = "Your request was approved"
		if flags.Title != "" {
			display.Title = flags.Title + " was approved"
		}
		display.Body = "Your media request was approved."
		display.ThreadID = requestThreadID(flags)
	case DeliveryTypeRequestDeclined:
		flags := parseRequestFlags(row.ReasonFlags)
		display.Category = "request_declined"
		display.Title = "Your request was declined"
		if flags.Title != "" {
			display.Title = flags.Title + " was declined"
		}
		display.Body = "Your media request was declined."
		if flags.Reason != "" {
			// Admin-typed free text with no upstream length cap; keep native
			// notification bodies bounded.
			display.Body = "Reason: " + truncateDisplayText(flags.Reason, displayBodyMaxLen)
		}
		display.ThreadID = requestThreadID(flags)
	case DeliveryTypeWebhookAutoDisabled:
		display.Category = "webhook_auto_disabled"
		display.Title = "A webhook stopped working"
		display.Body = "Open notification settings to fix it."
		display.ThreadID = "settings:notifications"
		display.URL = "/settings/notifications"
	default:
		if row.Type != "" {
			display.Category = strings.ReplaceAll(row.Type, ".", "_")
		}
	}
	return display
}

// episodeDisplayTitle leads with the series name alone: native notifications
// give the title one line, and any fixed wording around the name pushes it
// past the cut. The episode details go in the body, which wraps.
func episodeDisplayTitle(row DeliveryRow) string {
	if row.SeriesTitle != "" {
		return row.SeriesTitle
	}
	return "New episode available"
}

// episodeDisplayBody reads "New episode · S02E01 · Hello, Ms. Cobel", dropping
// the parts that are missing. A placeholder title such as "TBA" says nothing
// the episode code doesn't, so it is left out.
func episodeDisplayBody(row DeliveryRow) string {
	parts := make([]string, 0, 3)
	if row.SeriesTitle != "" {
		parts = append(parts, "New episode")
	}
	if code := episodeDisplayCode(row); code != "" {
		parts = append(parts, code)
	}
	if title := strings.TrimSpace(row.EpisodeTitle); title != "" && !titleutil.IsProvisionalEpisodeTitle(title) {
		parts = append(parts, title)
	}
	return strings.Join(parts, " · ")
}

func episodeDisplayCode(row DeliveryRow) string {
	if row.SeasonNumber != nil && row.EpisodeNumber != nil {
		return fmt.Sprintf("S%02dE%02d", *row.SeasonNumber, *row.EpisodeNumber)
	}
	return ""
}

// displayBodyMaxLen bounds free-text notification bodies.
const displayBodyMaxLen = 240

func truncateDisplayText(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

func requestThreadID(flags RequestFlags) string {
	if flags.RequestID == "" {
		return ""
	}
	return "request:" + flags.RequestID
}

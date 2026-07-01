package notifications

import (
	"fmt"
	"strings"
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
		display.Category = "request_fulfilled"
		display.Title = "Your request is now available"
		if row.SeriesTitle != "" {
			display.Title = row.SeriesTitle + " is now available"
		}
		display.Body = "Your media request has arrived in the library."
		flags := parseRequestFlags(row.ReasonFlags)
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

func episodeDisplayTitle(row DeliveryRow) string {
	code := episodeDisplayCode(row)
	switch {
	case row.SeriesTitle != "" && code != "":
		return fmt.Sprintf("The latest episode of %s %s just dropped!", row.SeriesTitle, code)
	case row.SeriesTitle != "":
		return "The latest episode of " + row.SeriesTitle + " just dropped!"
	case code != "":
		return "New episode " + code + " available"
	default:
		return "New episode available"
	}
}

func episodeDisplayBody(row DeliveryRow) string {
	if row.EpisodeTitle != "" {
		return row.EpisodeTitle
	}
	return episodeDisplayCode(row)
}

func episodeDisplayCode(row DeliveryRow) string {
	if row.SeasonNumber != nil && row.EpisodeNumber != nil {
		return fmt.Sprintf("S%02dE%02d", *row.SeasonNumber, *row.EpisodeNumber)
	}
	return ""
}

// displayBodyMaxLen bounds free-text notification bodies; matches the varchar
// caps used elsewhere in the delivery pipeline.
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

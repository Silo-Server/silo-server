package notifications

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// emailMaxItemsRendered caps how many lines one email renders; the remainder
// collapses into a "+N more" line (the inbox always has everything).
const emailMaxItemsRendered = 30

// untitledSeriesGroup is the group heading for episode rows whose series
// metadata is missing or was deleted.
const untitledSeriesGroup = "New episodes"

// emailContent is one rendered notification email.
type emailContent struct {
	Subject string
	Text    string
	HTML    string
}

// emailSeriesGroup is one series' new episodes, in first-appearance order.
type emailSeriesGroup struct {
	seriesID string
	title    string
	episodes []DeliveryRow
}

// emailItems is the collated, deduplicated content of one email. Accounts
// with several profiles following the same series have one delivery row per
// profile; an email reports the episode once.
type emailItems struct {
	series   []emailSeriesGroup
	episodes int
	requests []DeliveryRow
	others   []DeliveryRow
}

// collateEmailItems groups and dedupes delivery rows for rendering.
func collateEmailItems(rows []DeliveryRow) emailItems {
	var items emailItems
	seenEpisodes := make(map[string]struct{}, len(rows))
	seenRequests := make(map[string]struct{}, 4)
	groupIndex := make(map[string]int, 4)

	for _, row := range rows {
		switch row.Type {
		case DeliveryTypeEpisodeAvailable:
			key := row.ID
			if row.EpisodeID != nil && *row.EpisodeID != "" {
				key = *row.EpisodeID
			}
			if _, ok := seenEpisodes[key]; ok {
				continue
			}
			seenEpisodes[key] = struct{}{}
			seriesID := ""
			if row.SeriesID != nil {
				seriesID = *row.SeriesID
			}
			idx, ok := groupIndex[seriesID]
			if !ok {
				idx = len(items.series)
				groupIndex[seriesID] = idx
				title := row.SeriesTitle
				if title == "" {
					title = untitledSeriesGroup
				}
				items.series = append(items.series, emailSeriesGroup{seriesID: seriesID, title: title})
			}
			items.series[idx].episodes = append(items.series[idx].episodes, row)
			items.episodes++
		case DeliveryTypeRequestFulfilled:
			key := parseRequestFulfilledFlags(row.ReasonFlags).RequestID
			if key == "" {
				key = row.ID
			}
			if _, ok := seenRequests[key]; ok {
				continue
			}
			seenRequests[key] = struct{}{}
			items.requests = append(items.requests, row)
		default:
			items.others = append(items.others, row)
		}
	}

	for i := range items.series {
		sort.SliceStable(items.series[i].episodes, func(a, b int) bool {
			ea, eb := items.series[i].episodes[a], items.series[i].episodes[b]
			if ea.SeasonNumber == nil || eb.SeasonNumber == nil ||
				ea.EpisodeNumber == nil || eb.EpisodeNumber == nil {
				return false
			}
			if *ea.SeasonNumber != *eb.SeasonNumber {
				return *ea.SeasonNumber < *eb.SeasonNumber
			}
			return *ea.EpisodeNumber < *eb.EpisodeNumber
		})
	}
	return items
}

// episodeCode renders "S02E03"; empty when numbering is unknown.
func episodeCode(row DeliveryRow) string {
	if row.SeasonNumber == nil || row.EpisodeNumber == nil {
		return ""
	}
	return fmt.Sprintf("S%02dE%02d", *row.SeasonNumber, *row.EpisodeNumber)
}

// episodeLine renders one episode entry: "S02E03 — Title", degrading to
// whichever part exists.
func episodeLine(row DeliveryRow) string {
	code := episodeCode(row)
	switch {
	case code != "" && row.EpisodeTitle != "":
		return code + " — " + row.EpisodeTitle
	case code != "":
		return code
	case row.EpisodeTitle != "":
		return row.EpisodeTitle
	default:
		return genericEpisodeTitle
	}
}

// requestLine renders one fulfilled-request entry.
func requestLine(row DeliveryRow) string {
	if row.SeriesTitle != "" {
		return row.SeriesTitle + " is now available"
	}
	return "Your media request is now available"
}

// otherLine renders operational and unknown delivery types generically.
func otherLine(row DeliveryRow) string {
	if row.Type == DeliveryTypeWebhookAutoDisabled {
		return "A webhook stopped working — open notification settings to fix it"
	}
	return genericNotificationTitle
}

// countPart pluralizes "3 new episodes" style subject fragments.
func countPart(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", count, plural)
}

// emailSubject builds the subject line from the collated items.
func emailSubject(mode string, items emailItems) string {
	parts := make([]string, 0, 3)
	if items.episodes > 0 {
		parts = append(parts, countPart(items.episodes, "new episode", "new episodes"))
	}
	if len(items.requests) > 0 {
		parts = append(parts, countPart(len(items.requests), "request ready", "requests ready"))
	}
	if len(items.others) > 0 {
		parts = append(parts, countPart(len(items.others), "update", "updates"))
	}
	summary := strings.Join(parts, ", ")

	if mode == EmailModeDailyDigest {
		return "Silo daily digest: " + summary
	}
	if items.episodes == 1 && len(items.requests) == 0 && len(items.others) == 0 {
		row := items.series[0].episodes[0]
		subject := genericEpisodeTitle
		if items.series[0].title != untitledSeriesGroup {
			subject += " of " + items.series[0].title
		}
		if line := episodeLine(row); line != genericEpisodeTitle {
			subject += ": " + line
		}
		return subject
	}
	if items.episodes == 0 && len(items.requests) == 1 && len(items.others) == 0 {
		return requestLine(items.requests[0])
	}
	return "Silo: " + summary
}

// itemURL builds a deep link; empty when no external URL is configured.
func itemURL(baseURL, itemID string) string {
	if baseURL == "" || itemID == "" {
		return ""
	}
	return baseURL + "/item/" + itemID
}

// composeNotificationEmail renders one email (text + HTML) for the given
// delivery rows. baseURL is the admin-configured external URL; empty renders
// without links.
func composeNotificationEmail(mode string, rows []DeliveryRow, baseURL string) emailContent {
	items := collateEmailItems(rows)

	var text strings.Builder
	var body strings.Builder
	rendered := 0
	total := items.episodes + len(items.requests) + len(items.others)

	writeLine := func(plain, href string) {
		rendered++
		if rendered > emailMaxItemsRendered {
			return
		}
		text.WriteString("  " + plain + "\n")
		if href != "" {
			body.WriteString(fmt.Sprintf(
				`<li style="margin:2px 0;"><a href="%s" style="color:#6d6df7;text-decoration:none;">%s</a></li>`,
				html.EscapeString(href), html.EscapeString(plain)))
		} else {
			body.WriteString(fmt.Sprintf(`<li style="margin:2px 0;">%s</li>`, html.EscapeString(plain)))
		}
	}
	writeHeading := func(title, href string) {
		text.WriteString(title + "\n")
		label := html.EscapeString(title)
		if href != "" {
			label = fmt.Sprintf(`<a href="%s" style="color:inherit;text-decoration:none;">%s</a>`,
				html.EscapeString(href), label)
		}
		body.WriteString(fmt.Sprintf(
			`<h3 style="margin:14px 0 4px;font-size:15px;">%s</h3>`, label))
	}
	openList := func() { body.WriteString(`<ul style="margin:4px 0;padding-left:20px;">`) }
	closeList := func() { body.WriteString(`</ul>`) }

	for _, group := range items.series {
		if rendered >= emailMaxItemsRendered {
			break
		}
		writeHeading(group.title, itemURL(baseURL, group.seriesID))
		openList()
		for _, row := range group.episodes {
			episodeID := ""
			if row.EpisodeID != nil {
				episodeID = *row.EpisodeID
			}
			writeLine(episodeLine(row), itemURL(baseURL, episodeID))
		}
		closeList()
	}
	if len(items.requests) > 0 && rendered < emailMaxItemsRendered {
		writeHeading("Requests ready", "")
		openList()
		for _, row := range items.requests {
			seriesID := ""
			if row.SeriesID != nil {
				seriesID = *row.SeriesID
			}
			writeLine(requestLine(row), itemURL(baseURL, seriesID))
		}
		closeList()
	}
	if len(items.others) > 0 && rendered < emailMaxItemsRendered {
		writeHeading("Other updates", "")
		openList()
		for _, row := range items.others {
			writeLine(otherLine(row), "")
		}
		closeList()
	}
	if remainder := total - emailMaxItemsRendered; remainder > 0 {
		more := fmt.Sprintf("…and %d more in your Silo inbox.", remainder)
		text.WriteString(more + "\n")
		body.WriteString(fmt.Sprintf(
			`<p style="margin:8px 0;color:#888;">%s</p>`, html.EscapeString(more)))
	}

	intro := "New in your library:"
	if mode == EmailModeDailyDigest {
		intro = "Here's what's new since your last digest:"
	}
	footer := "You're receiving this because email notifications are enabled for your Silo account. " +
		"Manage them in Settings → Notifications."
	footerHTML := html.EscapeString(footer)
	if baseURL != "" {
		settingsURL := html.EscapeString(baseURL + "/settings/notifications")
		footerHTML = strings.Replace(footerHTML,
			"Settings → Notifications",
			fmt.Sprintf(`<a href="%s" style="color:#888;">Settings → Notifications</a>`, settingsURL), 1)
	}

	htmlBody := fmt.Sprintf(`<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.5;color:#1a1a1a;max-width:560px;">
<p style="margin:0 0 8px;">%s</p>
%s
<hr style="border:none;border-top:1px solid #e5e5e5;margin:16px 0 8px;">
<p style="margin:0;font-size:12px;color:#888;">%s</p>
</div>`,
		html.EscapeString(intro), body.String(), footerHTML)

	return emailContent{
		Subject: emailSubject(mode, items),
		Text:    intro + "\n\n" + text.String() + "\n" + footer + "\n",
		HTML:    htmlBody,
	}
}

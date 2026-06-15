package notifications

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/mail"
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
		case DeliveryTypeRequestFulfilled, DeliveryTypeRequestApproved, DeliveryTypeRequestDeclined:
			// Key by (type, request): one request may legitimately produce an
			// approved row and a fulfilled row in the same window.
			key := row.ID
			if requestID := parseRequestFlags(row.ReasonFlags).RequestID; requestID != "" {
				key = row.Type + ":" + requestID
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

// requestLine renders one request-status entry. Fulfilled rows carry the
// catalog title via the join; approved/declined rows carry it in the flags.
func requestLine(row DeliveryRow) string {
	flags := parseRequestFlags(row.ReasonFlags)
	title := row.SeriesTitle
	if title == "" {
		title = flags.Title
	}
	switch row.Type {
	case DeliveryTypeRequestApproved:
		if title != "" {
			return "Your request for " + title + " was approved"
		}
		return "Your media request was approved"
	case DeliveryTypeRequestDeclined:
		line := "Your media request was declined"
		if title != "" {
			line = "Your request for " + title + " was declined"
		}
		if flags.Reason != "" {
			line += " — " + flags.Reason
		}
		return line
	default:
		if title != "" {
			return title + " is now available"
		}
		return "Your media request is now available"
	}
}

// requestsAllFulfilled reports whether every collated request row is a
// fulfillment; it drives the "requests ready" vs "request updates" copy.
func requestsAllFulfilled(items emailItems) bool {
	for _, row := range items.requests {
		if row.Type != DeliveryTypeRequestFulfilled {
			return false
		}
	}
	return true
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
		singular, plural := "request update", "request updates"
		if requestsAllFulfilled(items) {
			singular, plural = "request ready", "requests ready"
		}
		parts = append(parts, countPart(len(items.requests), singular, plural))
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

// emailComposeOptions carries the per-send rendering context.
type emailComposeOptions struct {
	// BaseURL is the admin-configured external URL; empty renders without
	// links.
	BaseURL string
	// ProfileName labels whose notifications these are — several profiles on
	// one account may deliver to the same fallback address.
	ProfileName string
	// UnsubscribeURL is the tokenized one-click unsubscribe link; empty
	// renders without one.
	UnsubscribeURL string
}

// composeNotificationEmail renders one email (text + HTML) for the given
// delivery rows.
func composeNotificationEmail(mode string, rows []DeliveryRow, opts emailComposeOptions) emailContent {
	baseURL := opts.BaseURL
	items := collateEmailItems(rows)

	var text strings.Builder
	var body strings.Builder
	rendered := 0
	total := items.episodes + len(items.requests) + len(items.others)

	// writeItem renders one row: plain feeds the text body; code (optional
	// "S02E03" badge) and label feed the HTML row.
	writeItem := func(plain, code, label, href string) {
		rendered++
		if rendered > emailMaxItemsRendered {
			return
		}
		text.WriteString("  " + plain + "\n")
		var inner strings.Builder
		if code != "" {
			inner.WriteString(fmt.Sprintf(`<span style="font:500 12px/1 %s;color:%s;">%s</span>`,
				mail.EmailFontMono, mail.EmailColorMuted, html.EscapeString(code)))
			if label != "" {
				inner.WriteString("&nbsp;&nbsp;")
			}
		}
		inner.WriteString(html.EscapeString(label))
		content := inner.String()
		if href != "" {
			content = fmt.Sprintf(`<a href="%s" style="color:%s;text-decoration:none;">%s</a>`,
				html.EscapeString(href), mail.EmailColorText, content)
		}
		body.WriteString(fmt.Sprintf(
			`<li style="margin:0;padding:8px 2px;border-top:1px solid %s;font:400 14px/1.5 %s;color:%s;">%s</li>`,
			mail.EmailColorRule, mail.EmailFont, mail.EmailColorText, content))
	}
	writeHeading := func(title, href string) {
		text.WriteString(title + "\n")
		label := html.EscapeString(title)
		if href != "" {
			label = fmt.Sprintf(`<a href="%s" style="color:inherit;text-decoration:none;">%s</a>`,
				html.EscapeString(href), label)
		}
		top := "22px"
		if body.Len() == 0 {
			top = "0"
		}
		body.WriteString(fmt.Sprintf(
			`<h2 style="margin:%s 0 6px;font:600 15px/1.4 %s;color:%s;">%s</h2>`,
			top, mail.EmailFont, mail.EmailColorText, label))
	}
	openList := func() { body.WriteString(`<ul style="margin:0;padding:0;list-style:none;">`) }
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
			code := episodeCode(row)
			label := row.EpisodeTitle
			if code == "" && label == "" {
				label = genericEpisodeTitle
			}
			writeItem(episodeLine(row), code, label, itemURL(baseURL, episodeID))
		}
		closeList()
	}
	if len(items.requests) > 0 && rendered < emailMaxItemsRendered {
		heading := "Request updates"
		if requestsAllFulfilled(items) {
			heading = "Requests ready"
		}
		writeHeading(heading, "")
		openList()
		for _, row := range items.requests {
			seriesID := ""
			if row.SeriesID != nil {
				seriesID = *row.SeriesID
			}
			writeItem(requestLine(row), "", requestLine(row), itemURL(baseURL, seriesID))
		}
		closeList()
	}
	if len(items.others) > 0 && rendered < emailMaxItemsRendered {
		writeHeading("Other updates", "")
		openList()
		for _, row := range items.others {
			writeItem(otherLine(row), "", otherLine(row), "")
		}
		closeList()
	}
	if remainder := total - emailMaxItemsRendered; remainder > 0 {
		more := fmt.Sprintf("…and %d more in your Silo inbox.", remainder)
		text.WriteString(more + "\n")
		body.WriteString(fmt.Sprintf(`<p style="margin:14px 0 0;font:400 13px/1.5 %s;color:%s;">%s</p>`,
			mail.EmailFont, mail.EmailColorMuted, html.EscapeString(more)))
	}

	forProfile := ""
	if opts.ProfileName != "" {
		forProfile = " for " + opts.ProfileName
	}
	intro := fmt.Sprintf("New in your library%s:", forProfile)
	if mode == EmailModeDailyDigest {
		intro = fmt.Sprintf("Here's what's new%s since the last digest:", forProfile)
	}
	subjectFor := ""
	if opts.ProfileName != "" {
		subjectFor = " (for " + opts.ProfileName + ")"
	}

	profileLabel := "this profile"
	if opts.ProfileName != "" {
		profileLabel = "the profile “" + opts.ProfileName + "”"
	}
	footer := fmt.Sprintf("You're receiving this because email notifications are enabled for"+
		" %s on your Silo account. Manage them in Settings → Notifications.", profileLabel)
	footerHTML := html.EscapeString(footer)
	if baseURL != "" {
		settingsURL := html.EscapeString(baseURL + "/settings/notifications")
		footerHTML = strings.Replace(footerHTML,
			"Settings → Notifications",
			fmt.Sprintf(`<a href="%s" style="color:%s;">Settings → Notifications</a>`,
				settingsURL, mail.EmailColorMuted), 1)
	}
	if opts.UnsubscribeURL != "" {
		footer += " To stop these emails, open: " + opts.UnsubscribeURL
		footerHTML += fmt.Sprintf(` <a href="%s" style="color:%s;">Unsubscribe</a>`,
			html.EscapeString(opts.UnsubscribeURL), mail.EmailColorMuted)
	}

	htmlBody := mail.RenderLayout(mail.LayoutOptions{
		Preheader:  emailPreheader(items),
		Title:      strings.TrimSuffix(intro, ":"),
		BodyHTML:   body.String(),
		FooterHTML: footerHTML,
	})

	return emailContent{
		Subject: emailSubject(mode, items) + subjectFor,
		Text:    intro + "\n\n" + text.String() + "\n" + footer + "\n",
		HTML:    htmlBody,
	}
}

// emailPreheader picks the inbox-preview snippet: the first item, the same
// way a notification banner would lead with it.
func emailPreheader(items emailItems) string {
	if len(items.series) > 0 && len(items.series[0].episodes) > 0 {
		line := episodeLine(items.series[0].episodes[0])
		if title := items.series[0].title; title != untitledSeriesGroup {
			return title + " · " + line
		}
		return line
	}
	if len(items.requests) > 0 {
		return requestLine(items.requests[0])
	}
	if len(items.others) > 0 {
		return otherLine(items.others[0])
	}
	return ""
}

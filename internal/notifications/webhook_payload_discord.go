package notifications

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Discord embed accent colors per reason (decimal RGB), in reason precedence
// order: favorite, watchlist, continue_watching, next_up.
const (
	discordColorFavorite         = 5814783
	discordColorWatchlist        = 3066993
	discordColorContinueWatching = 15844367
	discordColorNextUp           = 15158332
)

// Discord embed limits (enforced by the builder).
const (
	discordTitleLimit       = 256
	discordDescriptionLimit = 4096
	discordFieldValueLimit  = 1024
	discordFooterLimit      = 2048
	discordTotalLimit       = 6000
)

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbedFooter struct {
	Text string `json:"text"`
}

type discordEmbedAuthor struct {
	Name string `json:"name"`
}

type discordEmbedMedia struct {
	URL string `json:"url"`
}

// discordEmbed names external origins under the admin's poster mode
// (System.discordPosterURL): public provider services (themoviedb.org,
// imdb.com, thetvdb.com and their image CDNs) by default, plus presigned
// server-storage URLs only under the explicit "server" opt-in — Discord
// fetches thumbnail URLs and the raw payload is visible to channel members,
// so a self-hosted URL reveals the server's address
// (docs/architecture/notifications.md, "Server URL leakage"). Builders never
// derive artwork URLs themselves; they render the PosterURL the sender layer
// resolved.
type discordEmbed struct {
	Title       string              `json:"title"`
	URL         string              `json:"url,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Author      *discordEmbedAuthor `json:"author,omitempty"`
	Thumbnail   *discordEmbedMedia  `json:"thumbnail,omitempty"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
}

// discordAllowedMentions whitelists exactly what a message's content may
// ping. Parse always serializes (an empty list disables Discord's implicit
// mention parsing), so content assembled from user-derived text can never
// ping roles or @everyone.
type discordAllowedMentions struct {
	Parse []string `json:"parse"`
	Users []string `json:"users,omitempty"`
}

type discordWebhookBody struct {
	Content         string                  `json:"content,omitempty"`
	Embeds          []discordEmbed          `json:"embeds"`
	Username        string                  `json:"username"`
	AllowedMentions *discordAllowedMentions `json:"allowed_mentions,omitempty"`
}

// BuildDiscordWebhookPayload renders a delivery as a Discord webhook body.
// Pure function; enforces Discord's embed limits with the spec's truncation
// policy (description first, then drop fields right-to-left).
func BuildDiscordWebhookPayload(row DeliveryRow, test bool) ([]byte, error) {
	return json.Marshal(discordWebhookBody{
		Embeds:   []discordEmbed{buildDiscordDigestEntry(row, test).embed},
		Username: siloSenderName,
	})
}

// discordDMMaxEmbeds is Discord's per-message embed cap.
const discordDMMaxEmbeds = 10

// discordDMBody is a bot channel-message body. Unlike webhook bodies it has
// no username override: a bot message always carries the bot's own identity.
type discordDMBody struct {
	Content string         `json:"content,omitempty"`
	Embeds  []discordEmbed `json:"embeds"`
}

// BuildDiscordDMPayload renders one account's pending deliveries as a single
// bot DM, one embed per item up to Discord's 10-embed cap and the message's
// 6,000-character budget. Overflow keeps the newest items and points at the
// Silo inbox for the rest, mirroring the email digest's rendering cap.
func BuildDiscordDMPayload(rows []DeliveryRow) ([]byte, error) {
	overflow := 0
	if len(rows) > discordDMMaxEmbeds {
		overflow = len(rows) - discordDMMaxEmbeds
		rows = rows[len(rows)-discordDMMaxEmbeds:]
	}
	entries := make([]discordDigestEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, buildDiscordDigestEntry(row, false))
	}
	embeds, dropped := fitDiscordMessage(entries)
	overflow += dropped
	body := discordDMBody{Embeds: embeds}
	if overflow > 0 {
		body.Content = fmt.Sprintf("…and %d more in your Silo inbox", overflow)
	}
	return json.Marshal(body)
}

// discordEmbedAuthorLine renders the small "what happened" line above the
// embed title.
func discordEmbedAuthorLine(row DeliveryRow) string {
	switch row.Type {
	case DeliveryTypeEpisodeAvailable:
		return "New episode on Silo"
	case DeliveryTypeRequestFulfilled:
		if parseRequestFlags(row.ReasonFlags).Follower {
			return "Now available on Silo"
		}
		return "Your request is now available on Silo"
	case DeliveryTypeRequestApproved:
		return "Your request was approved on Silo"
	case DeliveryTypeRequestDeclined:
		return "Your request was declined on Silo"
	default:
		return genericNotificationTitle
	}
}

// discordEmbedFooterText renders the footer: the sender brand, plus the
// content rating when known so the advisory rides along unobtrusively.
func discordEmbedFooterText(contentRating string, test bool) string {
	if test {
		return "Silo test notification"
	}
	if contentRating != "" {
		return siloSenderName + " • " + truncateWithEllipsis(contentRating, 32)
	}
	return siloSenderName
}

// buildDiscordDigestEntry renders one delivery as a Discord embed within all
// of Discord's per-embed limits: poster thumbnail, overview teaser, provider
// links, rating, and genres on top of the title/reason basics. The
// season/episode code lives in the title, so no dedicated fields repeat it.
func buildDiscordDigestEntry(row DeliveryRow, test bool) discordDigestEntry {
	flags := parseReasonFlags(row.ReasonFlags)

	// Titles assembled from catalog metadata virtually never approach the
	// limit, but Discord hard-rejects oversized embeds, so clip as a last
	// resort even though the truncation policy prefers other fields.
	title := truncateWithEllipsis(discordEmbedTitle(row), discordTitleLimit)

	overview := row.SeriesOverview
	if row.Type == DeliveryTypeEpisodeAvailable && row.EpisodeOverview != "" {
		overview = row.EpisodeOverview
	}
	isRequestType := row.Type == DeliveryTypeRequestFulfilled || isRequestLifecycleType(row.Type)
	var requestFlags RequestFlags
	if isRequestType {
		requestFlags = parseRequestFlags(row.ReasonFlags)
	}
	ids := providerIDs{MediaType: row.MediaType, IMDB: row.IMDBID, TMDB: row.TMDBID, TVDB: row.TVDBID}
	if isRequestLifecycleType(row.Type) {
		// No catalog join; derive the provider link from the request flags.
		ids = providerIDs{MediaType: requestFlags.MediaType}
		if requestFlags.TMDBID > 0 {
			ids.TMDB = fmt.Sprintf("%d", requestFlags.TMDBID)
		}
	}

	fields := make([]discordEmbedField, 0, 3)
	if labels := reasonLabelList(flags); len(labels) > 0 {
		fields = append(fields, discordEmbedField{
			Name:   "Reason",
			Value:  truncateWithEllipsis(strings.Join(labels, " & "), discordFieldValueLimit),
			Inline: true,
		})
	}
	if isRequestType {
		if mediaType := mediaTypeLabel(requestFlags.MediaType); mediaType != "" {
			fields = append(fields, discordEmbedField{Name: "Type", Value: mediaType, Inline: true})
		}
	}
	if row.Type == DeliveryTypeRequestDeclined && requestFlags.Reason != "" {
		fields = append(fields, discordEmbedField{
			Name:  "Reason",
			Value: truncateWithEllipsis(requestFlags.Reason, discordFieldValueLimit),
		})
	}
	if rating := ratingLabel(row.RatingIMDB, row.RatingTMDB); rating != "" {
		fields = append(fields, discordEmbedField{Name: "Rating", Value: rating, Inline: true})
	}
	if genres := genresLabel(row.Genres); genres != "" {
		fields = append(fields, discordEmbedField{Name: "Genres", Value: genres, Inline: true})
	}

	color := discordEmbedColor(flags)
	switch row.Type {
	case DeliveryTypeRequestApproved:
		color = serverChannelColorApproved
	case DeliveryTypeRequestDeclined:
		color = serverChannelColorDeclined
	}
	embed := discordEmbed{
		Title:  title,
		URL:    ids.titleURL(),
		Color:  color,
		Author: &discordEmbedAuthor{Name: discordEmbedAuthorLine(row)},
		Footer: &discordEmbedFooter{Text: discordEmbedFooterText(row.ContentRating, test)},
		Fields: fields,
	}
	// The poster decision (provider CDN vs presigned vs none) is the sender
	// layer's: builders render whatever PosterURL carries.
	if row.PosterURL != "" {
		embed.Thumbnail = &discordEmbedMedia{URL: row.PosterURL}
	}
	if !row.CreatedAt.IsZero() {
		embed.Timestamp = row.CreatedAt.UTC().Format(time.RFC3339)
	}
	return newDiscordDigestEntry(embed, overview, ids)
}

func discordEmbedTitle(row DeliveryRow) string {
	switch row.Type {
	case DeliveryTypeRequestFulfilled:
		if row.SeriesTitle != "" {
			return titleWithYear(row.SeriesTitle, row.Year)
		}
		return "Request fulfilled"
	case DeliveryTypeRequestApproved, DeliveryTypeRequestDeclined:
		// No catalog join exists for an unfulfilled request; the title rides
		// in the reason flags.
		flags := parseRequestFlags(row.ReasonFlags)
		if flags.Title != "" {
			return titleWithYear(flags.Title, flags.Year)
		}
		return "Media request"
	case DeliveryTypeEpisodeAvailable:
		// Falls out of the switch into the episode title assembly below.
	default:
		return genericNotificationTitle
	}
	series := row.SeriesTitle
	if series == "" {
		series = genericEpisodeTitle
	}
	var code string
	if row.SeasonNumber != nil && row.EpisodeNumber != nil {
		code = fmt.Sprintf("S%d E%d", *row.SeasonNumber, *row.EpisodeNumber)
	}
	switch {
	case code != "" && row.EpisodeTitle != "":
		return fmt.Sprintf("%s — %s: %s", series, code, row.EpisodeTitle)
	case code != "":
		return fmt.Sprintf("%s — %s", series, code)
	default:
		return series
	}
}

func discordEmbedColor(flags ReasonFlags) int {
	switch {
	case flags.Favorite:
		return discordColorFavorite
	case flags.Watchlist:
		return discordColorWatchlist
	case flags.ContinueWatching:
		return discordColorContinueWatching
	case flags.NextUp:
		return discordColorNextUp
	default:
		return discordColorFavorite
	}
}

// discordEmbedLen sums length over the embed text Discord's 6,000-character
// limit counts: title, description, field names and values, footer, and
// author name.
func discordEmbedLen(embed *discordEmbed, length func(string) int) int {
	total := length(embed.Title) + length(embed.Description)
	if embed.Author != nil {
		total += length(embed.Author.Name)
	}
	if embed.Footer != nil {
		total += length(embed.Footer.Text)
	}
	for _, field := range embed.Fields {
		total += length(field.Name) + length(field.Value)
	}
	return total
}

func byteLen(s string) int { return len(s) }

// discordEmbedTotal counts in bytes, which never undercounts Discord's
// characters.
func discordEmbedTotal(embed *discordEmbed) int {
	return discordEmbedLen(embed, byteLen)
}

// enforceDiscordTotalLimit applies the 6,000-char total cap: truncate the
// description first, then drop fields right-to-left.
func enforceDiscordTotalLimit(embed *discordEmbed) {
	embed.Description = truncateWithEllipsis(embed.Description, discordDescriptionLimit)
	if discordEmbedTotal(embed) <= discordTotalLimit {
		return
	}
	overflow := discordEmbedTotal(embed) - discordTotalLimit
	if keep := len(embed.Description) - overflow; keep > 0 {
		embed.Description = truncateWithEllipsis(embed.Description, keep)
	} else {
		embed.Description = ""
	}
	for discordEmbedTotal(embed) > discordTotalLimit && len(embed.Fields) > 0 {
		embed.Fields = embed.Fields[:len(embed.Fields)-1]
	}
}

// discordDigestEntry is one digest embed plus the parts of its description,
// so the message budget can shorten the overview teaser without cutting the
// provider link line.
type discordDigestEntry struct {
	embed  discordEmbed
	teaser string
	links  string
}

// newDiscordDigestEntry sets the embed's description from the overview and
// provider links and applies the per-embed limits.
func newDiscordDigestEntry(embed discordEmbed, overview string, ids providerIDs) discordDigestEntry {
	entry := discordDigestEntry{embed: embed, teaser: overviewSnippet(overview), links: ids.linkLine()}
	entry.embed.Description = joinDescription(entry.teaser, entry.links)
	enforceDiscordTotalLimit(&entry.embed)
	return entry
}

// discordMinTeaser is the shortest overview teaser worth sending; below it the
// message budget drops teasers entirely.
const discordMinTeaser = 80

// discordTextLen counts characters for Discord's limits: code points, with
// characters outside the Basic Multilingual Plane (most emoji) counted twice
// so the count is safe whether Discord counts code points or UTF-16 units.
func discordTextLen(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// discordMessageChars counts embed text across a whole message, the scope of
// Discord's 6,000-character limit.
func discordMessageChars(embeds []discordEmbed) int {
	total := 0
	for i := range embeds {
		total += discordEmbedLen(&embeds[i], discordTextLen)
	}
	return total
}

// renderDiscordDigest returns the entries' embeds with every overview teaser
// clipped to teaserLimit characters; a negative limit leaves teasers whole.
func renderDiscordDigest(entries []discordDigestEntry, teaserLimit int) []discordEmbed {
	embeds := make([]discordEmbed, 0, len(entries))
	for _, entry := range entries {
		embed := entry.embed
		if teaserLimit >= 0 {
			// Only ever shorten: the per-embed limits may already have cut
			// this description below what the parts would rebuild.
			if description := joinDescription(shortenTeaser(entry.teaser, teaserLimit), entry.links); len(description) < len(embed.Description) {
				embed.Description = description
			}
		}
		embeds = append(embeds, embed)
	}
	return embeds
}

// fitDiscordMessage applies Discord's 6,000-character limit to a whole
// multi-embed message. Teasers shorten first, as evenly as the budget allows;
// when even teaser-less embeds don't fit, the oldest entry drops. The newest
// entry always stays (one embed already fits on its own). Returns the embeds
// to send and how many entries were dropped.
func fitDiscordMessage(entries []discordDigestEntry) ([]discordEmbed, int) {
	for dropped := 0; ; dropped++ {
		fits := func(teaserLimit int) ([]discordEmbed, bool) {
			embeds := renderDiscordDigest(entries, teaserLimit)
			return embeds, discordMessageChars(embeds) <= discordTotalLimit
		}
		if embeds, ok := fits(-1); ok {
			return embeds, dropped
		}
		// The total shrinks with the teaser limit, so search for the
		// longest limit that fits; only a checked limit is ever returned.
		lo, hi, best := discordMinTeaser, discordOverviewLimit, -1
		for lo <= hi {
			mid := (lo + hi) / 2
			if _, ok := fits(mid); ok {
				best, lo = mid, mid+1
			} else {
				hi = mid - 1
			}
		}
		if best >= 0 {
			embeds, _ := fits(best)
			return embeds, dropped
		}
		if embeds, ok := fits(0); ok || len(entries) == 1 {
			return embeds, dropped
		}
		entries = entries[1:]
	}
}

func truncateWithEllipsis(value string, limit int) string {
	const ellipsis = "…" // 3 bytes in UTF-8; limits are byte counts
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	if limit <= len(ellipsis) {
		return value[:0]
	}
	runes := []rune(value)
	for len(runes) > 0 && len(string(runes))+len(ellipsis) > limit {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + ellipsis
}

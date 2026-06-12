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

// discordEmbed deliberately has no image, url, or thumbnail fields: in v1 the
// embed must never name an origin Discord would fetch from, because that
// origin would be the user's server URL. The v1.5 CDN proxy re-enables images
// (docs/superpowers/plans/notifications/04, "Discord" payload notes).
type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
}

type discordWebhookBody struct {
	Content  string         `json:"content,omitempty"`
	Embeds   []discordEmbed `json:"embeds"`
	Username string         `json:"username"`
}

// BuildDiscordWebhookPayload renders a delivery as a Discord webhook body.
// Pure function; enforces Discord's embed limits with the spec's truncation
// policy (description first, then drop fields right-to-left).
func BuildDiscordWebhookPayload(row DeliveryRow, test bool) ([]byte, error) {
	return json.Marshal(discordWebhookBody{
		Embeds:   []discordEmbed{buildDiscordEmbed(row, test)},
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
// bot DM, one embed per item up to Discord's 10-embed cap. Overflow keeps the
// newest items and points at the Silo inbox for the rest, mirroring the email
// digest's rendering cap.
func BuildDiscordDMPayload(rows []DeliveryRow) ([]byte, error) {
	overflow := 0
	if len(rows) > discordDMMaxEmbeds {
		overflow = len(rows) - discordDMMaxEmbeds
		rows = rows[len(rows)-discordDMMaxEmbeds:]
	}
	embeds := make([]discordEmbed, 0, len(rows))
	for _, row := range rows {
		embeds = append(embeds, buildDiscordEmbed(row, false))
	}
	body := discordDMBody{Embeds: embeds}
	if overflow > 0 {
		body.Content = fmt.Sprintf("…and %d more in your Silo inbox", overflow)
	}
	return json.Marshal(body)
}

// buildDiscordEmbed renders one delivery as a Discord embed within all of
// Discord's per-embed limits.
func buildDiscordEmbed(row DeliveryRow, test bool) discordEmbed {
	flags := parseReasonFlags(row.ReasonFlags)

	title := discordEmbedTitle(row)
	// Titles assembled from catalog metadata virtually never approach the
	// limit, but Discord hard-rejects oversized embeds, so clip as a last
	// resort even though the truncation policy prefers other fields.
	title = truncateWithEllipsis(title, discordTitleLimit)

	description := "New episode available on Silo"
	if row.Type == DeliveryTypeRequestFulfilled {
		description = "Your media request is now available on Silo"
	}
	footerText := siloSenderName
	if row.SeriesTitle != "" {
		footerText = "Silo • " + truncateWithEllipsis(row.SeriesTitle, discordFooterLimit-16)
	}
	if test {
		footerText = "Silo test notification"
	}

	fields := make([]discordEmbedField, 0, 3)
	if labels := reasonLabelList(flags); len(labels) > 0 {
		fields = append(fields, discordEmbedField{
			Name:   "Reason",
			Value:  truncateWithEllipsis(strings.Join(labels, " & "), discordFieldValueLimit),
			Inline: true,
		})
	}
	if row.Type == DeliveryTypeRequestFulfilled {
		if mediaType := requestMediaTypeLabel(row.ReasonFlags); mediaType != "" {
			fields = append(fields, discordEmbedField{Name: "Type", Value: mediaType, Inline: true})
		}
	}
	if row.SeasonNumber != nil {
		fields = append(fields, discordEmbedField{
			Name:   "Season",
			Value:  fmt.Sprintf("%d", *row.SeasonNumber),
			Inline: true,
		})
	}
	if row.EpisodeNumber != nil {
		fields = append(fields, discordEmbedField{
			Name:   "Episode",
			Value:  fmt.Sprintf("%d", *row.EpisodeNumber),
			Inline: true,
		})
	}

	embed := discordEmbed{
		Title:       title,
		Description: description,
		Color:       discordEmbedColor(flags),
		Footer:      &discordEmbedFooter{Text: footerText},
		Fields:      fields,
	}
	if !row.CreatedAt.IsZero() {
		embed.Timestamp = row.CreatedAt.UTC().Format(time.RFC3339)
	}
	enforceDiscordTotalLimit(&embed)
	return embed
}

func discordEmbedTitle(row DeliveryRow) string {
	switch row.Type {
	case DeliveryTypeRequestFulfilled:
		if row.SeriesTitle != "" {
			return row.SeriesTitle
		}
		return "Request fulfilled"
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

// requestMediaTypeLabel renders a request.fulfilled delivery's media type as
// a display label; unknown values render nothing.
func requestMediaTypeLabel(reasonFlags []byte) string {
	return mediaTypeLabel(parseRequestFulfilledFlags(reasonFlags).MediaType)
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

func discordEmbedTotal(embed *discordEmbed) int {
	total := len(embed.Title) + len(embed.Description)
	if embed.Footer != nil {
		total += len(embed.Footer.Text)
	}
	for _, field := range embed.Fields {
		total += len(field.Name) + len(field.Value)
	}
	return total
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

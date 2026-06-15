package notifications

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// siloSenderName labels Discord posts and embed footers.
const siloSenderName = "Silo"

// Server-channel embed accent colors (decimal RGB).
const (
	serverChannelColorContent   = 5814783  // blurple — new content posts
	serverChannelColorSubmitted = 15844367 // gold — request submitted
	serverChannelColorApproved  = 3066993  // green — request approved
	serverChannelColorDeclined  = 15158332 // red — request declined
	serverChannelColorFulfilled = 5814783  // blurple — request fulfilled
)

// ContentMeta is the display metadata for one catalog item (series or
// movie), fetched in one batched query by the sweep worker. Only metadata a
// Discord embed renders is carried; everything else stays in the catalog.
type ContentMeta struct {
	Title      string
	Year       int
	Type       string // media_items.type: "movie" | "series"
	Overview   string
	PosterPath string
	// PosterSourcePath is the provider-origin path preserved when image
	// caching rewrote PosterPath to a local storage key.
	PosterSourcePath string
	// PosterURL is the fetchable poster URL chosen by the sweep worker
	// (System.discordPosterURL); empty renders the embed without an image.
	PosterURL     string
	Genres        []string
	ContentRating string
	RatingIMDB    float64
	RatingTMDB    float64
	IMDBID        string
	TMDBID        string
	TVDBID        string
}

func (m ContentMeta) providerIDs() providerIDs {
	return providerIDs{MediaType: m.Type, IMDB: m.IMDBID, TMDB: m.TMDBID, TVDB: m.TVDBID}
}

// ContentGroup is one rendered unit of a content digest: a movie, or every
// new episode of one series in the batch.
type ContentGroup struct {
	Kind      string // EventKindEpisode | EventKindMovie
	LibraryID int
	// Episode groups.
	SeriesID string
	Episodes []ReleaseEvent // ascending episode_key
	// Movie groups.
	ItemID string
	// Meta describes the series (episode groups) or the movie itself, with
	// Title already defaulted when the catalog row is missing.
	Meta ContentMeta
}

// GroupContentEvents folds a batch of release events into display groups:
// episodes group per (library, series) so a season pack renders as one line,
// movies render individually but dedupe by item across libraries. Group order
// follows first appearance in the batch (sweep order). metas is keyed by
// series_id / item_id; missing entries fall back to generic labels.
func GroupContentEvents(events []ReleaseEvent, metas map[string]ContentMeta) []ContentGroup {
	type groupKey struct {
		kind      string
		libraryID int
		contentID string
	}
	index := make(map[groupKey]int)
	seenMovies := make(map[string]struct{})
	groups := make([]ContentGroup, 0, len(events))

	for _, event := range events {
		switch normalizeEventKind(event.Kind) {
		case EventKindMovie:
			// The same movie landing in two libraries (e.g. "Movies" and
			// "Movies 4K") announces once.
			if _, dup := seenMovies[event.ItemID]; dup {
				continue
			}
			seenMovies[event.ItemID] = struct{}{}
			meta := metas[event.ItemID]
			if meta.Title == "" {
				meta.Title = "New movie"
			}
			groups = append(groups, ContentGroup{
				Kind:      EventKindMovie,
				LibraryID: event.LibraryID,
				ItemID:    event.ItemID,
				Meta:      meta,
			})
		default:
			key := groupKey{EventKindEpisode, event.LibraryID, event.SeriesID}
			if at, ok := index[key]; ok {
				groups[at].Episodes = append(groups[at].Episodes, event)
				continue
			}
			meta := metas[event.SeriesID]
			if meta.Title == "" {
				meta.Title = genericEpisodeTitle
			}
			index[key] = len(groups)
			groups = append(groups, ContentGroup{
				Kind:      EventKindEpisode,
				LibraryID: event.LibraryID,
				SeriesID:  event.SeriesID,
				Episodes:  []ReleaseEvent{event},
				Meta:      meta,
			})
		}
	}
	for i := range groups {
		sort.Slice(groups[i].Episodes, func(a, b int) bool {
			return groups[i].Episodes[a].EpisodeKey < groups[i].Episodes[b].EpisodeKey
		})
	}
	return groups
}

// eventEpisodeCode renders one release event's "S2 E05" code.
func eventEpisodeCode(event ReleaseEvent) string {
	return fmt.Sprintf("S%d E%d", event.SeasonNumber, event.EpisodeNumber)
}

// episodeRangeLabel renders an episode group's span: "S2 E05" for one
// episode, "S2 E01–E03" within a season, "S1 E10 – S2 E03" across seasons.
func episodeRangeLabel(episodes []ReleaseEvent) string {
	if len(episodes) == 0 {
		return ""
	}
	first, last := episodes[0], episodes[len(episodes)-1]
	switch {
	case len(episodes) == 1:
		return eventEpisodeCode(first)
	case first.SeasonNumber == last.SeasonNumber:
		return fmt.Sprintf("S%d E%d–E%d", first.SeasonNumber, first.EpisodeNumber, last.EpisodeNumber)
	default:
		return fmt.Sprintf("%s – %s", eventEpisodeCode(first), eventEpisodeCode(last))
	}
}

// contentGroupTitle renders a group's display line.
func contentGroupTitle(group ContentGroup) string {
	switch group.Kind {
	case EventKindMovie:
		return titleWithYear(group.Meta.Title, group.Meta.Year)
	default:
		if len(group.Episodes) == 1 {
			return fmt.Sprintf("%s — %s", group.Meta.Title, episodeRangeLabel(group.Episodes))
		}
		return fmt.Sprintf("%s — %d new episodes (%s)",
			group.Meta.Title, len(group.Episodes), episodeRangeLabel(group.Episodes))
	}
}

// serverChannelMaxEmbeds caps content digests at Discord's per-message embed
// limit; generic payloads share the cap so receivers see bounded bodies.
const serverChannelMaxEmbeds = discordDMMaxEmbeds

// BuildServerChannelDiscordContent renders a content digest as a Discord
// webhook body: one embed per group up to the 10-embed cap, the newest groups
// kept, with an overflow line for the rest. Pure function.
func BuildServerChannelDiscordContent(groups []ContentGroup, test bool) ([]byte, error) {
	overflow := 0
	if len(groups) > serverChannelMaxEmbeds {
		overflow = len(groups) - serverChannelMaxEmbeds
		groups = groups[len(groups)-serverChannelMaxEmbeds:]
	}
	now := time.Now().UTC().Format(time.RFC3339)
	embeds := make([]discordEmbed, 0, len(groups))
	for _, group := range groups {
		author := "New episodes available on Silo"
		if group.Kind == EventKindMovie {
			author = "New movie available on Silo"
		} else if len(group.Episodes) == 1 {
			author = "New episode available on Silo"
		}
		ids := group.Meta.providerIDs()
		fields := make([]discordEmbedField, 0, 2)
		if rating := ratingLabel(group.Meta.RatingIMDB, group.Meta.RatingTMDB); rating != "" {
			fields = append(fields, discordEmbedField{Name: "Rating", Value: rating, Inline: true})
		}
		if genres := genresLabel(group.Meta.Genres); genres != "" {
			fields = append(fields, discordEmbedField{Name: "Genres", Value: genres, Inline: true})
		}
		embed := discordEmbed{
			Title:       truncateWithEllipsis(contentGroupTitle(group), discordTitleLimit),
			URL:         ids.titleURL(),
			Description: embedDescription(group.Meta.Overview, ids),
			Color:       serverChannelColorContent,
			Author:      &discordEmbedAuthor{Name: author},
			Footer:      &discordEmbedFooter{Text: discordEmbedFooterText(group.Meta.ContentRating, test)},
			Timestamp:   now,
			Fields:      fields,
		}
		if group.Meta.PosterURL != "" {
			embed.Thumbnail = &discordEmbedMedia{URL: group.Meta.PosterURL}
		}
		enforceDiscordTotalLimit(&embed)
		embeds = append(embeds, embed)
	}
	body := discordWebhookBody{Embeds: embeds, Username: siloSenderName}
	if overflow > 0 {
		body.Content = fmt.Sprintf("…and %d more new items on Silo", overflow)
	}
	return json.Marshal(body)
}

// serverChannelContentBody is the canonical generic-webhook JSON for a
// content digest. Like the per-profile generic payload it carries no server
// URL and no artwork URLs.
type serverChannelContentBody struct {
	Event     string                    `json:"event"`
	ChannelID string                    `json:"channel_id"`
	Timestamp string                    `json:"timestamp"`
	Version   int                       `json:"version"`
	Test      bool                      `json:"test"`
	Items     []serverChannelContentRow `json:"items"`
	// Truncated reports how many additional groups were dropped by the
	// per-post item cap; 0 means the batch is complete.
	Truncated int `json:"truncated,omitempty"`
}

type serverChannelContentRow struct {
	Kind        string `json:"kind"`
	LibraryID   int    `json:"library_id"`
	ItemID      string `json:"item_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Year        int    `json:"year,omitempty"`
	SeriesID    string `json:"series_id,omitempty"`
	SeriesTitle string `json:"series_title,omitempty"`
	// Episode span for episode groups.
	EpisodeCount int    `json:"episode_count,omitempty"`
	FirstSeason  int    `json:"first_season,omitempty"`
	FirstEpisode int    `json:"first_episode,omitempty"`
	LastSeason   int    `json:"last_season,omitempty"`
	LastEpisode  int    `json:"last_episode,omitempty"`
	EpisodeLabel string `json:"episode_label,omitempty"`
}

// BuildServerChannelGenericContent renders a content digest as canonical Silo
// JSON. Pure function.
func BuildServerChannelGenericContent(groups []ContentGroup, channelID string, test bool) ([]byte, error) {
	truncated := 0
	if len(groups) > serverChannelMaxEmbeds {
		truncated = len(groups) - serverChannelMaxEmbeds
		groups = groups[len(groups)-serverChannelMaxEmbeds:]
	}
	items := make([]serverChannelContentRow, 0, len(groups))
	for _, group := range groups {
		row := serverChannelContentRow{
			Kind:      group.Kind,
			LibraryID: group.LibraryID,
		}
		switch group.Kind {
		case EventKindMovie:
			row.ItemID = group.ItemID
			row.Title = group.Meta.Title
			row.Year = group.Meta.Year
		default:
			row.SeriesID = group.SeriesID
			row.SeriesTitle = group.Meta.Title
			row.EpisodeCount = len(group.Episodes)
			if len(group.Episodes) > 0 {
				first := group.Episodes[0]
				last := group.Episodes[len(group.Episodes)-1]
				row.FirstSeason, row.FirstEpisode = first.SeasonNumber, first.EpisodeNumber
				row.LastSeason, row.LastEpisode = last.SeasonNumber, last.EpisodeNumber
				row.EpisodeLabel = episodeRangeLabel(group.Episodes)
			}
		}
		items = append(items, row)
	}
	return json.Marshal(serverChannelContentBody{
		Event:     ServerChannelEventContentAdded,
		ChannelID: channelID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Version:   1,
		Test:      test,
		Items:     items,
		Truncated: truncated,
	})
}

// requestEventDescription maps a lifecycle event to its embed description.
func requestEventDescription(event string) string {
	switch event {
	case ServerChannelEventRequestSubmitted:
		return "New media request on Silo"
	case ServerChannelEventRequestApproved:
		return "Media request approved"
	case ServerChannelEventRequestDeclined:
		return "Media request declined"
	case ServerChannelEventRequestFulfilled:
		return "Requested media is now available on Silo"
	default:
		return genericNotificationTitle
	}
}

func requestEventColor(event string) int {
	switch event {
	case ServerChannelEventRequestSubmitted:
		return serverChannelColorSubmitted
	case ServerChannelEventRequestApproved:
		return serverChannelColorApproved
	case ServerChannelEventRequestDeclined:
		return serverChannelColorDeclined
	default:
		return serverChannelColorFulfilled
	}
}

// BuildServerChannelRequestDiscord renders one request lifecycle event as a
// Discord webhook body. Pure function.
func BuildServerChannelRequestDiscord(event string, info RequestEventInfo) ([]byte, error) {
	title := info.Title
	if title == "" {
		title = "Media request"
	}
	title = titleWithYear(title, info.Year)
	ids := providerIDs{MediaType: info.MediaType, IMDB: info.IMDBID}
	if info.TMDBID > 0 {
		ids.TMDB = fmt.Sprintf("%d", info.TMDBID)
	}
	if info.TVDBID > 0 {
		ids.TVDB = fmt.Sprintf("%d", info.TVDBID)
	}
	fields := make([]discordEmbedField, 0, 2)
	if label := mediaTypeLabel(info.MediaType); label != "" {
		fields = append(fields, discordEmbedField{Name: "Type", Value: label, Inline: true})
	}
	if info.RequesterName != "" {
		fields = append(fields, discordEmbedField{
			Name:   "Requested by",
			Value:  truncateWithEllipsis(info.RequesterName, discordFieldValueLimit),
			Inline: true,
		})
	}
	embed := discordEmbed{
		Title:       truncateWithEllipsis(title, discordTitleLimit),
		URL:         ids.titleURL(),
		Description: embedDescription(info.Overview, ids),
		Color:       requestEventColor(event),
		Author:      &discordEmbedAuthor{Name: requestEventDescription(event)},
		Footer:      &discordEmbedFooter{Text: siloSenderName},
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Fields:      fields,
	}
	// Request poster paths are raw TMDB image paths from discovery, not
	// stored catalog artwork.
	if poster := tmdbRawImageURL(info.PosterPath); poster != "" {
		embed.Thumbnail = &discordEmbedMedia{URL: poster}
	}
	enforceDiscordTotalLimit(&embed)
	body := discordWebhookBody{Embeds: []discordEmbed{embed}, Username: siloSenderName}
	// A mention only pings from message content, never from inside an embed,
	// so the tag rides above the embed; the embed field keeps the plain
	// username so the post stays readable when the user left the guild.
	if info.RequesterDiscordID != "" {
		body.Content = "<@" + info.RequesterDiscordID + ">"
		body.AllowedMentions = &discordAllowedMentions{
			Parse: []string{},
			Users: []string{info.RequesterDiscordID},
		}
	}
	return json.Marshal(body)
}

// serverChannelRequestBody is the canonical generic-webhook JSON for one
// request lifecycle event.
type serverChannelRequestBody struct {
	Event     string                      `json:"event"`
	ChannelID string                      `json:"channel_id"`
	Timestamp string                      `json:"timestamp"`
	Version   int                         `json:"version"`
	Test      bool                        `json:"test"`
	Request   serverChannelRequestPayload `json:"request"`
}

type serverChannelRequestPayload struct {
	ID            string `json:"id"`
	TMDBID        int    `json:"tmdb_id,omitempty"`
	MediaType     string `json:"media_type,omitempty"`
	Title         string `json:"title,omitempty"`
	Year          int    `json:"year,omitempty"`
	RequesterName string `json:"requester_name,omitempty"`
}

// BuildServerChannelRequestGeneric renders one request lifecycle event as
// canonical Silo JSON. Pure function.
func BuildServerChannelRequestGeneric(event string, info RequestEventInfo, channelID string) ([]byte, error) {
	return json.Marshal(serverChannelRequestBody{
		Event:     event,
		ChannelID: channelID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Version:   1,
		Request: serverChannelRequestPayload{
			ID:            info.RequestID,
			TMDBID:        info.TMDBID,
			MediaType:     info.MediaType,
			Title:         info.Title,
			Year:          info.Year,
			RequesterName: info.RequesterName,
		},
	})
}

// mediaTypeLabel renders a request media type as a display label.
func mediaTypeLabel(mediaType string) string {
	switch mediaType {
	case mediaTypeMovie:
		return "Movie"
	case mediaTypeSeries:
		return "Series"
	default:
		return ""
	}
}

// serverChannelHeaders builds the signed delivery headers for a generic
// server-channel POST, mirroring the per-profile webhook convention
// (X-Silo-Signature follows Stripe's t=...,v1=... form).
func serverChannelHeaders(event, channelID, secret string, now time.Time, body []byte) map[string]string {
	timestamp := now.Unix()
	return map[string]string{
		"X-Silo-Event":      event,
		"X-Silo-Channel-Id": channelID,
		"X-Silo-Timestamp":  fmt.Sprintf("%d", timestamp),
		"X-Silo-Signature":  SignGenericWebhook(secret, timestamp, body),
	}
}

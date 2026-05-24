package webhooksync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

type PlexProvider struct {
	client *historyimport.PlexClient
}

func NewPlexProvider(client *historyimport.PlexClient) *PlexProvider {
	return &PlexProvider{client: client}
}

func (p *PlexProvider) ID() string { return ProviderPlex }

func (p *PlexProvider) ValidateCreateInput(input CreateConnectionInput) error {
	if strings.TrimSpace(input.ServerID) == "" || strings.TrimSpace(input.ServerName) == "" || strings.TrimSpace(input.BaseURL) == "" || strings.TrimSpace(input.AccessToken) == "" || strings.TrimSpace(input.DefaultProfileID) == "" {
		return fmt.Errorf("provider, server_id, server_name, base_url, access_token, and default_profile_id are required")
	}
	return nil
}

func (p *PlexProvider) DefaultUser(ctx context.Context, _ *Connection, input CreateConnectionInput) (string, string, bool, error) {
	account, err := p.client.GetCurrentUser(ctx, input.AccessToken)
	if err != nil {
		return "", "", false, err
	}
	return strconv.Itoa(account.ID), account.Title, true, nil
}

func (p *PlexProvider) DiscoverUsers(ctx context.Context, conn *Connection, mappings []ProfileMapping) ([]DiscoveredUser, bool, error) {
	if strings.TrimSpace(conn.BaseURL) == "" || strings.TrimSpace(conn.AccessToken) == "" {
		return mappingsToDiscoveredUsers(mappings), false, nil
	}
	accounts, err := p.client.ListAccounts(ctx, conn.BaseURL, conn.AccessToken)
	if err != nil {
		return nil, false, err
	}
	accounts = filterDiscoveredAccounts(accounts, mappings)
	result := make([]DiscoveredUser, 0, len(accounts))
	for _, a := range accounts {
		result = append(result, DiscoveredUser{
			ExternalUserID:   a.ID,
			ExternalUserName: a.Name,
		})
	}
	return result, true, nil
}

func (p *PlexProvider) ParseWebhook(ctx context.Context, conn *Connection, r *http.Request) (*CanonicalEvent, error) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		return nil, fmt.Errorf("invalid multipart request")
	}
	payloadRaw := r.FormValue("payload")
	if payloadRaw == "" {
		return nil, fmt.Errorf("missing payload")
	}
	var payload struct {
		Event   string `json:"event"`
		Account struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"Account"`
		Metadata struct {
			RatingKey string `json:"ratingKey"`
			Type      string `json:"type"`
		} `json:"Metadata"`
	}
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		return nil, fmt.Errorf("invalid Plex payload")
	}
	if payload.Event == "" || payload.Account.ID == 0 || payload.Metadata.RatingKey == "" || payload.Metadata.Type == "" {
		return nil, fmt.Errorf("invalid Plex webhook payload")
	}
	if !shouldApplyPlexWebhookEvent(payload.Event) {
		return &CanonicalEvent{
			EventKind: payload.Event,
			Summary:   "Ignored unsupported Plex event type",
			Apply:     false,
		}, nil
	}
	item, err := p.client.FetchMetadata(ctx, conn.BaseURL, conn.AccessToken, payload.Metadata.RatingKey)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return &CanonicalEvent{
			EventKind: payload.Event,
			Summary:   "Ignored Plex event because item metadata could not be resolved",
			Apply:     false,
		}, nil
	}
	var series *historyimport.PlexItem
	if item.Type == "episode" && item.GrandparentRatingKey != "" {
		series, _ = p.client.FetchMetadata(ctx, conn.BaseURL, conn.AccessToken, item.GrandparentRatingKey)
	}
	record := historyimport.NormalizePlexItem(*item, series)
	canonical := fromHistoryImportRecord(record)
	occurredAt := canonical.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
		canonical.UpdatedAt = occurredAt
	}
	return &CanonicalEvent{
		Provider:        ProviderPlex,
		ServerName:      conn.ServerName,
		OccurredAt:      occurredAt,
		Action:          ActionImportProgress,
		EventKind:       payload.Event,
		UserID:          strconv.FormatInt(payload.Account.ID, 10),
		UserName:        payload.Account.Title,
		ExternalItemID:  payload.Metadata.RatingKey,
		MediaKind:       payload.Metadata.Type,
		Completed:       canonical.Played,
		PositionSeconds: canonical.PositionSeconds,
		DurationSeconds: canonical.DurationSeconds,
		Record:          canonical,
		Summary:         "Applied Plex watch progress event",
		Apply:           true,
	}, nil
}

type EmbyProvider struct{}

func NewEmbyProvider() *EmbyProvider { return &EmbyProvider{} }

func (p *EmbyProvider) ID() string { return ProviderEmby }

func (p *EmbyProvider) ValidateCreateInput(input CreateConnectionInput) error {
	if strings.TrimSpace(input.ServerName) == "" || strings.TrimSpace(input.DefaultProfileID) == "" {
		return fmt.Errorf("provider, server_name, and default_profile_id are required")
	}
	return nil
}

func (p *EmbyProvider) DefaultUser(context.Context, *Connection, CreateConnectionInput) (string, string, bool, error) {
	return "", "", false, nil
}

func (p *EmbyProvider) DiscoverUsers(_ context.Context, _ *Connection, mappings []ProfileMapping) ([]DiscoveredUser, bool, error) {
	return mappingsToDiscoveredUsers(mappings), false, nil
}

func (p *EmbyProvider) ParseWebhook(_ context.Context, conn *Connection, r *http.Request) (*CanonicalEvent, error) {
	var payload embyWebhookPayload
	if err := decodeEmbyPayload(r, &payload); err != nil {
		return nil, err
	}
	eventName := strings.ToLower(strings.TrimSpace(payload.Event))
	if eventName == "" || payload.User.ID == "" || payload.Item.ID == "" || payload.Item.Type == "" {
		return nil, fmt.Errorf("invalid Emby webhook payload")
	}
	action, ok := embyWebhookAction(eventName)
	if !ok {
		return &CanonicalEvent{
			EventKind: eventName,
			Summary:   "Ignored unsupported Emby event type",
			Apply:     false,
		}, nil
	}
	record, mediaKind, ok := embyLikeRecord(payload.Item.Type, payload.Item.Name, payload.Item.SeriesName, payload.Item.ProductionYear, payload.Item.IndexNumber, payload.Item.ParentIndexNumber, payload.Item.RunTimeTicks, payload.PlaybackInfo.PositionTicks, payload.PlaybackInfo.PlayedToCompletion || action == ActionImportProgress && isEmbyMarkPlayedEvent(eventName), payload.Item.ProviderIDs, payload.Item.ID, payload.Date, actionAllowsSeries(action))
	if !ok {
		return &CanonicalEvent{
			EventKind: eventName,
			Action:    action,
			Summary:   "Ignored Emby event for unsupported media kind",
			Apply:     false,
		}, nil
	}
	serverName := conn.ServerName
	if strings.TrimSpace(payload.Server.Name) != "" {
		serverName = payload.Server.Name
	}
	return &CanonicalEvent{
		Provider:        ProviderEmby,
		ServerName:      serverName,
		OccurredAt:      record.UpdatedAt,
		Action:          action,
		EventKind:       eventName,
		UserID:          payload.User.ID,
		UserName:        payload.User.Name,
		ExternalItemID:  payload.Item.ID,
		MediaKind:       mediaKind,
		Completed:       record.Played,
		PositionSeconds: record.PositionSeconds,
		DurationSeconds: record.DurationSeconds,
		Record:          record,
		Summary:         "Applied Emby webhook event",
		Apply:           true,
	}, nil
}

type embyWebhookPayload struct {
	Event string    `json:"Event"`
	Date  time.Time `json:"Date"`
	User  struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
	Item struct {
		ID                string            `json:"Id"`
		Name              string            `json:"Name"`
		Type              string            `json:"Type"`
		SeriesName        string            `json:"SeriesName"`
		ProductionYear    int               `json:"ProductionYear"`
		IndexNumber       int               `json:"IndexNumber"`
		ParentIndexNumber int               `json:"ParentIndexNumber"`
		RunTimeTicks      int64             `json:"RunTimeTicks"`
		ProviderIDs       map[string]string `json:"ProviderIds"`
	} `json:"Item"`
	PlaybackInfo struct {
		PlayedToCompletion bool  `json:"PlayedToCompletion"`
		PositionTicks      int64 `json:"PositionTicks"`
	} `json:"PlaybackInfo"`
	Server struct {
		Name string `json:"Name"`
	} `json:"Server"`
}

func decodeEmbyPayload(r *http.Request, payload *embyWebhookPayload) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("invalid Emby payload")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return fmt.Errorf("invalid Emby payload")
	}

	contentType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch strings.ToLower(contentType) {
	case "", "application/json", "text/json":
		return decodeEmbyJSON(body, payload)
	case "application/x-www-form-urlencoded":
		return decodeEmbyForm(body, payload)
	case "multipart/form-data":
		boundary := params["boundary"]
		if boundary == "" {
			return fmt.Errorf("invalid Emby payload")
		}
		return decodeEmbyMultipart(body, boundary, payload)
	default:
		if err := decodeEmbyJSON(body, payload); err == nil {
			return nil
		}
		if err := decodeEmbyForm(body, payload); err == nil {
			return nil
		}
		return fmt.Errorf("invalid Emby payload")
	}
}

func decodeEmbyJSON(body []byte, payload *embyWebhookPayload) error {
	if err := json.Unmarshal(body, payload); err != nil {
		return fmt.Errorf("invalid Emby payload")
	}
	return nil
}

func decodeEmbyForm(body []byte, payload *embyWebhookPayload) error {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return fmt.Errorf("invalid Emby payload")
	}
	for _, key := range []string{"payload", "data", "json", "body"} {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return decodeEmbyJSON([]byte(value), payload)
		}
	}
	if len(values) == 1 {
		for _, vals := range values {
			if len(vals) == 1 && strings.TrimSpace(vals[0]) != "" {
				return decodeEmbyJSON([]byte(vals[0]), payload)
			}
		}
	}
	return fmt.Errorf("invalid Emby payload")
}

func decodeEmbyMultipart(body []byte, boundary string, payload *embyWebhookPayload) error {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var fallback []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid Emby payload")
		}
		partBody, err := io.ReadAll(part)
		if err != nil {
			return fmt.Errorf("invalid Emby payload")
		}
		partBody = bytes.TrimSpace(partBody)
		if len(partBody) == 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part.FormName()))
		partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if name == "payload" || name == "data" || name == "json" || name == "body" || strings.EqualFold(partType, "application/json") {
			return decodeEmbyJSON(partBody, payload)
		}
		if fallback == nil {
			fallback = partBody
		}
	}
	if len(fallback) > 0 {
		return decodeEmbyJSON(fallback, payload)
	}
	return fmt.Errorf("invalid Emby payload")
}

type JellyfinProvider struct{}

func NewJellyfinProvider() *JellyfinProvider { return &JellyfinProvider{} }

func (p *JellyfinProvider) ID() string { return ProviderJellyfin }

func (p *JellyfinProvider) ValidateCreateInput(input CreateConnectionInput) error {
	if strings.TrimSpace(input.ServerName) == "" || strings.TrimSpace(input.DefaultProfileID) == "" {
		return fmt.Errorf("provider, server_name, and default_profile_id are required")
	}
	return nil
}

func (p *JellyfinProvider) DefaultUser(context.Context, *Connection, CreateConnectionInput) (string, string, bool, error) {
	return "", "", false, nil
}

func (p *JellyfinProvider) DiscoverUsers(_ context.Context, _ *Connection, mappings []ProfileMapping) ([]DiscoveredUser, bool, error) {
	return mappingsToDiscoveredUsers(mappings), false, nil
}

func (p *JellyfinProvider) ParseWebhook(_ context.Context, conn *Connection, r *http.Request) (*CanonicalEvent, error) {
	var payload struct {
		Provider         string `json:"provider"`
		NotificationType string `json:"notification_type"`
		Timestamp        string `json:"timestamp"`
		ServerName       string `json:"server_name"`
		User             struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		Item struct {
			ID            string            `json:"id"`
			Type          string            `json:"type"`
			Name          string            `json:"name"`
			SeriesName    string            `json:"series_name"`
			Year          int               `json:"year"`
			SeasonNumber  int               `json:"season_number"`
			EpisodeNumber int               `json:"episode_number"`
			ProviderIDs   map[string]string `json:"provider_ids"`
			RuntimeTicks  int64             `json:"runtime_ticks"`
		} `json:"item"`
		Playback struct {
			PositionTicks      int64 `json:"position_ticks"`
			PlayedToCompletion bool  `json:"played_to_completion"`
			RuntimeTicks       int64 `json:"runtime_ticks"`
		} `json:"playback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid Jellyfin payload")
	}
	if payload.NotificationType != "PlaybackStop" {
		return &CanonicalEvent{
			EventKind: payload.NotificationType,
			Summary:   "Ignored Jellyfin notification because only PlaybackStop is supported",
			Apply:     false,
		}, nil
	}
	if payload.User.ID == "" || payload.Item.ID == "" || payload.Item.Type == "" {
		return nil, fmt.Errorf("invalid Jellyfin webhook payload")
	}
	occurredAt, err := time.Parse(time.RFC3339, payload.Timestamp)
	if err != nil {
		occurredAt = time.Now().UTC()
	}
	runtimeTicks := payload.Playback.RuntimeTicks
	if runtimeTicks == 0 {
		runtimeTicks = payload.Item.RuntimeTicks
	}
	record, mediaKind, ok := embyLikeRecord(payload.Item.Type, payload.Item.Name, payload.Item.SeriesName, payload.Item.Year, payload.Item.EpisodeNumber, payload.Item.SeasonNumber, runtimeTicks, payload.Playback.PositionTicks, payload.Playback.PlayedToCompletion, payload.Item.ProviderIDs, payload.Item.ID, occurredAt, false)
	if !ok {
		return &CanonicalEvent{
			EventKind: payload.NotificationType,
			Summary:   "Ignored Jellyfin event for unsupported media kind",
			Apply:     false,
		}, nil
	}
	serverName := conn.ServerName
	if strings.TrimSpace(payload.ServerName) != "" {
		serverName = payload.ServerName
	}
	return &CanonicalEvent{
		Provider:        ProviderJellyfin,
		ServerName:      serverName,
		OccurredAt:      record.UpdatedAt,
		Action:          ActionImportProgress,
		EventKind:       payload.NotificationType,
		UserID:          payload.User.ID,
		UserName:        payload.User.Name,
		ExternalItemID:  payload.Item.ID,
		MediaKind:       mediaKind,
		Completed:       record.Played,
		PositionSeconds: record.PositionSeconds,
		DurationSeconds: record.DurationSeconds,
		Record:          record,
		Summary:         "Applied Jellyfin watch progress event",
		Apply:           true,
	}, nil
}

func embyLikeRecord(itemType, itemName, seriesName string, productionYear, episodeNumber, seasonNumber int, runTimeTicks, positionTicks int64, completed bool, providerIDs map[string]string, externalID string, occurredAt time.Time, allowSeries bool) (CanonicalRecord, string, bool) {
	record := CanonicalRecord{
		ExternalID:      externalID,
		Title:           itemName,
		Year:            productionYear,
		Played:          completed,
		PositionSeconds: ticksToSeconds(positionTicks),
		DurationSeconds: ticksToSeconds(runTimeTicks),
		UpdatedAt:       occurredAt.UTC(),
	}
	record.LastPlayedAt = &record.UpdatedAt
	record.IMDbID = providerID(providerIDs, "imdb")
	record.TMDBID = providerID(providerIDs, "tmdb")
	record.TVDBID = providerID(providerIDs, "tvdb")

	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "movie":
		record.Kind = historyimport.KindMovie
		return record, "movie", true
	case "episode":
		record.Kind = historyimport.KindEpisode
		record.SeriesTitle = seriesName
		record.SeasonNumber = seasonNumber
		record.EpisodeNumber = episodeNumber
		return record, "episode", true
	case "series":
		if !allowSeries {
			return CanonicalRecord{}, "series", false
		}
		record.Kind = historyimport.KindSeries
		return record, "series", true
	default:
		return CanonicalRecord{}, strings.ToLower(strings.TrimSpace(itemType)), false
	}
}

func fromHistoryImportRecord(record historyimport.Record) CanonicalRecord {
	return CanonicalRecord{
		ExternalID:      record.ExternalID,
		Kind:            record.Kind,
		Title:           record.Title,
		Year:            record.Year,
		IMDbID:          record.IMDbID,
		TMDBID:          record.TMDBID,
		TVDBID:          record.TVDBID,
		SeriesTitle:     record.SeriesTitle,
		SeriesYear:      record.SeriesYear,
		SeriesIMDbID:    record.SeriesIMDbID,
		SeriesTMDBID:    record.SeriesTMDBID,
		SeriesTVDBID:    record.SeriesTVDBID,
		SeasonNumber:    record.SeasonNumber,
		EpisodeNumber:   record.EpisodeNumber,
		Played:          record.Played,
		LastPlayedAt:    record.LastPlayedAt,
		PositionSeconds: record.PositionSeconds,
		DurationSeconds: record.DurationSeconds,
		UpdatedAt:       record.UpdatedAt,
	}
}

func providerID(ids map[string]string, key string) string {
	for k, v := range ids {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ticksToSeconds(ticks int64) float64 {
	if ticks <= 0 {
		return 0
	}
	return float64(ticks) / 10_000_000
}

func embyWebhookAction(eventName string) (string, bool) {
	switch {
	case eventName == "playback.stop", isEmbyMarkPlayedEvent(eventName):
		return ActionImportProgress, true
	case isEmbyMarkUnplayedEvent(eventName):
		return ActionMarkUnplayed, true
	case eventName == "item.rate":
		return ActionToggleFavorite, true
	case isEmbyFavoriteRemovedEvent(eventName):
		return ActionRemoveFavorite, true
	case isEmbyFavoriteAddedEvent(eventName):
		return ActionAddFavorite, true
	default:
		return "", false
	}
}

func isEmbyMarkPlayedEvent(eventName string) bool {
	return strings.Contains(eventName, "markplayed")
}

func isEmbyMarkUnplayedEvent(eventName string) bool {
	return strings.Contains(eventName, "markunplayed")
}

func isEmbyFavoriteAddedEvent(eventName string) bool {
	return containsEmbyFavoriteToken(eventName) &&
		(strings.Contains(eventName, "add") ||
			strings.Contains(eventName, "favorited") ||
			strings.Contains(eventName, "favourited"))
}

func isEmbyFavoriteRemovedEvent(eventName string) bool {
	return containsEmbyFavoriteToken(eventName) &&
		(strings.Contains(eventName, "remove") ||
			strings.Contains(eventName, "unfavorite") ||
			strings.Contains(eventName, "unfavourite"))
}

func containsEmbyFavoriteToken(eventName string) bool {
	return strings.Contains(eventName, "favorite") || strings.Contains(eventName, "favourite")
}

func actionAllowsSeries(action string) bool {
	switch action {
	case ActionAddFavorite, ActionRemoveFavorite, ActionToggleFavorite:
		return true
	default:
		return false
	}
}

func shouldApplyPlexWebhookEvent(event string) bool {
	switch event {
	case "media.scrobble", "media.stop", "media.pause":
		return true
	default:
		return false
	}
}

func filterDiscoveredAccounts(accounts []historyimport.ExternalUser, mappings []ProfileMapping) []historyimport.ExternalUser {
	if len(accounts) == 0 {
		return nil
	}
	mappedIDs := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		mappedIDs[mapping.ExternalUserID] = struct{}{}
	}
	filtered := make([]historyimport.ExternalUser, 0, len(accounts))
	for _, account := range accounts {
		if strings.TrimSpace(account.Name) == "" {
			continue
		}
		if _, mapped := mappedIDs[account.ID]; mapped {
			filtered = append(filtered, account)
			continue
		}
		if account.Home || account.Guest || account.Restricted {
			filtered = append(filtered, account)
		}
	}
	if len(filtered) == 0 {
		fallback := make([]historyimport.ExternalUser, 0, len(accounts))
		for _, account := range accounts {
			if strings.TrimSpace(account.Name) == "" {
				continue
			}
			fallback = append(fallback, account)
		}
		return fallback
	}
	return filtered
}

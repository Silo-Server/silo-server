// Package mdblist implements the MDBList watch provider, authenticating via
// a user-supplied API key sent as the `apikey` query parameter.
package mdblist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const (
	defaultBaseURL = "https://api.mdblist.com"
	syncPageLimit  = 1000
)

type Provider struct {
	client  *http.Client
	baseURL string
}

func NewProvider(client *http.Client, baseURL string) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		client:  client,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (p *Provider) Key() string {
	return "mdblist"
}

func (p *Provider) DisplayName() string {
	return "MDBList"
}

func (p *Provider) Capabilities() watchsync.Capabilities {
	return watchsync.Capabilities{
		ImportWatched:    true,
		ImportProgress:   true,
		ExportWatched:    true,
		ExportUnwatched:  true,
		ImportFavorites:  true,
		ExportFavorites:  true,
		RemoveFavorites:  true,
		ScrobblePlayback: true,
	}
}

func (p *Provider) HistorySource() userstore.WatchHistorySource {
	return userstore.WatchHistorySourceMDBList
}

func (p *Provider) ConnectWithAPIKey(ctx context.Context, apiKey string) (watchsync.TokenSet, watchsync.ProviderAccount, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, errors.New("mdblist api key is required")
	}
	user, err := p.fetchUser(ctx, apiKey)
	if err != nil {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, err
	}
	account := user.account()
	if account.ID == "" {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, errors.New("mdblist /user response missing identity")
	}
	return watchsync.TokenSet{AccessToken: apiKey}, account, nil
}

func (p *Provider) LookupAccount(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.ProviderAccount, error) {
	user, err := p.fetchUser(ctx, conn.AccessToken)
	if err != nil {
		return watchsync.ProviderAccount{}, err
	}
	return user.account(), nil
}

// RefreshToken is a no-op: MDBList API keys don't expire on their own.
func (p *Provider) RefreshToken(_ context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.TokenSet, error) {
	return watchsync.TokenSet{AccessToken: conn.AccessToken}, nil
}

func (p *Provider) FetchWatched(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemoteWatch, error) {
	var rows []watchsync.RemoteWatch
	for offset := 0; ; {
		var payload mdblistWatchedResponse
		path := fmt.Sprintf("/sync/watched?limit=%d&offset=%d", syncPageLimit, offset)
		if err := p.do(ctx, http.MethodGet, path, conn.AccessToken, nil, &payload); err != nil {
			return nil, err
		}
		page := watchedRowsFromPayload(p.Key(), payload)
		rows = append(rows, page...)
		fetched := len(payload.Movies) + len(payload.Episodes)
		if fetched < syncPageLimit {
			break
		}
		offset += fetched
	}
	return rows, nil
}

func watchedRowsFromPayload(providerKey string, payload mdblistWatchedResponse) []watchsync.RemoteWatch {
	rows := make([]watchsync.RemoteWatch, 0, len(payload.Movies)+len(payload.Episodes))
	for _, movie := range payload.Movies {
		watchedAt := movie.WatchedAt
		if watchedAt == nil {
			continue
		}
		key := movieKey(movie.Movie.IDs)
		if key == "" {
			continue
		}
		rows = append(rows, watchsync.RemoteWatch{
			Provider:        providerKey,
			ProviderItemKey: key,
			Kind:            historyimport.KindMovie,
			Title:           movie.Movie.Title,
			Year:            movie.Movie.Year,
			IMDbID:          movie.Movie.IDs.IMDb,
			TMDBID:          intString(movie.Movie.IDs.TMDB),
			TVDBID:          intString(movie.Movie.IDs.TVDB),
			PlayCount:       1,
			LastWatchedAt:   watchedAt,
		})
	}
	for _, episode := range payload.Episodes {
		watchedAt := episode.WatchedAt
		if watchedAt == nil {
			continue
		}
		key := episodeKey(episode.Show.IDs, episode.Season, episode.Number, episode.IDs)
		if key == "" {
			continue
		}
		rows = append(rows, watchsync.RemoteWatch{
			Provider:        providerKey,
			ProviderItemKey: key,
			Kind:            historyimport.KindEpisode,
			Title:           episode.Title,
			IMDbID:          episode.IDs.IMDb,
			TMDBID:          intString(episode.IDs.TMDB),
			TVDBID:          intString(episode.IDs.TVDB),
			SeriesTitle:     episode.Show.Title,
			SeriesYear:      episode.Show.Year,
			SeriesIMDbID:    episode.Show.IDs.IMDb,
			SeriesTMDBID:    intString(episode.Show.IDs.TMDB),
			SeriesTVDBID:    intString(episode.Show.IDs.TVDB),
			SeasonNumber:    episode.Season,
			EpisodeNumber:   episode.Number,
			PlayCount:       1,
			LastWatchedAt:   watchedAt,
		})
	}
	return rows
}

func (p *Provider) FetchProgress(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemoteProgress, error) {
	var payload mdblistPlaybackResponse
	if err := p.do(ctx, http.MethodGet, "/sync/playback", conn.AccessToken, nil, &payload); err != nil {
		return nil, err
	}
	items := payload.items()
	rows := make([]watchsync.RemoteProgress, 0, len(items))
	for _, item := range items {
		// Skip actively-scrobbling sessions: they're mid-playback on another
		// device and would race with the local session if imported as resume points.
		if strings.EqualFold(item.Action, "start") {
			continue
		}
		row, ok := progressFromPlayback(p.Key(), item)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func progressFromPlayback(providerKey string, item mdblistPlaybackItem) (watchsync.RemoteProgress, bool) {
	pausedAt := item.PausedAt
	if pausedAt.IsZero() {
		pausedAt = time.Now().UTC()
	}
	if item.Episode != nil {
		key := episodeKey(item.Show.IDs, item.Episode.Season, item.Episode.Number, item.Episode.IDs)
		if key == "" {
			return watchsync.RemoteProgress{}, false
		}
		return watchsync.RemoteProgress{
			Provider:        providerKey,
			ProviderItemKey: key,
			Kind:            historyimport.KindEpisode,
			Title:           item.Episode.Title,
			IMDbID:          item.Episode.IDs.IMDb,
			TMDBID:          intString(item.Episode.IDs.TMDB),
			TVDBID:          intString(item.Episode.IDs.TVDB),
			SeriesTitle:     item.Show.Title,
			SeriesYear:      item.Show.Year,
			SeriesIMDbID:    item.Show.IDs.IMDb,
			SeriesTMDBID:    intString(item.Show.IDs.TMDB),
			SeriesTVDBID:    intString(item.Show.IDs.TVDB),
			SeasonNumber:    item.Episode.Season,
			EpisodeNumber:   item.Episode.Number,
			ProgressPercent: float64(item.Progress),
			PausedAt:        pausedAt,
		}, true
	}
	key := movieKey(item.Movie.IDs)
	if key == "" {
		return watchsync.RemoteProgress{}, false
	}
	return watchsync.RemoteProgress{
		Provider:        providerKey,
		ProviderItemKey: key,
		Kind:            historyimport.KindMovie,
		Title:           item.Movie.Title,
		Year:            item.Movie.Year,
		IMDbID:          item.Movie.IDs.IMDb,
		TMDBID:          intString(item.Movie.IDs.TMDB),
		TVDBID:          intString(item.Movie.IDs.TVDB),
		ProgressPercent: float64(item.Progress),
		PausedAt:        pausedAt,
	}, true
}

// MDBList exposes a watched snapshot rather than a per-play history, so each
// watched record surfaces once with its last_watched_at as the play timestamp.
func (p *Provider) FetchHistory(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemotePlay, error) {
	rows, err := p.FetchWatched(ctx, cfg, conn)
	if err != nil {
		return nil, err
	}
	plays := make([]watchsync.RemotePlay, 0, len(rows))
	for _, row := range rows {
		if row.LastWatchedAt == nil {
			continue
		}
		plays = append(plays, watchsync.RemotePlay{
			Provider:        row.Provider,
			ProviderItemKey: row.ProviderItemKey,
			Kind:            row.Kind,
			Title:           row.Title,
			Year:            row.Year,
			IMDbID:          row.IMDbID,
			TMDBID:          row.TMDBID,
			TVDBID:          row.TVDBID,
			SeriesTitle:     row.SeriesTitle,
			SeriesYear:      row.SeriesYear,
			SeriesIMDbID:    row.SeriesIMDbID,
			SeriesTMDBID:    row.SeriesTMDBID,
			SeriesTVDBID:    row.SeriesTVDBID,
			SeasonNumber:    row.SeasonNumber,
			EpisodeNumber:   row.EpisodeNumber,
			WatchedAt:       *row.LastWatchedAt,
		})
	}
	return plays, nil
}

func (p *Provider) ExportHistory(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, plays []watchsync.LocalPlay) (watchsync.ExportResult, error) {
	payload := buildWatchedPayload(plays, true)
	if payload.empty() {
		return watchsync.ExportResult{}, nil
	}
	return p.sendWatched(ctx, conn, plays, "/sync/watched", payload)
}

func (p *Provider) RemoveHistory(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, plays []watchsync.LocalPlay) (watchsync.ExportResult, error) {
	payload := buildWatchedPayload(plays, false)
	if payload.empty() {
		return watchsync.ExportResult{}, nil
	}
	return p.sendWatched(ctx, conn, plays, "/sync/watched/remove", payload)
}

func (p *Provider) sendWatched(ctx context.Context, conn watchsync.Connection, plays []watchsync.LocalPlay, path string, payload mdblistWatchedPayload) (watchsync.ExportResult, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode mdblist watched payload: %w", err)
	}
	if err := p.do(ctx, http.MethodPost, path, conn.AccessToken, &body, nil); err != nil {
		return watchsync.ExportResult{}, err
	}
	result := watchsync.ExportResult{Sent: make([]string, 0, len(plays))}
	for _, play := range plays {
		result.Sent = append(result.Sent, play.HistoryID)
	}
	return result, nil
}

func (p *Provider) FetchFavorites(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemoteFavorite, error) {
	now := time.Now().UTC()
	var rows []watchsync.RemoteFavorite
	for offset := 0; ; {
		var payload mdblistWatchlistResponse
		path := fmt.Sprintf("/watchlist/items?limit=%d&offset=%d", syncPageLimit, offset)
		if err := p.do(ctx, http.MethodGet, path, conn.AccessToken, nil, &payload); err != nil {
			return nil, err
		}
		rows = append(rows, watchlistRowsFromPayload(p.Key(), payload, now)...)
		fetched := len(payload.Movies) + len(payload.Shows)
		if fetched < syncPageLimit {
			break
		}
		offset += fetched
	}
	return rows, nil
}

func watchlistRowsFromPayload(providerKey string, payload mdblistWatchlistResponse, now time.Time) []watchsync.RemoteFavorite {
	rows := make([]watchsync.RemoteFavorite, 0, len(payload.Movies)+len(payload.Shows))
	for _, item := range payload.Movies {
		ids := item.preferredIDs()
		key := movieKey(ids)
		if key == "" {
			continue
		}
		rows = append(rows, watchsync.RemoteFavorite{
			Provider:        providerKey,
			ProviderItemKey: key,
			Kind:            historyimport.KindMovie,
			Title:           item.Title,
			Year:            item.ReleaseYear,
			IMDbID:          ids.IMDb,
			TMDBID:          intString(ids.TMDB),
			TVDBID:          intString(ids.TVDB),
			FavoritedAt:     now,
		})
	}
	for _, item := range payload.Shows {
		ids := item.preferredIDs()
		key := showKey(ids)
		if key == "" {
			continue
		}
		rows = append(rows, watchsync.RemoteFavorite{
			Provider:        providerKey,
			ProviderItemKey: key,
			Kind:            historyimport.KindSeries,
			Title:           item.Title,
			Year:            item.ReleaseYear,
			IMDbID:          ids.IMDb,
			TMDBID:          intString(ids.TMDB),
			TVDBID:          intString(ids.TVDB),
			FavoritedAt:     now,
		})
	}
	return rows
}

func (p *Provider) ExportFavorites(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, favorites []watchsync.LocalFavorite) (watchsync.ExportResult, error) {
	return p.sendWatchlist(ctx, conn, favorites, "/watchlist/items/add")
}

func (p *Provider) RemoveFavorites(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, favorites []watchsync.LocalFavorite) (watchsync.ExportResult, error) {
	return p.sendWatchlist(ctx, conn, favorites, "/watchlist/items/remove")
}

func (p *Provider) sendWatchlist(ctx context.Context, conn watchsync.Connection, favorites []watchsync.LocalFavorite, path string) (watchsync.ExportResult, error) {
	payload := buildWatchlistPayload(favorites)
	if len(payload.Movies) == 0 && len(payload.Shows) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode mdblist watchlist payload: %w", err)
	}
	if err := p.do(ctx, http.MethodPost, path, conn.AccessToken, &body, nil); err != nil {
		return watchsync.ExportResult{}, err
	}
	result := watchsync.ExportResult{Sent: make([]string, 0, len(favorites)*2)}
	for _, fav := range favorites {
		result.Sent = append(result.Sent, fav.MediaItemID)
	}
	return result, nil
}

func (p *Provider) Start(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/start", conn, event)
}

func (p *Provider) Pause(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/pause", conn, event)
}

func (p *Provider) Stop(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/stop", conn, event)
}

func (p *Provider) ScrobbleOrderingKey(conn watchsync.Connection, _ watchsync.ScrobbleEvent) string {
	return "mdblist:" + conn.ID
}

func (p *Provider) scrobble(ctx context.Context, path string, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	payload := buildScrobblePayload(event)
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return fmt.Errorf("encode mdblist scrobble payload: %w", err)
	}
	return p.do(ctx, http.MethodPost, path, conn.AccessToken, &body, nil)
}

func (p *Provider) fetchUser(ctx context.Context, apiKey string) (mdblistUser, error) {
	var user mdblistUser
	if err := p.do(ctx, http.MethodGet, "/user", apiKey, nil, &user); err != nil {
		return mdblistUser{}, err
	}
	return user, nil
}

func (p *Provider) do(ctx context.Context, method string, path string, apiKey string, body io.Reader, out any) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("mdblist api key is missing")
	}
	target := p.baseURL + path
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	target += separator + "apikey=" + url.QueryEscape(apiKey)

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("create mdblist request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("send mdblist request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("mdblist request %s %s rejected: status %d (check api key)", method, path, resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("mdblist request %s %s failed: status %d", method, path, resp.StatusCode)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode mdblist response: %w", err)
	}
	return nil
}

// --- ID & payload helpers ---

type mdblistIDs struct {
	IMDb    string `json:"imdb,omitempty"`
	TMDB    int    `json:"tmdb,omitempty"`
	TVDB    int    `json:"tvdb,omitempty"`
	Trakt   int    `json:"trakt,omitempty"`
	MDBList string `json:"mdblist,omitempty"`
}

func (ids *mdblistIDs) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	ids.IMDb = stringFromJSON(raw["imdb"])
	ids.TMDB = intFromJSON(raw["tmdb"])
	ids.TVDB = intFromJSON(raw["tvdb"])
	ids.Trakt = intFromJSON(raw["trakt"])
	ids.MDBList = stringFromJSON(raw["mdblist"])
	return nil
}

type mdblistMovie struct {
	Title string     `json:"title"`
	Year  int        `json:"year"`
	IDs   mdblistIDs `json:"ids"`
}

type mdblistShow struct {
	Title string     `json:"title"`
	Year  int        `json:"year"`
	IDs   mdblistIDs `json:"ids"`
}

type mdblistEpisode struct {
	Title  string     `json:"title"`
	Season int        `json:"season"`
	Number int        `json:"number"`
	IDs    mdblistIDs `json:"ids"`
}

type mdblistWatchedMovie struct {
	WatchedAt *time.Time   `json:"watched_at"`
	Movie     mdblistMovie `json:"movie"`
}

// mdblistWatchedEpisode tolerates both shapes MDBList uses for episode rows:
// season/number inlined on the row, or nested under an `episode` object.
type mdblistWatchedEpisode struct {
	WatchedAt *time.Time  `json:"watched_at"`
	Season    int         `json:"season"`
	Number    int         `json:"number"`
	Title     string      `json:"title"`
	IDs       mdblistIDs  `json:"ids"`
	Show      mdblistShow `json:"show"`
}

func (e *mdblistWatchedEpisode) UnmarshalJSON(data []byte) error {
	var raw struct {
		WatchedAt *time.Time      `json:"watched_at"`
		Season    int             `json:"season"`
		Number    int             `json:"number"`
		Title     string          `json:"title"`
		IDs       mdblistIDs      `json:"ids"`
		Show      mdblistShow     `json:"show"`
		Episode   *mdblistEpisode `json:"episode"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.WatchedAt = raw.WatchedAt
	e.Season = raw.Season
	e.Number = raw.Number
	e.Title = raw.Title
	e.IDs = raw.IDs
	e.Show = raw.Show
	if raw.Episode != nil {
		if e.Season == 0 {
			e.Season = raw.Episode.Season
		}
		if e.Number == 0 {
			e.Number = raw.Episode.Number
		}
		if e.Title == "" {
			e.Title = raw.Episode.Title
		}
		if e.IDs == (mdblistIDs{}) {
			e.IDs = raw.Episode.IDs
		}
	}
	return nil
}

type mdblistWatchedResponse struct {
	Movies   []mdblistWatchedMovie   `json:"movies"`
	Episodes []mdblistWatchedEpisode `json:"episodes"`
}

type mdblistPlaybackItem struct {
	Progress mdblistProgress `json:"progress"`
	PausedAt time.Time       `json:"paused_at"`
	Type     string          `json:"type"`
	Action   string          `json:"action"`
	Movie    mdblistMovie    `json:"movie"`
	Show     mdblistShow     `json:"show"`
	Episode  *mdblistEpisode `json:"episode"`
}

type mdblistProgress float64

func (p *mdblistProgress) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*p = 0
		return nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err == nil {
		*p = mdblistProgress(value)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("mdblist progress must be a number or numeric string")
	}
	text = strings.TrimSpace(text)
	text = strings.TrimSpace(strings.TrimSuffix(text, "%"))
	if text == "" {
		*p = 0
		return nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("parse mdblist progress: %w", err)
	}
	*p = mdblistProgress(value)
	return nil
}

// mdblistPlaybackResponse accepts both the live API shape (a flat array of
// playback items) and the OpenAPI-documented shape ({paused, scrobbling}
// arrays). Some MDBList deployments return the array form; some return the
// object form.
type mdblistPlaybackResponse struct {
	flat   []mdblistPlaybackItem
	nested struct {
		Paused     []mdblistPlaybackItem `json:"paused"`
		Scrobbling []mdblistPlaybackItem `json:"scrobbling"`
	}
}

func (r *mdblistPlaybackResponse) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return json.Unmarshal(data, &r.flat)
	}
	return json.Unmarshal(data, &r.nested)
}

func (r mdblistPlaybackResponse) items() []mdblistPlaybackItem {
	if len(r.flat) > 0 {
		return r.flat
	}
	out := make([]mdblistPlaybackItem, 0, len(r.nested.Paused)+len(r.nested.Scrobbling))
	out = append(out, r.nested.Paused...)
	for _, item := range r.nested.Scrobbling {
		if item.Action == "" {
			item.Action = "start"
		}
		out = append(out, item)
	}
	return out
}

type mdblistWatchlistItem struct {
	ID          int        `json:"id"`
	Title       string     `json:"title"`
	IMDbID      string     `json:"imdb_id"`
	TVDBID      int        `json:"tvdb_id"`
	Mediatype   string     `json:"mediatype"`
	ReleaseYear int        `json:"release_year"`
	IDs         mdblistIDs `json:"ids"`
}

func (i mdblistWatchlistItem) preferredIDs() mdblistIDs {
	ids := i.IDs
	if ids.IMDb == "" && i.IMDbID != "" {
		ids.IMDb = i.IMDbID
	}
	if ids.TVDB == 0 && i.TVDBID > 0 {
		ids.TVDB = i.TVDBID
	}
	if ids.TMDB == 0 && i.ID > 0 && (i.Mediatype == "movie" || i.Mediatype == "show") {
		// `id` on watchlist items mirrors the TMDB id for movies and shows.
		ids.TMDB = i.ID
	}
	return ids
}

type mdblistWatchlistResponse struct {
	Movies []mdblistWatchlistItem `json:"movies"`
	Shows  []mdblistWatchlistItem `json:"shows"`
}

type mdblistUser struct {
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (u mdblistUser) account() watchsync.ProviderAccount {
	id := strings.TrimSpace(u.Username)
	if u.UserID > 0 {
		id = strconv.Itoa(u.UserID)
	}
	return watchsync.ProviderAccount{ID: id, Username: u.Username}
}

type mdblistWatchedPayload struct {
	Movies   []mdblistWatchedMoviePayload   `json:"movies,omitempty"`
	Episodes []mdblistWatchedEpisodePayload `json:"episodes,omitempty"`
}

func (p mdblistWatchedPayload) empty() bool {
	return len(p.Movies) == 0 && len(p.Episodes) == 0
}

type mdblistWatchedMoviePayload struct {
	IDs       mdblistIDs `json:"ids"`
	WatchedAt string     `json:"watched_at,omitempty"`
}

type mdblistWatchedEpisodePayload struct {
	IDs  mdblistIDs `json:"ids,omitempty"`
	Show *struct {
		IDs mdblistIDs `json:"ids"`
	} `json:"show,omitempty"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	WatchedAt string `json:"watched_at,omitempty"`
}

type mdblistWatchlistPayload struct {
	Movies []mdblistWatchlistRef `json:"movies,omitempty"`
	Shows  []mdblistWatchlistRef `json:"shows,omitempty"`
}

type mdblistWatchlistRef struct {
	IDs mdblistIDs `json:"ids"`
}

func buildWatchedPayload(plays []watchsync.LocalPlay, includeWatchedAt bool) mdblistWatchedPayload {
	var payload mdblistWatchedPayload
	for _, play := range plays {
		watchedAt := ""
		if includeWatchedAt && !play.WatchedAt.IsZero() {
			watchedAt = play.WatchedAt.UTC().Format(time.RFC3339)
		}
		switch play.Kind {
		case historyimport.KindMovie:
			ids := idsFromLocal(play.IMDbID, play.TMDBID, play.TVDBID)
			if ids == (mdblistIDs{}) {
				continue
			}
			payload.Movies = append(payload.Movies, mdblistWatchedMoviePayload{
				IDs:       ids,
				WatchedAt: watchedAt,
			})
		case historyimport.KindEpisode:
			episodeIDs := idsFromLocal(play.IMDbID, play.TMDBID, play.TVDBID)
			row := mdblistWatchedEpisodePayload{
				IDs:       episodeIDs,
				Season:    play.SeasonNumber,
				Episode:   play.EpisodeNumber,
				WatchedAt: watchedAt,
			}
			showIDs := idsFromLocal(play.SeriesIMDbID, play.SeriesTMDBID, play.SeriesTVDBID)
			if showIDs != (mdblistIDs{}) {
				row.Show = &struct {
					IDs mdblistIDs `json:"ids"`
				}{IDs: showIDs}
			}
			if episodeIDs == (mdblistIDs{}) && row.Show == nil {
				continue
			}
			payload.Episodes = append(payload.Episodes, row)
		}
	}
	return payload
}

func buildWatchlistPayload(favorites []watchsync.LocalFavorite) mdblistWatchlistPayload {
	var payload mdblistWatchlistPayload
	for _, fav := range favorites {
		ids := idsFromLocal(fav.IMDbID, fav.TMDBID, fav.TVDBID)
		if ids == (mdblistIDs{}) {
			ids = idsFromProviderItemKey(fav.ProviderItemKey)
		}
		if ids == (mdblistIDs{}) {
			continue
		}
		ref := mdblistWatchlistRef{IDs: ids}
		switch fav.Kind {
		case historyimport.KindMovie:
			payload.Movies = append(payload.Movies, ref)
		case historyimport.KindSeries:
			payload.Shows = append(payload.Shows, ref)
		}
	}
	return payload
}

func buildScrobblePayload(event watchsync.ScrobbleEvent) map[string]any {
	progress := 0.0
	if event.DurationSeconds > 0 {
		progress = event.PositionSeconds / event.DurationSeconds * 100
	}
	if progress > 100 {
		progress = 100
	}
	if progress < 0 {
		progress = 0
	}
	payload := map[string]any{"progress": progress}
	switch event.Kind {
	case historyimport.KindEpisode:
		showIDs := idsFromLocal(event.SeriesIMDbID, event.SeriesTMDBID, event.SeriesTVDBID)
		if showIDs == (mdblistIDs{}) {
			showIDs = idsFromLocal(event.IMDbID, event.TMDBID, event.TVDBID)
		}
		payload["show"] = map[string]any{"ids": showIDs}
		payload["season"] = event.SeasonNumber
		payload["episode"] = event.EpisodeNumber
	default:
		payload["movie"] = map[string]any{"ids": idsFromLocal(event.IMDbID, event.TMDBID, event.TVDBID)}
	}
	return payload
}

func idsFromLocal(imdbID, tmdbID, tvdbID string) mdblistIDs {
	return mdblistIDs{IMDb: imdbID, TMDB: parseInt(tmdbID), TVDB: parseInt(tvdbID)}
}

func idsFromProviderItemKey(key string) mdblistIDs {
	prefix, value, ok := strings.Cut(key, ":")
	if !ok || value == "" {
		return mdblistIDs{}
	}
	switch prefix {
	case "imdb":
		return mdblistIDs{IMDb: value}
	case "tmdb":
		return mdblistIDs{TMDB: parseInt(value)}
	case "tvdb":
		return mdblistIDs{TVDB: parseInt(value)}
	case "mdblist":
		return mdblistIDs{MDBList: value}
	default:
		return mdblistIDs{}
	}
}

func movieKey(ids mdblistIDs) string {
	switch {
	case ids.IMDb != "":
		return "imdb:" + ids.IMDb
	case ids.TMDB > 0:
		return "tmdb:" + strconv.Itoa(ids.TMDB)
	case ids.TVDB > 0:
		return "tvdb:" + strconv.Itoa(ids.TVDB)
	case ids.MDBList != "":
		return "mdblist:" + ids.MDBList
	default:
		return ""
	}
}

func showKey(ids mdblistIDs) string {
	switch {
	case ids.TVDB > 0:
		return "tvdb:" + strconv.Itoa(ids.TVDB)
	case ids.TMDB > 0:
		return "tmdb:" + strconv.Itoa(ids.TMDB)
	case ids.IMDb != "":
		return "imdb:" + ids.IMDb
	case ids.MDBList != "":
		return "mdblist:" + ids.MDBList
	default:
		return ""
	}
}

func episodeKey(showIDs mdblistIDs, season, episode int, episodeIDs mdblistIDs) string {
	switch {
	case episodeIDs.TVDB > 0:
		return "tvdb:" + strconv.Itoa(episodeIDs.TVDB)
	case episodeIDs.TMDB > 0:
		return "tmdb:" + strconv.Itoa(episodeIDs.TMDB)
	case episodeIDs.IMDb != "":
		return "imdb:" + episodeIDs.IMDb
	case showIDs.TVDB > 0:
		return fmt.Sprintf("show:tvdb:%d:s%d:e%d", showIDs.TVDB, season, episode)
	case showIDs.TMDB > 0:
		return fmt.Sprintf("show:tmdb:%d:s%d:e%d", showIDs.TMDB, season, episode)
	case showIDs.IMDb != "":
		return fmt.Sprintf("show:imdb:%s:s%d:e%d", showIDs.IMDb, season, episode)
	default:
		return ""
	}
}

func intString(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func intFromJSON(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case string:
		parsed, _ := strconv.Atoi(v)
		return parsed
	default:
		return 0
	}
}

func stringFromJSON(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const defaultBaseURL = "https://api.trakt.tv"

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
	return "trakt"
}

func (p *Provider) DisplayName() string {
	return "Trakt"
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
		ImportWatchlist:  true,
		ExportWatchlist:  true,
		RemoveWatchlist:  true,
		ScrobblePlayback: true,
	}
}

func (p *Provider) HistorySource() userstore.WatchHistorySource {
	return userstore.WatchHistorySourceTrakt
}

func (p *Provider) StartDeviceAuth(
	ctx context.Context,
	cfg watchsync.ServerConfig,
) (watchsync.DeviceAuthSession, error) {
	if !cfg.Configured() {
		return watchsync.DeviceAuthSession{}, errors.New("trakt server config is not configured")
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]string{
		"client_id": cfg.ClientID,
	}); err != nil {
		return watchsync.DeviceAuthSession{}, fmt.Errorf("encode trakt device auth request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.baseURL+"/oauth/device/code",
		&body,
	)
	if err != nil {
		return watchsync.DeviceAuthSession{}, fmt.Errorf("create trakt device auth request: %w", err)
	}
	p.addHeaders(req, cfg, "")

	resp, err := p.client.Do(req)
	if err != nil {
		return watchsync.DeviceAuthSession{}, fmt.Errorf("send trakt device auth request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return watchsync.DeviceAuthSession{}, fmt.Errorf("trakt device auth request failed: status %d", resp.StatusCode)
	}

	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return watchsync.DeviceAuthSession{}, fmt.Errorf("decode trakt device auth response: %w", err)
	}
	if response.DeviceCode == "" || response.UserCode == "" || response.VerificationURL == "" ||
		response.ExpiresIn <= 0 || response.Interval <= 0 {
		return watchsync.DeviceAuthSession{}, errors.New("trakt device auth response is missing required fields")
	}

	return watchsync.DeviceAuthSession{
		Provider:        p.Key(),
		DeviceCode:      response.DeviceCode,
		UserCode:        response.UserCode,
		VerificationURL: response.VerificationURL,
		IntervalSeconds: response.Interval,
		ExpiresAt:       time.Now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second),
	}, nil
}

func (p *Provider) addHeaders(req *http.Request, cfg watchsync.ServerConfig, token string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", cfg.ClientID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (p *Provider) PollDeviceAuth(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	session watchsync.DeviceAuthSession,
) (watchsync.TokenSet, error) {
	if !cfg.Configured() {
		return watchsync.TokenSet{}, errors.New("trakt server config is not configured")
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]string{
		"code":          session.DeviceCode,
		"client_id":     cfg.ClientID,
		"client_secret": cfg.ClientSecret,
	}); err != nil {
		return watchsync.TokenSet{}, fmt.Errorf("encode trakt device token request: %w", err)
	}
	var response tokenResponse
	if err := p.do(ctx, http.MethodPost, "/oauth/device/token", cfg, "", &body, &response); err != nil {
		return watchsync.TokenSet{}, err
	}
	return response.tokenSet(), nil
}

func (p *Provider) RefreshToken(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) (watchsync.TokenSet, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]string{
		"refresh_token": conn.RefreshToken,
		"client_id":     cfg.ClientID,
		"client_secret": cfg.ClientSecret,
		"grant_type":    "refresh_token",
	}); err != nil {
		return watchsync.TokenSet{}, fmt.Errorf("encode trakt refresh request: %w", err)
	}
	var response tokenResponse
	if err := p.do(ctx, http.MethodPost, "/oauth/token", cfg, "", &body, &response); err != nil {
		return watchsync.TokenSet{}, err
	}
	return response.tokenSet(), nil
}

func (p *Provider) LookupAccount(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) (watchsync.ProviderAccount, error) {
	var response struct {
		User struct {
			Username string `json:"username"`
			IDs      struct {
				Slug string `json:"slug"`
			} `json:"ids"`
		} `json:"user"`
	}
	if err := p.do(ctx, http.MethodGet, "/users/settings", cfg, conn.AccessToken, nil, &response); err != nil {
		return watchsync.ProviderAccount{}, err
	}
	id := response.User.IDs.Slug
	if id == "" {
		id = response.User.Username
	}
	return watchsync.ProviderAccount{ID: id, Username: response.User.Username}, nil
}

func (p *Provider) FetchWatched(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) ([]watchsync.RemoteWatch, error) {
	var movies []traktWatchedMovie
	if err := p.do(ctx, http.MethodGet, "/sync/watched/movies", cfg, conn.AccessToken, nil, &movies); err != nil {
		return nil, err
	}
	var shows []traktWatchedShow
	if err := p.do(ctx, http.MethodGet, "/sync/watched/shows", cfg, conn.AccessToken, nil, &shows); err != nil {
		return nil, err
	}

	rows := make([]watchsync.RemoteWatch, 0, len(movies)+len(shows))
	for _, movie := range movies {
		watchedAt := movie.LastWatchedAt
		rows = append(rows, watchsync.RemoteWatch{
			Provider:        p.Key(),
			ProviderItemKey: movieKey(movie.Movie.IDs),
			Kind:            historyimport.KindMovie,
			Title:           movie.Movie.Title,
			Year:            movie.Movie.Year,
			IMDbID:          movie.Movie.IDs.IMDb,
			TMDBID:          intString(movie.Movie.IDs.TMDB),
			TVDBID:          intString(movie.Movie.IDs.TVDB),
			PlayCount:       movie.Plays,
			LastWatchedAt:   &watchedAt,
		})
	}
	for _, show := range shows {
		for _, season := range show.Seasons {
			for _, episode := range season.Episodes {
				watchedAt := episode.LastWatchedAt
				rows = append(rows, watchsync.RemoteWatch{
					Provider:        p.Key(),
					ProviderItemKey: episodeKey(show.Show.IDs, season.Number, episode.Number, traktIDs{}),
					Kind:            historyimport.KindEpisode,
					SeriesTitle:     show.Show.Title,
					SeriesYear:      show.Show.Year,
					SeriesIMDbID:    show.Show.IDs.IMDb,
					SeriesTMDBID:    intString(show.Show.IDs.TMDB),
					SeriesTVDBID:    intString(show.Show.IDs.TVDB),
					SeasonNumber:    season.Number,
					EpisodeNumber:   episode.Number,
					PlayCount:       episode.Plays,
					LastWatchedAt:   &watchedAt,
				})
			}
		}
	}
	return rows, nil
}

func (p *Provider) FetchProgress(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) ([]watchsync.RemoteProgress, error) {
	var payload []traktPlayback
	if err := p.do(ctx, http.MethodGet, "/sync/playback", cfg, conn.AccessToken, nil, &payload); err != nil {
		return nil, err
	}
	rows := make([]watchsync.RemoteProgress, 0, len(payload))
	for _, item := range payload {
		switch item.Type {
		case "movie":
			rows = append(rows, watchsync.RemoteProgress{
				Provider:        p.Key(),
				ProviderItemKey: movieKey(item.Movie.IDs),
				Kind:            historyimport.KindMovie,
				Title:           item.Movie.Title,
				Year:            item.Movie.Year,
				IMDbID:          item.Movie.IDs.IMDb,
				TMDBID:          intString(item.Movie.IDs.TMDB),
				TVDBID:          intString(item.Movie.IDs.TVDB),
				ProgressPercent: item.Progress,
				PausedAt:        item.PausedAt,
			})
		case "episode":
			rows = append(rows, watchsync.RemoteProgress{
				Provider:        p.Key(),
				ProviderItemKey: episodeKey(item.Show.IDs, item.Episode.Season, item.Episode.Number, item.Episode.IDs),
				Kind:            historyimport.KindEpisode,
				Title:           item.Episode.Title,
				Year:            item.Episode.Year,
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
				ProgressPercent: item.Progress,
				PausedAt:        item.PausedAt,
			})
		}
	}
	return rows, nil
}

func (p *Provider) FetchFavorites(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) ([]watchsync.RemoteFavorite, error) {
	var movies []traktFavoriteMovie
	if err := p.do(ctx, http.MethodGet, "/users/me/favorites/movies/added", cfg, conn.AccessToken, nil, &movies); err != nil {
		return nil, err
	}
	var shows []traktFavoriteShow
	if err := p.do(ctx, http.MethodGet, "/users/me/favorites/shows/added", cfg, conn.AccessToken, nil, &shows); err != nil {
		return nil, err
	}
	return p.remoteListItems(movies, shows), nil
}

// FetchWatchlist pulls Trakt's watchlist — a distinct list from favorites. The
// watchlist endpoints return the same item shape, so the mapping is shared.
func (p *Provider) FetchWatchlist(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) ([]watchsync.RemoteFavorite, error) {
	var movies []traktFavoriteMovie
	if err := p.do(ctx, http.MethodGet, "/sync/watchlist/movies", cfg, conn.AccessToken, nil, &movies); err != nil {
		return nil, err
	}
	var shows []traktFavoriteShow
	if err := p.do(ctx, http.MethodGet, "/sync/watchlist/shows", cfg, conn.AccessToken, nil, &shows); err != nil {
		return nil, err
	}
	return p.remoteListItems(movies, shows), nil
}

// remoteListItems maps Trakt movie/show list rows (favorites or watchlist) into
// the kind-neutral RemoteFavorite carrier.
func (p *Provider) remoteListItems(movies []traktFavoriteMovie, shows []traktFavoriteShow) []watchsync.RemoteFavorite {
	rows := make([]watchsync.RemoteFavorite, 0, len(movies)+len(shows))
	for _, item := range movies {
		rows = append(rows, watchsync.RemoteFavorite{
			Provider:        p.Key(),
			ProviderItemKey: movieKey(item.Movie.IDs),
			Kind:            historyimport.KindMovie,
			Title:           item.Movie.Title,
			Year:            item.Movie.Year,
			IMDbID:          item.Movie.IDs.IMDb,
			TMDBID:          intString(item.Movie.IDs.TMDB),
			TVDBID:          intString(item.Movie.IDs.TVDB),
			FavoritedAt:     item.ListedAt,
		})
	}
	for _, item := range shows {
		rows = append(rows, watchsync.RemoteFavorite{
			Provider:        p.Key(),
			ProviderItemKey: showKey(item.Show.IDs),
			Kind:            historyimport.KindSeries,
			Title:           item.Show.Title,
			Year:            item.Show.Year,
			IMDbID:          item.Show.IDs.IMDb,
			TMDBID:          intString(item.Show.IDs.TMDB),
			TVDBID:          intString(item.Show.IDs.TVDB),
			FavoritedAt:     item.ListedAt,
		})
	}
	return rows
}

func (p *Provider) FetchHistory(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) ([]watchsync.RemotePlay, error) {
	var payload []traktHistoryItem
	if err := p.do(ctx, http.MethodGet, "/sync/history", cfg, conn.AccessToken, nil, &payload); err != nil {
		return nil, err
	}
	rows := make([]watchsync.RemotePlay, 0, len(payload))
	for _, item := range payload {
		switch item.Type {
		case "movie":
			rows = append(rows, watchsync.RemotePlay{
				Provider:        p.Key(),
				ProviderItemKey: movieKey(item.Movie.IDs),
				Kind:            historyimport.KindMovie,
				Title:           item.Movie.Title,
				Year:            item.Movie.Year,
				IMDbID:          item.Movie.IDs.IMDb,
				TMDBID:          intString(item.Movie.IDs.TMDB),
				WatchedAt:       item.WatchedAt,
			})
		case "episode":
			rows = append(rows, watchsync.RemotePlay{
				Provider:        p.Key(),
				ProviderItemKey: episodeKey(item.Show.IDs, item.Episode.Season, item.Episode.Number, item.Episode.IDs),
				Kind:            historyimport.KindEpisode,
				SeriesTitle:     item.Show.Title,
				SeriesYear:      item.Show.Year,
				SeriesIMDbID:    item.Show.IDs.IMDb,
				SeriesTMDBID:    intString(item.Show.IDs.TMDB),
				SeriesTVDBID:    intString(item.Show.IDs.TVDB),
				SeasonNumber:    item.Episode.Season,
				EpisodeNumber:   item.Episode.Number,
				WatchedAt:       item.WatchedAt,
			})
		}
	}
	return rows, nil
}

func (p *Provider) ExportHistory(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	plays []watchsync.LocalPlay,
) (watchsync.ExportResult, error) {
	payload := buildHistoryPayload(plays)
	if len(payload.Movies) == 0 && len(payload.Episodes) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode trakt history payload: %w", err)
	}
	if err := p.do(ctx, http.MethodPost, "/sync/history", cfg, conn.AccessToken, &body, nil); err != nil {
		return watchsync.ExportResult{}, err
	}
	result := watchsync.ExportResult{Sent: make([]string, 0, len(plays))}
	for _, play := range plays {
		result.Sent = append(result.Sent, play.HistoryID)
	}
	return result, nil
}

func (p *Provider) ExportFavorites(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	favorites []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	payload := buildFavoritesPayload(favorites)
	if len(payload.Movies) == 0 && len(payload.Shows) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode trakt favorites payload: %w", err)
	}
	var response traktFavoritesResponse
	if err := p.do(ctx, http.MethodPost, "/sync/favorites", cfg, conn.AccessToken, &body, &response); err != nil {
		return watchsync.ExportResult{}, err
	}
	return favoriteExportResult(favorites, response.NotFound), nil
}

func (p *Provider) RemoveFavorites(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	favorites []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	payload := buildFavoritesPayload(favorites)
	if len(payload.Movies) == 0 && len(payload.Shows) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode trakt favorites remove payload: %w", err)
	}
	var response traktFavoritesResponse
	if err := p.do(ctx, http.MethodPost, "/sync/favorites/remove", cfg, conn.AccessToken, &body, &response); err != nil {
		return watchsync.ExportResult{}, err
	}
	return favoriteExportResult(favorites, response.NotFound), nil
}

// Trakt's watchlist accepts and returns the same {movies, shows} id payload as
// favorites, so the payload builder and not-found parser are shared.
func (p *Provider) ExportWatchlist(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	return p.sendWatchlist(ctx, "/sync/watchlist", cfg, conn, items)
}

func (p *Provider) RemoveWatchlist(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	return p.sendWatchlist(ctx, "/sync/watchlist/remove", cfg, conn, items)
}

func (p *Provider) sendWatchlist(
	ctx context.Context,
	path string,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	payload := buildFavoritesPayload(items)
	if len(payload.Movies) == 0 && len(payload.Shows) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode trakt watchlist payload: %w", err)
	}
	var response traktFavoritesResponse
	if err := p.do(ctx, http.MethodPost, path, cfg, conn.AccessToken, &body, &response); err != nil {
		return watchsync.ExportResult{}, err
	}
	return favoriteExportResult(items, response.NotFound), nil
}

func (p *Provider) RemoveHistory(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	plays []watchsync.LocalPlay,
) (watchsync.ExportResult, error) {
	payload := buildHistoryRemovePayload(plays)
	if len(payload.Movies) == 0 && len(payload.Episodes) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode trakt history remove payload: %w", err)
	}
	if err := p.do(ctx, http.MethodPost, "/sync/history/remove", cfg, conn.AccessToken, &body, nil); err != nil {
		return watchsync.ExportResult{}, err
	}
	result := watchsync.ExportResult{Sent: make([]string, 0, len(plays))}
	for _, play := range plays {
		result.Sent = append(result.Sent, play.HistoryID)
	}
	return result, nil
}

func (p *Provider) Start(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/start", cfg, conn, event)
}

func (p *Provider) Pause(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/pause", cfg, conn, event)
}

func (p *Provider) Stop(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/stop", cfg, conn, event)
}

func (p *Provider) scrobble(ctx context.Context, path string, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	payload := buildScrobblePayload(event)
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return fmt.Errorf("encode trakt scrobble payload: %w", err)
	}
	return p.do(ctx, http.MethodPost, path, cfg, conn.AccessToken, &body, nil)
}

func (p *Provider) do(
	ctx context.Context,
	method string,
	path string,
	cfg watchsync.ServerConfig,
	token string,
	body io.Reader,
	out any,
) error {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create trakt request: %w", err)
	}
	p.addHeaders(req, cfg, token)
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("send trakt request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("trakt request %s %s failed: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode trakt response: %w", err)
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (r tokenResponse) tokenSet() watchsync.TokenSet {
	var expires *time.Time
	if r.ExpiresIn > 0 {
		value := time.Now().UTC().Add(time.Duration(r.ExpiresIn) * time.Second)
		expires = &value
	}
	return watchsync.TokenSet{
		AccessToken:    r.AccessToken,
		RefreshToken:   r.RefreshToken,
		TokenExpiresAt: expires,
	}
}

type traktIDs struct {
	Trakt int    `json:"trakt"`
	Slug  string `json:"slug"`
	IMDb  string `json:"imdb"`
	TMDB  int    `json:"tmdb"`
	TVDB  int    `json:"tvdb"`
}

type traktMovie struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   traktIDs `json:"ids"`
}

type traktShow struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   traktIDs `json:"ids"`
}

type traktEpisode struct {
	Title  string   `json:"title"`
	Year   int      `json:"year"`
	Season int      `json:"season"`
	Number int      `json:"number"`
	IDs    traktIDs `json:"ids"`
}

type traktWatchedMovie struct {
	Plays         int        `json:"plays"`
	LastWatchedAt time.Time  `json:"last_watched_at"`
	Movie         traktMovie `json:"movie"`
}

type traktWatchedShow struct {
	Show    traktShow `json:"show"`
	Seasons []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Number        int       `json:"number"`
			Plays         int       `json:"plays"`
			LastWatchedAt time.Time `json:"last_watched_at"`
		} `json:"episodes"`
	} `json:"seasons"`
}

type traktPlayback struct {
	Type     string       `json:"type"`
	Progress float64      `json:"progress"`
	PausedAt time.Time    `json:"paused_at"`
	Movie    traktMovie   `json:"movie"`
	Show     traktShow    `json:"show"`
	Episode  traktEpisode `json:"episode"`
}

type traktHistoryItem struct {
	Type      string       `json:"type"`
	WatchedAt time.Time    `json:"watched_at"`
	Movie     traktMovie   `json:"movie"`
	Show      traktShow    `json:"show"`
	Episode   traktEpisode `json:"episode"`
}

type traktFavoriteMovie struct {
	ListedAt time.Time  `json:"listed_at"`
	Movie    traktMovie `json:"movie"`
}

type traktFavoriteShow struct {
	ListedAt time.Time `json:"listed_at"`
	Show     traktShow `json:"show"`
}

func intString(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func movieKey(ids traktIDs) string {
	switch {
	case ids.IMDb != "":
		return "imdb:" + ids.IMDb
	case ids.TMDB > 0:
		return "tmdb:" + strconv.Itoa(ids.TMDB)
	case ids.Trakt > 0:
		return "trakt:" + strconv.Itoa(ids.Trakt)
	default:
		return ""
	}
}

func showKey(ids traktIDs) string {
	switch {
	case ids.TVDB > 0:
		return "tvdb:" + strconv.Itoa(ids.TVDB)
	case ids.TMDB > 0:
		return "tmdb:" + strconv.Itoa(ids.TMDB)
	case ids.IMDb != "":
		return "imdb:" + ids.IMDb
	case ids.Trakt > 0:
		return "trakt:" + strconv.Itoa(ids.Trakt)
	default:
		return ""
	}
}

func episodeKey(showIDs traktIDs, season, episode int, episodeIDs traktIDs) string {
	switch {
	case episodeIDs.TVDB > 0:
		return "tvdb:" + strconv.Itoa(episodeIDs.TVDB)
	case episodeIDs.TMDB > 0:
		return "tmdb:" + strconv.Itoa(episodeIDs.TMDB)
	case episodeIDs.Trakt > 0:
		return "trakt:" + strconv.Itoa(episodeIDs.Trakt)
	case showIDs.TVDB > 0:
		return fmt.Sprintf("show:tvdb:%d:s%d:e%d", showIDs.TVDB, season, episode)
	case showIDs.TMDB > 0:
		return fmt.Sprintf("show:tmdb:%d:s%d:e%d", showIDs.TMDB, season, episode)
	case showIDs.IMDb != "":
		return fmt.Sprintf("show:imdb:%s:s%d:e%d", showIDs.IMDb, season, episode)
	default:
		return fmt.Sprintf("episode:s%d:e%d", season, episode)
	}
}

type traktHistoryPayload struct {
	Movies   []traktHistoryMovie   `json:"movies,omitempty"`
	Episodes []traktHistoryEpisode `json:"episodes,omitempty"`
}

type traktHistoryRemovePayload struct {
	Movies   []traktHistoryRemoveMovie   `json:"movies,omitempty"`
	Episodes []traktHistoryRemoveEpisode `json:"episodes,omitempty"`
}

type traktFavoritesPayload struct {
	Movies []traktFavoriteMoviePayload `json:"movies,omitempty"`
	Shows  []traktFavoriteShowPayload  `json:"shows,omitempty"`
}

type traktFavoriteMoviePayload struct {
	IDs traktIDs `json:"ids"`
}

type traktFavoriteShowPayload struct {
	IDs traktIDs `json:"ids"`
}

type traktFavoritesResponse struct {
	NotFound traktFavoritesPayload `json:"not_found"`
}

type traktHistoryMovie struct {
	WatchedAt string   `json:"watched_at"`
	IDs       traktIDs `json:"ids"`
}

type traktHistoryRemoveMovie struct {
	IDs traktIDs `json:"ids"`
}

type traktHistoryEpisode struct {
	WatchedAt string   `json:"watched_at"`
	IDs       traktIDs `json:"ids,omitempty"`
	Show      *struct {
		IDs traktIDs `json:"ids"`
	} `json:"show,omitempty"`
	Season int `json:"season"`
	Number int `json:"number"`
}

type traktHistoryRemoveEpisode struct {
	IDs  traktIDs `json:"ids,omitempty"`
	Show *struct {
		IDs traktIDs `json:"ids"`
	} `json:"show,omitempty"`
	Season int `json:"season"`
	Number int `json:"number"`
}

func buildHistoryPayload(plays []watchsync.LocalPlay) traktHistoryPayload {
	var payload traktHistoryPayload
	for _, play := range plays {
		watchedAt := play.WatchedAt.UTC().Format(time.RFC3339)
		switch play.Kind {
		case historyimport.KindMovie:
			ids := traktIDs{IMDb: play.IMDbID}
			if play.TVDBID != "" {
				ids.TVDB, _ = strconv.Atoi(play.TVDBID)
			}
			if play.TMDBID != "" {
				ids.TMDB, _ = strconv.Atoi(play.TMDBID)
			}
			payload.Movies = append(payload.Movies, traktHistoryMovie{WatchedAt: watchedAt, IDs: ids})
		case historyimport.KindEpisode:
			ids := traktIDs{IMDb: play.IMDbID}
			if play.TVDBID != "" {
				ids.TVDB, _ = strconv.Atoi(play.TVDBID)
			}
			if play.TMDBID != "" {
				ids.TMDB, _ = strconv.Atoi(play.TMDBID)
			}
			row := traktHistoryEpisode{WatchedAt: watchedAt, IDs: ids, Season: play.SeasonNumber, Number: play.EpisodeNumber}
			if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDb == "" {
				showIDs := traktIDs{IMDb: play.SeriesIMDbID}
				if play.SeriesTVDBID != "" {
					showIDs.TVDB, _ = strconv.Atoi(play.SeriesTVDBID)
				}
				if play.SeriesTMDBID != "" {
					showIDs.TMDB, _ = strconv.Atoi(play.SeriesTMDBID)
				}
				row.Show = &struct {
					IDs traktIDs `json:"ids"`
				}{IDs: showIDs}
			}
			payload.Episodes = append(payload.Episodes, row)
		}
	}
	return payload
}

func buildHistoryRemovePayload(plays []watchsync.LocalPlay) traktHistoryRemovePayload {
	var payload traktHistoryRemovePayload
	for _, play := range plays {
		switch play.Kind {
		case historyimport.KindMovie:
			ids := traktIDs{IMDb: play.IMDbID}
			if play.TVDBID != "" {
				ids.TVDB, _ = strconv.Atoi(play.TVDBID)
			}
			if play.TMDBID != "" {
				ids.TMDB, _ = strconv.Atoi(play.TMDBID)
			}
			payload.Movies = append(payload.Movies, traktHistoryRemoveMovie{IDs: ids})
		case historyimport.KindEpisode:
			ids := traktIDs{IMDb: play.IMDbID}
			if play.TVDBID != "" {
				ids.TVDB, _ = strconv.Atoi(play.TVDBID)
			}
			if play.TMDBID != "" {
				ids.TMDB, _ = strconv.Atoi(play.TMDBID)
			}
			row := traktHistoryRemoveEpisode{IDs: ids, Season: play.SeasonNumber, Number: play.EpisodeNumber}
			if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDb == "" {
				showIDs := traktIDs{IMDb: play.SeriesIMDbID}
				if play.SeriesTVDBID != "" {
					showIDs.TVDB, _ = strconv.Atoi(play.SeriesTVDBID)
				}
				if play.SeriesTMDBID != "" {
					showIDs.TMDB, _ = strconv.Atoi(play.SeriesTMDBID)
				}
				row.Show = &struct {
					IDs traktIDs `json:"ids"`
				}{IDs: showIDs}
			}
			payload.Episodes = append(payload.Episodes, row)
		}
	}
	return payload
}

func buildFavoritesPayload(favorites []watchsync.LocalFavorite) traktFavoritesPayload {
	var payload traktFavoritesPayload
	for _, favorite := range favorites {
		ids := traktIDs{IMDb: favorite.IMDbID, TMDB: parseInt(favorite.TMDBID), TVDB: parseInt(favorite.TVDBID)}
		if ids.IMDb == "" && ids.TMDB == 0 && ids.TVDB == 0 {
			ids = idsFromProviderItemKey(favorite.ProviderItemKey)
		}
		if ids.IMDb == "" && ids.TMDB == 0 && ids.TVDB == 0 {
			continue
		}
		switch favorite.Kind {
		case historyimport.KindMovie:
			payload.Movies = append(payload.Movies, traktFavoriteMoviePayload{IDs: ids})
		case historyimport.KindSeries:
			payload.Shows = append(payload.Shows, traktFavoriteShowPayload{IDs: ids})
		}
	}
	return payload
}

func favoriteExportResult(favorites []watchsync.LocalFavorite, notFound traktFavoritesPayload) watchsync.ExportResult {
	result := watchsync.ExportResult{Sent: make([]string, 0, len(favorites))}
	notFoundKeys := map[string]bool{}
	for _, movie := range notFound.Movies {
		notFoundKeys[movieKey(movie.IDs)] = true
	}
	for _, show := range notFound.Shows {
		notFoundKeys[showKey(show.IDs)] = true
	}
	for _, favorite := range favorites {
		key := favorite.ProviderItemKey
		if key == "" {
			key = favoriteKey(favorite)
		}
		if key == "" {
			continue
		}
		if notFoundKeys[key] {
			result.NotFound = append(result.NotFound, favorite.MediaItemID, key)
			continue
		}
		result.Sent = append(result.Sent, favorite.MediaItemID, key)
	}
	return result
}

func favoriteKey(favorite watchsync.LocalFavorite) string {
	ids := traktIDs{IMDb: favorite.IMDbID, TMDB: parseInt(favorite.TMDBID), TVDB: parseInt(favorite.TVDBID)}
	if ids.IMDb == "" && ids.TMDB == 0 && ids.TVDB == 0 {
		ids = idsFromProviderItemKey(favorite.ProviderItemKey)
	}
	if favorite.Kind == historyimport.KindSeries {
		return showKey(ids)
	}
	return movieKey(ids)
}

func idsFromProviderItemKey(key string) traktIDs {
	prefix, value, ok := strings.Cut(key, ":")
	if !ok || value == "" {
		return traktIDs{}
	}
	switch prefix {
	case "imdb":
		return traktIDs{IMDb: value}
	case "tmdb":
		return traktIDs{TMDB: parseInt(value)}
	case "tvdb":
		return traktIDs{TVDB: parseInt(value)}
	case "trakt":
		return traktIDs{Trakt: parseInt(value)}
	default:
		return traktIDs{}
	}
}

func buildScrobblePayload(event watchsync.ScrobbleEvent) map[string]any {
	progress := 0.0
	if event.DurationSeconds > 0 {
		progress = event.PositionSeconds / event.DurationSeconds * 100
	}
	payload := map[string]any{
		"progress":    progress,
		"app_version": "Silo",
		"app_date":    time.Now().UTC().Format("2006-01-02"),
	}
	switch event.Kind {
	case historyimport.KindEpisode:
		ids := traktIDs{IMDb: event.IMDbID, TMDB: parseInt(event.TMDBID), TVDB: parseInt(event.TVDBID)}
		if ids.IMDb != "" || ids.TMDB > 0 || ids.TVDB > 0 {
			payload["episode"] = map[string]any{"ids": ids}
		} else {
			payload["show"] = map[string]any{"ids": traktIDs{IMDb: event.SeriesIMDbID, TMDB: parseInt(event.SeriesTMDBID), TVDB: parseInt(event.SeriesTVDBID)}}
			payload["episode"] = map[string]any{"season": event.SeasonNumber, "number": event.EpisodeNumber}
		}
	default:
		payload["movie"] = map[string]any{"ids": traktIDs{IMDb: event.IMDbID, TMDB: parseInt(event.TMDBID), TVDB: parseInt(event.TVDBID)}}
	}
	return payload
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

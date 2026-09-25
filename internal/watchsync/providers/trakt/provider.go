package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const defaultBaseURL = "https://api.trakt.tv"

// traktExtendedProgress asks watched shows for per-episode season progress.
const traktExtendedProgress = "progress"

// Trakt rate limits, from its API rate-limiting guide: authenticated users get
// one POST/PUT/DELETE per second (AUTHED_API_POST_LIMIT) and 500 GETs per
// five minutes (AUTHED_API_GET_LIMIT). Writes are paced to one per second.
// Paged reads, which a large history can stretch to hundreds of pages (read
// twice for consistency), are paced so any five-minute window stays inside the
// GET budget: a burst of 50 covers ordinary accounts at full speed, and the
// refill keeps burst plus five minutes of refill under 500.
const (
	writeInterval = time.Second
	writeBurst    = 1
	pageInterval  = 675 * time.Millisecond
	pageBurst     = 50

	// A 429 whose Retry-After is this short, which is typical of the
	// one-second write limit, is retried in place. Longer waits defer the
	// connection instead of holding a sync run or scrobble open.
	maxInPlaceRetryWait = 10 * time.Second
	maxRetryAttempts    = 2

	// Trakt's limiter sends Retry-After, but 429s from its security layer may
	// not. Without a hint, wait out one full window of the longest documented
	// bucket (AUTHED_API_GET_LIMIT, 300 seconds) so whichever bucket tripped
	// has reset. Trakt has no daily quota that would call for longer.
	defaultRetryAfter = 5 * time.Minute
)

type Provider struct {
	client  *http.Client
	baseURL string
	// writes paces authenticated writes per access token.
	writes *watchsync.CredentialLimiter
	// pages paces paginated reads per Trakt account (see pageLimiterKey).
	pages *watchsync.CredentialLimiter
	// sleep waits between in-place rate-limit retries; tests replace it.
	sleep func(context.Context, time.Duration) error
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
		writes:  watchsync.NewCredentialLimiter(writeInterval, writeBurst),
		pages:   watchsync.NewCredentialLimiter(pageInterval, pageBurst),
		sleep:   watchsync.SleepContext,
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
		ImportRatings:    true,
		ExportRatings:    true,
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

	if resp.StatusCode == http.StatusTooManyRequests {
		wait, ok := watchsync.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if !ok {
			wait = defaultRetryAfter
		}
		return watchsync.DeviceAuthSession{}, watchsync.RateLimitedError{Provider: p.Key(), RetryAfter: wait}
	}
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
	movies, err := fetchTraktPages[traktWatchedMovie](ctx, p, cfg, conn, "/sync/watched/movies", nil)
	if err != nil {
		return nil, err
	}
	// Season and episode watched data is no longer included by default.
	shows, err := fetchTraktPages[traktWatchedShow](ctx, p, cfg, conn, "/sync/watched/shows", url.Values{"extended": {traktExtendedProgress}})
	if err != nil {
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

const (
	// traktPageLimit is Trakt's maximum page size. Larger limits are clamped.
	traktPageLimit = 250
	// traktMaxPages bounds a listing whose last page is never detected, such
	// as a server that ignores page and sends no pagination headers.
	traktMaxPages = 1000
)

// fetchTraktPages loads every page of a paginated Trakt GET endpoint. Trakt
// serves only a short first page when page and limit are omitted, so both are
// always sent; they replace any page or limit in query, and other parameters
// such as extended are kept. A failure on any page returns an error and no
// rows, so callers never import a partial listing.
//
// Offset pages shift when the list changes mid-read, which can skip or repeat
// a row, and callers treat a skipped row as removed. A listing that spans
// several pages is therefore read twice, and the read fails unless both
// passes return the same rows; the next sync retries it.
func fetchTraktPages[T any](
	ctx context.Context,
	p *Provider,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	path string,
	query url.Values,
) ([]T, error) {
	raw, pages, err := fetchTraktPass(ctx, p, cfg, conn, path, query)
	if err != nil {
		return nil, err
	}
	if pages > 1 {
		again, _, err := fetchTraktPass(ctx, p, cfg, conn, path, query)
		if err != nil {
			return nil, err
		}
		if !slices.EqualFunc(raw, again, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
			return nil, fmt.Errorf("trakt %s changed while it was read", path)
		}
	}
	rows := make([]T, 0, len(raw))
	for _, item := range raw {
		var row T
		if err := json.Unmarshal(item, &row); err != nil {
			return nil, fmt.Errorf("decode trakt response: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// pageLimiterKey identifies whose GET budget a paged read spends. Trakt counts
// requests per user, so profiles linked to one Trakt account with different
// tokens share a budget; the token is the fallback before the account is known.
func pageLimiterKey(conn watchsync.Connection) string {
	if account := strings.TrimSpace(conn.ProviderAccountID); account != "" {
		return "account:" + account
	}
	return "token:" + conn.AccessToken
}

// fetchTraktPass reads every page of a listing once and reports how many pages
// it took. A changed X-Pagination-Item-Count between pages fails the pass.
func fetchTraktPass(
	ctx context.Context,
	p *Provider,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	path string,
	query url.Values,
) ([]json.RawMessage, int, error) {
	params := url.Values{}
	maps.Copy(params, query)
	params.Set("limit", strconv.Itoa(traktPageLimit))
	var rows []json.RawMessage
	itemCount := 0
	for page := 1; page <= traktMaxPages; page++ {
		params.Set("page", strconv.Itoa(page))
		if conn.AccessToken != "" {
			if err := p.pages.Wait(ctx, pageLimiterKey(conn)); err != nil {
				return nil, 0, watchsync.LimiterWaitError(ctx, p.Key(), pageInterval, err)
			}
		}
		var batch []json.RawMessage
		header, err := p.doWithHeader(ctx, http.MethodGet, path+"?"+params.Encode(), cfg, conn.AccessToken, nil, &batch)
		if err != nil {
			return nil, 0, err
		}
		if count, ok := positiveHeaderInt(header, "X-Pagination-Item-Count"); ok {
			if itemCount != 0 && count != itemCount {
				return nil, 0, fmt.Errorf("trakt %s changed while it was read (%d items, then %d)", path, itemCount, count)
			}
			itemCount = count
		}
		rows = append(rows, batch...)
		if lastTraktPage(header, page, len(batch)) {
			return rows, page, nil
		}
	}
	return nil, 0, fmt.Errorf("trakt %s did not reach its last page within %d pages", path, traktMaxPages)
}

// lastTraktPage reports whether page, holding items rows, ends the listing.
// X-Pagination-Page-Count is authoritative when present. Otherwise a page
// shorter than the applied X-Pagination-Limit is the last one. The requested
// limit is not a safe comparison: Trakt can apply a smaller one, particularly
// for shows with season progress, so without headers only an empty page ends
// the listing.
func lastTraktPage(header http.Header, page, items int) bool {
	if items == 0 {
		return true
	}
	if count, ok := positiveHeaderInt(header, "X-Pagination-Page-Count"); ok {
		return page >= count
	}
	if limit, ok := positiveHeaderInt(header, "X-Pagination-Limit"); ok {
		return items < limit
	}
	return false
}

func positiveHeaderInt(header http.Header, key string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(header.Get(key)))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
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
	movies, err := fetchTraktPages[traktFavoriteMovie](ctx, p, cfg, conn, "/users/me/favorites/movies/added", nil)
	if err != nil {
		return nil, err
	}
	shows, err := fetchTraktPages[traktFavoriteShow](ctx, p, cfg, conn, "/users/me/favorites/shows/added", nil)
	if err != nil {
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
	movies, err := fetchTraktPages[traktFavoriteMovie](ctx, p, cfg, conn, "/sync/watchlist/movies", nil)
	if err != nil {
		return nil, err
	}
	shows, err := fetchTraktPages[traktFavoriteShow](ctx, p, cfg, conn, "/sync/watchlist/shows", nil)
	if err != nil {
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
	// ExportWatched reconciles against every remote play, so a missing page
	// would resend plays Trakt already has; Trakt does not deduplicate them.
	payload, err := fetchTraktPages[traktHistoryItem](ctx, p, cfg, conn, "/sync/history", nil)
	if err != nil {
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
	if len(payload.Movies) == 0 && len(payload.Episodes) == 0 && len(payload.Shows) == 0 {
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
	if len(payload.Movies) == 0 && len(payload.Episodes) == 0 && len(payload.Shows) == 0 {
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
	_, err := p.doWithHeader(ctx, method, path, cfg, token, body, out)
	return err
}

// doWithHeader is do that also returns the response headers, which carry
// Trakt's X-Pagination-* values.
func (p *Provider) doWithHeader(
	ctx context.Context,
	method string,
	path string,
	cfg watchsync.ServerConfig,
	token string,
	body io.Reader,
	out any,
) (http.Header, error) {
	// Buffer the body so a rate-limited request can be replayed.
	var payload []byte
	if body != nil {
		buffered, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read trakt request body: %w", err)
		}
		payload = buffered
	}
	// Trakt's write limit is per authenticated user. The OAuth endpoints are
	// unauthenticated and count against the application instead.
	paced := token != "" && method != http.MethodGet
	for attempt := 0; ; attempt++ {
		if paced {
			if err := p.writes.Wait(ctx, token); err != nil {
				return nil, watchsync.LimiterWaitError(ctx, p.Key(), writeInterval, err)
			}
		}
		header, wait, limited, err := p.doOnce(ctx, method, path, cfg, token, payload, out)
		if !limited {
			return header, err
		}
		if attempt < maxRetryAttempts && wait <= maxInPlaceRetryWait {
			if err := p.sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		// Repeated short hints that still end in 429 are not trustworthy, so
		// back off for a full fallback window rather than the last hint.
		if attempt >= maxRetryAttempts && wait < defaultRetryAfter {
			wait = defaultRetryAfter
		}
		return nil, watchsync.RateLimitedError{Provider: p.Key(), RetryAfter: wait}
	}
}

// doOnce performs a single HTTP attempt and returns the response headers. A
// 429 reports limited with the wait from Retry-After, or defaultRetryAfter
// when the header is absent or malformed; every other outcome reports its
// error, if any.
func (p *Provider) doOnce(
	ctx context.Context,
	method string,
	path string,
	cfg watchsync.ServerConfig,
	token string,
	payload []byte,
	out any,
) (header http.Header, wait time.Duration, limited bool, err error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return nil, 0, false, fmt.Errorf("create trakt request: %w", err)
	}
	p.addHeaders(req, cfg, token)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, false, fmt.Errorf("send trakt request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		wait, ok := watchsync.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if !ok {
			wait = defaultRetryAfter
		}
		return nil, wait, true, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, 0, false, fmt.Errorf("trakt request %s %s failed: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return resp.Header, 0, false, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, 0, false, fmt.Errorf("decode trakt response: %w", err)
	}
	return resp.Header, 0, false, nil
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

// traktIDIndex matches items Trakt echoes back in a response, such as its
// not_found lists, to the request items that produced them. An echo matches an
// item when the two share ANY identifier (Trakt id, slug, IMDb, TMDB, or TVDB):
// Trakt may echo a different id subset than Silo sent, and Silo keys an item by
// its own preferred id, so comparing one derived key per side misses matches.
// Identifiers are namespaced by Silo item kind (historyimport.Kind*) because
// TMDB and TVDB number movies, shows, and episodes independently. Zero ids
// never match. Create one with traktIDIndex{}.
//
// Limitation: an echo that carries only identifiers the item lacks (for
// example a bare Trakt id for an item Silo knows only by IMDb) cannot be
// matched, so callers treat that item as accepted.
type traktIDIndex map[traktIDRef]struct{}

// ID schemes, as used in provider item keys ("tmdb:949") and traktIDRef.
const (
	idSchemeTrakt = "trakt"
	idSchemeSlug  = "slug"
	idSchemeIMDb  = "imdb"
	idSchemeTMDB  = "tmdb"
	idSchemeTVDB  = "tvdb"
)

type traktIDRef struct {
	kind   string
	scheme string
	value  string
}

// add records every non-zero identifier in ids under kind.
func (idx traktIDIndex) add(kind string, ids traktIDs) {
	for _, ref := range traktIDRefs(kind, ids) {
		idx[ref] = struct{}{}
	}
}

// matches reports whether any non-zero identifier in ids was added under kind.
func (idx traktIDIndex) matches(kind string, ids traktIDs) bool {
	for _, ref := range traktIDRefs(kind, ids) {
		if _, ok := idx[ref]; ok {
			return true
		}
	}
	return false
}

func traktIDRefs(kind string, ids traktIDs) []traktIDRef {
	refs := make([]traktIDRef, 0, 5)
	if ids.Trakt > 0 {
		refs = append(refs, traktIDRef{kind: kind, scheme: idSchemeTrakt, value: strconv.Itoa(ids.Trakt)})
	}
	if ids.Slug != "" {
		refs = append(refs, traktIDRef{kind: kind, scheme: idSchemeSlug, value: ids.Slug})
	}
	if ids.IMDb != "" {
		refs = append(refs, traktIDRef{kind: kind, scheme: idSchemeIMDb, value: ids.IMDb})
	}
	if ids.TMDB > 0 {
		refs = append(refs, traktIDRef{kind: kind, scheme: idSchemeTMDB, value: strconv.Itoa(ids.TMDB)})
	}
	if ids.TVDB > 0 {
		refs = append(refs, traktIDRef{kind: kind, scheme: idSchemeTVDB, value: strconv.Itoa(ids.TVDB)})
	}
	return refs
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
	Shows    []traktHistoryShow    `json:"shows,omitempty"`
}

type traktHistoryRemovePayload struct {
	Movies   []traktHistoryRemoveMovie   `json:"movies,omitempty"`
	Episodes []traktHistoryRemoveEpisode `json:"episodes,omitempty"`
	Shows    []traktHistoryRemoveShow    `json:"shows,omitempty"`
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

// traktHistoryEpisode addresses an episode by its OWN episode-level id in the
// flat episodes[] array. Episodes lacking their own id go through the nested
// shows[] structure instead (see traktHistoryShow).
type traktHistoryEpisode struct {
	WatchedAt string   `json:"watched_at"`
	IDs       traktIDs `json:"ids"`
}

type traktHistoryRemoveEpisode struct {
	IDs traktIDs `json:"ids"`
}

// traktHistoryShow is the nested add form: an episode is addressed by
// show ids -> season number -> episode number, which is the only shape Trakt
// accepts when the episode itself carries no external id.
type traktHistoryShow struct {
	IDs     traktIDs             `json:"ids"`
	Seasons []traktHistorySeason `json:"seasons"`
}

type traktHistorySeason struct {
	Number   int                       `json:"number"`
	Episodes []traktHistoryShowEpisode `json:"episodes"`
}

type traktHistoryShowEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at"`
}

// traktHistoryRemoveShow mirrors traktHistoryShow for /sync/history/remove,
// where watched_at is not required.
type traktHistoryRemoveShow struct {
	IDs     traktIDs                   `json:"ids"`
	Seasons []traktHistoryRemoveSeason `json:"seasons"`
}

type traktHistoryRemoveSeason struct {
	Number   int                             `json:"number"`
	Episodes []traktHistoryRemoveShowEpisode `json:"episodes"`
}

type traktHistoryRemoveShowEpisode struct {
	Number int `json:"number"`
}

// playOwnIDs builds the item's OWN external ids (movie or episode level).
func playOwnIDs(play watchsync.LocalPlay) traktIDs {
	ids := traktIDs{IMDb: play.IMDbID}
	if play.TVDBID != "" {
		ids.TVDB, _ = strconv.Atoi(play.TVDBID)
	}
	if play.TMDBID != "" {
		ids.TMDB, _ = strconv.Atoi(play.TMDBID)
	}
	return ids
}

// playSeriesIDs builds the parent show's external ids, used for the nested
// shows[] fallback when an episode has no id of its own.
func playSeriesIDs(play watchsync.LocalPlay) traktIDs {
	ids := traktIDs{IMDb: play.SeriesIMDbID}
	if play.SeriesTVDBID != "" {
		ids.TVDB, _ = strconv.Atoi(play.SeriesTVDBID)
	}
	if play.SeriesTMDBID != "" {
		ids.TMDB, _ = strconv.Atoi(play.SeriesTMDBID)
	}
	return ids
}

func hasAnyID(ids traktIDs) bool {
	return ids.TVDB != 0 || ids.TMDB != 0 || ids.IMDb != ""
}

func buildHistoryPayload(plays []watchsync.LocalPlay) traktHistoryPayload {
	var payload traktHistoryPayload
	for _, play := range plays {
		watchedAt := play.WatchedAt.UTC().Format(time.RFC3339)
		switch play.Kind {
		case historyimport.KindMovie:
			payload.Movies = append(payload.Movies, traktHistoryMovie{WatchedAt: watchedAt, IDs: playOwnIDs(play)})
		case historyimport.KindEpisode:
			if ids := playOwnIDs(play); hasAnyID(ids) {
				payload.Episodes = append(payload.Episodes, traktHistoryEpisode{WatchedAt: watchedAt, IDs: ids})
				continue
			}
			showIDs := playSeriesIDs(play)
			slog.Debug("trakt history export: episode has no episode ids, using nested show fallback",
				"show_tmdb", showIDs.TMDB, "show_tvdb", showIDs.TVDB, "show_imdb", showIDs.IMDb,
				"season", play.SeasonNumber, "number", play.EpisodeNumber)
			payload.Shows = appendNestedEpisode(payload.Shows, showIDs, play.SeasonNumber, traktHistoryShowEpisode{
				Number:    play.EpisodeNumber,
				WatchedAt: watchedAt,
			})
		}
	}
	return payload
}

func buildHistoryRemovePayload(plays []watchsync.LocalPlay) traktHistoryRemovePayload {
	var payload traktHistoryRemovePayload
	for _, play := range plays {
		switch play.Kind {
		case historyimport.KindMovie:
			payload.Movies = append(payload.Movies, traktHistoryRemoveMovie{IDs: playOwnIDs(play)})
		case historyimport.KindEpisode:
			if ids := playOwnIDs(play); hasAnyID(ids) {
				payload.Episodes = append(payload.Episodes, traktHistoryRemoveEpisode{IDs: ids})
				continue
			}
			showIDs := playSeriesIDs(play)
			slog.Debug("trakt history remove: episode has no episode ids, using nested show fallback",
				"show_tmdb", showIDs.TMDB, "show_tvdb", showIDs.TVDB, "show_imdb", showIDs.IMDb,
				"season", play.SeasonNumber, "number", play.EpisodeNumber)
			payload.Shows = appendNestedRemoveEpisode(payload.Shows, showIDs, play.SeasonNumber, traktHistoryRemoveShowEpisode{
				Number: play.EpisodeNumber,
			})
		}
	}
	return payload
}

// appendNestedEpisode inserts an episode under the nested shows[] structure,
// merging by show ids then by season number so repeated episodes of the same
// show/season collapse into a single show + season entry.
func appendNestedEpisode(shows []traktHistoryShow, showIDs traktIDs, season int, episode traktHistoryShowEpisode) []traktHistoryShow {
	idx := -1
	for i := range shows {
		if shows[i].IDs == showIDs {
			idx = i
			break
		}
	}
	if idx == -1 {
		shows = append(shows, traktHistoryShow{IDs: showIDs})
		idx = len(shows) - 1
	}
	sIdx := -1
	for i := range shows[idx].Seasons {
		if shows[idx].Seasons[i].Number == season {
			sIdx = i
			break
		}
	}
	if sIdx == -1 {
		shows[idx].Seasons = append(shows[idx].Seasons, traktHistorySeason{Number: season})
		sIdx = len(shows[idx].Seasons) - 1
	}
	shows[idx].Seasons[sIdx].Episodes = append(shows[idx].Seasons[sIdx].Episodes, episode)
	return shows
}

func appendNestedRemoveEpisode(shows []traktHistoryRemoveShow, showIDs traktIDs, season int, episode traktHistoryRemoveShowEpisode) []traktHistoryRemoveShow {
	idx := -1
	for i := range shows {
		if shows[i].IDs == showIDs {
			idx = i
			break
		}
	}
	if idx == -1 {
		shows = append(shows, traktHistoryRemoveShow{IDs: showIDs})
		idx = len(shows) - 1
	}
	sIdx := -1
	for i := range shows[idx].Seasons {
		if shows[idx].Seasons[i].Number == season {
			sIdx = i
			break
		}
	}
	if sIdx == -1 {
		shows[idx].Seasons = append(shows[idx].Seasons, traktHistoryRemoveSeason{Number: season})
		sIdx = len(shows[idx].Seasons) - 1
	}
	shows[idx].Seasons[sIdx].Episodes = append(shows[idx].Seasons[sIdx].Episodes, episode)
	return shows
}

func buildFavoritesPayload(favorites []watchsync.LocalFavorite) traktFavoritesPayload {
	var payload traktFavoritesPayload
	for _, favorite := range favorites {
		ids := favoriteIDs(favorite)
		if !sendableIDs(ids) {
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

// favoriteExportResult maps a favorites or watchlist response back to the
// request items as (MediaItemID, key) pairs. An item goes to NotFound when a
// not_found echo of the same kind shares any id with the ids it was sent with
// (see traktIDIndex for the limitation), otherwise to Sent. Items with no key
// are left out of both lists.
func favoriteExportResult(favorites []watchsync.LocalFavorite, notFound traktFavoritesPayload) watchsync.ExportResult {
	result := watchsync.ExportResult{Sent: make([]string, 0, len(favorites))}
	missing := traktIDIndex{}
	for _, movie := range notFound.Movies {
		missing.add(historyimport.KindMovie, movie.IDs)
	}
	for _, show := range notFound.Shows {
		missing.add(historyimport.KindSeries, show.IDs)
	}
	for _, favorite := range favorites {
		key := favorite.ProviderItemKey
		if key == "" {
			key = favoriteKey(favorite)
		}
		ids := favoriteIDs(favorite)
		// An item without a sendable id was left out of the request, so it
		// is neither sent nor reported missing.
		if key == "" || !sendableIDs(ids) {
			continue
		}
		if missing.matches(favorite.Kind, ids) {
			result.NotFound = append(result.NotFound, favorite.MediaItemID, key)
			continue
		}
		result.Sent = append(result.Sent, favorite.MediaItemID, key)
	}
	return result
}

// favoriteIDs returns the ids a favorite or watchlist item is sent to Trakt
// with: its own external ids, falling back to the id its provider item key
// encodes.
func favoriteIDs(favorite watchsync.LocalFavorite) traktIDs {
	ids := traktIDs{IMDb: favorite.IMDbID, TMDB: parseInt(favorite.TMDBID), TVDB: parseInt(favorite.TVDBID)}
	if ids.IMDb == "" && ids.TMDB == 0 && ids.TVDB == 0 {
		ids = idsFromProviderItemKey(favorite.ProviderItemKey)
	}
	return ids
}

// sendableIDs reports whether ids can identify a title in a Trakt sync write.
// Trakt accepts its own id as well as IMDb, TMDB, and TVDB ids.
func sendableIDs(ids traktIDs) bool {
	return hasAnyID(ids) || ids.Trakt > 0
}

func favoriteKey(favorite watchsync.LocalFavorite) string {
	ids := favoriteIDs(favorite)
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
	case idSchemeIMDb:
		return traktIDs{IMDb: value}
	case idSchemeTMDB:
		return traktIDs{TMDB: parseInt(value)}
	case idSchemeTVDB:
		return traktIDs{TVDB: parseInt(value)}
	case idSchemeTrakt:
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

package simkl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const defaultBaseURL = "https://api.simkl.com"

const (
	simklCursorInboundMoviesCompleted = "simkl.inbound.movies.completed"
	simklCursorInboundShowsWatching   = "simkl.inbound.shows.watching"
	simklCursorInboundShowsCompleted  = "simkl.inbound.shows.completed"
	simklCursorInboundAnimeWatching   = "simkl.inbound.anime.watching"
	simklCursorInboundAnimeCompleted  = "simkl.inbound.anime.completed"
	simklCursorProgressMovies         = "simkl.progress.movies"
	simklCursorProgressShows          = "simkl.progress.shows"
	simklCursorProgressAnime          = "simkl.progress.anime"

	simklCursorRemovedMovies = "simkl.inbound.movies.removed_from_list"
	simklCursorRemovedShows  = "simkl.inbound.shows.removed_from_list"
	simklCursorRemovedAnime  = "simkl.inbound.anime.removed_from_list"
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
	return &Provider{client: client, baseURL: strings.TrimRight(baseURL, "/")}
}

func (p *Provider) Key() string {
	return "simkl"
}

func (p *Provider) DisplayName() string {
	return "Simkl"
}

func (p *Provider) Capabilities() watchsync.Capabilities {
	return watchsync.Capabilities{
		ImportWatched:    true,
		ImportProgress:   true,
		ExportWatched:    true,
		ExportUnwatched:  true,
		ScrobblePlayback: true,
	}
}

func (p *Provider) HistorySource() userstore.WatchHistorySource {
	return userstore.WatchHistorySourceSimkl
}

func (p *Provider) ScrobbleOrderingKey(conn watchsync.Connection, _ watchsync.ScrobbleEvent) string {
	return "simkl:" + conn.ID
}

func (p *Provider) StartDeviceAuth(ctx context.Context, cfg watchsync.ServerConfig) (watchsync.DeviceAuthSession, error) {
	if !cfg.Configured() {
		return watchsync.DeviceAuthSession{}, errors.New("simkl server config is not configured")
	}
	var response pinCodeResponse
	if err := p.do(ctx, http.MethodGet, "/oauth/pin?client_id="+url.QueryEscape(cfg.ClientID), cfg, "", nil, &response); err != nil {
		return watchsync.DeviceAuthSession{}, err
	}
	if response.Result != "OK" || response.UserCode == "" || response.VerificationURL == "" ||
		response.ExpiresIn <= 0 || response.Interval <= 0 {
		return watchsync.DeviceAuthSession{}, errors.New("simkl pin auth response is missing required fields")
	}
	deviceCode := response.DeviceCode
	if deviceCode == "" {
		deviceCode = response.UserCode
	}
	return watchsync.DeviceAuthSession{
		Provider:        p.Key(),
		DeviceCode:      deviceCode,
		UserCode:        response.UserCode,
		VerificationURL: response.VerificationURL,
		IntervalSeconds: response.Interval,
		ExpiresAt:       time.Now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second),
	}, nil
}

func (p *Provider) PollDeviceAuth(ctx context.Context, cfg watchsync.ServerConfig, session watchsync.DeviceAuthSession) (watchsync.TokenSet, error) {
	if !cfg.Configured() {
		return watchsync.TokenSet{}, errors.New("simkl server config is not configured")
	}
	userCode := session.UserCode
	if userCode == "" {
		userCode = session.DeviceCode
	}
	var response pinStatusResponse
	path := "/oauth/pin/" + url.PathEscape(userCode) + "?client_id=" + url.QueryEscape(cfg.ClientID)
	if err := p.do(ctx, http.MethodGet, path, cfg, "", nil, &response); err != nil {
		return watchsync.TokenSet{}, err
	}
	if response.Result != "OK" || strings.TrimSpace(response.AccessToken) == "" {
		message := strings.TrimSpace(response.Message)
		if message == "" {
			message = "authorization pending"
		}
		return watchsync.TokenSet{}, fmt.Errorf("simkl pin authorization pending: %s", message)
	}
	return watchsync.TokenSet{AccessToken: strings.TrimSpace(response.AccessToken)}, nil
}

func (p *Provider) RefreshToken(_ context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.TokenSet, error) {
	if conn.TokenExpiresAt == nil {
		return watchsync.TokenSet{AccessToken: conn.AccessToken}, nil
	}
	return watchsync.TokenSet{}, errors.New("simkl access tokens do not support refresh")
}

func (p *Provider) LookupAccount(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) (watchsync.ProviderAccount, error) {
	var response struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
		Account struct {
			ID int `json:"id"`
		} `json:"account"`
	}
	if err := p.do(ctx, http.MethodPost, "/users/settings", cfg, conn.AccessToken, nil, &response); err != nil {
		return watchsync.ProviderAccount{}, err
	}
	id := strconv.Itoa(response.Account.ID)
	if response.Account.ID == 0 {
		id = response.User.Name
	}
	return watchsync.ProviderAccount{ID: id, Username: response.User.Name}, nil
}

func (p *Provider) FetchWatched(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemoteWatch, error) {
	batch, err := p.FetchWatchedBatch(ctx, cfg, conn)
	if err != nil {
		return nil, err
	}
	return batch.Rows, nil
}

func (p *Provider) FetchWatchedBatch(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) (watchsync.WatchedImportBatch, error) {
	return p.fetchWatchedBatch(ctx, cfg, conn, true)
}

func (p *Provider) fetchWatchedBatch(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, useCursors bool) (watchsync.WatchedImportBatch, error) {
	activities, err := p.fetchActivities(ctx, cfg, conn)
	if err != nil {
		return watchsync.WatchedImportBatch{}, err
	}
	batch := watchsync.WatchedImportBatch{
		UpdatedCursors: make(map[string]string),
	}
	p.addRemovedListWarning(conn, activities.Movies.RemovedFromList, simklCursorRemovedMovies, &batch)
	p.addRemovedListWarning(conn, activities.TVShows.RemovedFromList, simklCursorRemovedShows, &batch)
	p.addRemovedListWarning(conn, activities.Anime.RemovedFromList, simklCursorRemovedAnime, &batch)

	for _, bucket := range watchedBuckets(activities) {
		previous := conn.SyncCursors[bucket.cursorKey]
		if useCursors && shouldSkipSimklBucket(previous, bucket.activity) {
			continue
		}
		path := bucket.path
		if useCursors && previous != "" {
			path = appendDateFrom(path, previous)
		}
		var payload simklAllItemsResponse
		if err := p.do(ctx, http.MethodGet, path, cfg, conn.AccessToken, nil, &payload); err != nil {
			return watchsync.WatchedImportBatch{}, err
		}
		rows, warnings := watchedRowsFromAllItems(payload, bucket.allowShowTimestampFallback)
		batch.Rows = append(batch.Rows, rows...)
		batch.Warnings = append(batch.Warnings, warnings...)
		if useCursors && bucket.activity != "" {
			batch.UpdatedCursors[bucket.cursorKey] = bucket.activity
		}
	}
	return batch, nil
}

func (p *Provider) FetchProgress(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemoteProgress, error) {
	batch, err := p.FetchProgressBatch(ctx, cfg, conn)
	if err != nil {
		return nil, err
	}
	return batch.Rows, nil
}

func (p *Provider) FetchProgressBatch(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) (watchsync.ProgressImportBatch, error) {
	activities, err := p.fetchActivities(ctx, cfg, conn)
	if err != nil {
		return watchsync.ProgressImportBatch{}, err
	}
	batch := watchsync.ProgressImportBatch{
		UpdatedCursors: make(map[string]string),
	}

	moviePrevious := conn.SyncCursors[simklCursorProgressMovies]
	if !shouldSkipSimklBucket(moviePrevious, activities.Movies.Playback) {
		payload, err := p.fetchPlayback(ctx, cfg, conn, "/sync/playback/movies", moviePrevious)
		if err != nil {
			return watchsync.ProgressImportBatch{}, err
		}
		rows, warnings, _ := progressRowsFromPlayback(payload, p.Key())
		batch.Rows = append(batch.Rows, rows...)
		batch.Warnings = append(batch.Warnings, warnings...)
		if activities.Movies.Playback != "" {
			batch.UpdatedCursors[simklCursorProgressMovies] = activities.Movies.Playback
		}
	}

	showsPrevious := conn.SyncCursors[simklCursorProgressShows]
	animePrevious := conn.SyncCursors[simklCursorProgressAnime]
	showsChanged := !shouldSkipSimklBucket(showsPrevious, activities.TVShows.Playback)
	animeChanged := !shouldSkipSimklBucket(animePrevious, activities.Anime.Playback)
	if showsChanged || animeChanged {
		dateFrom := ""
		if !(showsChanged && showsPrevious == "") && !(animeChanged && animePrevious == "") {
			dateFrom = oldestCursor(showsPrevious, animePrevious)
		}
		payload, err := p.fetchPlayback(ctx, cfg, conn, "/sync/playback/episodes", dateFrom)
		if err != nil {
			return watchsync.ProgressImportBatch{}, err
		}
		rows, warnings, hasAnimeRows := progressRowsFromPlayback(payload, p.Key())
		batch.Rows = append(batch.Rows, rows...)
		batch.Warnings = append(batch.Warnings, warnings...)
		if showsChanged && activities.TVShows.Playback != "" {
			batch.UpdatedCursors[simklCursorProgressShows] = activities.TVShows.Playback
		}
		if animeChanged && activities.Anime.Playback != "" && hasAnimeRows {
			batch.UpdatedCursors[simklCursorProgressAnime] = activities.Anime.Playback
		}
	}
	return batch, nil
}

func (p *Provider) fetchPlayback(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, path string, dateFrom string) ([]simklPlayback, error) {
	if dateFrom != "" {
		path = appendDateFrom(path, dateFrom)
	}
	var payload []simklPlayback
	if err := p.do(ctx, http.MethodGet, path, cfg, conn.AccessToken, nil, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (p *Provider) FetchHistory(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) ([]watchsync.RemotePlay, error) {
	slog.WarnContext(ctx, "simkl full watched fetch used for export dedupe", "provider", p.Key())
	batch, err := p.fetchWatchedBatch(ctx, cfg, conn, false)
	if err != nil {
		return nil, err
	}
	rows := make([]watchsync.RemotePlay, 0, len(batch.Rows))
	for _, row := range batch.Rows {
		if row.LastWatchedAt == nil {
			continue
		}
		rows = append(rows, watchsync.RemotePlay{
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
	return rows, nil
}

func (p *Provider) ExportHistory(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, plays []watchsync.LocalPlay) (watchsync.ExportResult, error) {
	return p.sendHistory(ctx, cfg, conn, plays, "/sync/history", true)
}

func (p *Provider) RemoveHistory(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, plays []watchsync.LocalPlay) (watchsync.ExportResult, error) {
	return p.sendHistory(ctx, cfg, conn, plays, "/sync/history/remove", false)
}

func (p *Provider) Start(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/start", cfg, conn, event)
}

func (p *Provider) Pause(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, "/scrobble/pause", cfg, conn, event)
}

func (p *Provider) Stop(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	err := p.scrobble(ctx, "/scrobble/stop", cfg, conn, event)
	var conflict simklConflictError
	if event.Completed && errors.As(err, &conflict) {
		return nil
	}
	return err
}

func (p *Provider) fetchActivities(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) (simklActivities, error) {
	var activities simklActivities
	if err := p.do(ctx, http.MethodGet, "/sync/activities", cfg, conn.AccessToken, nil, &activities); err != nil {
		return simklActivities{}, err
	}
	return activities, nil
}

func (p *Provider) sendHistory(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, plays []watchsync.LocalPlay, path string, includeWatchedAt bool) (watchsync.ExportResult, error) {
	request := buildHistoryRequest(plays, includeWatchedAt)
	payload := request.Payload
	if len(payload.Movies) == 0 && len(payload.Shows) == 0 && len(payload.Episodes) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode simkl history payload: %w", err)
	}
	var response simklHistoryResponse
	if err := p.do(ctx, http.MethodPost, path, cfg, conn.AccessToken, &body, &response); err != nil {
		return watchsync.ExportResult{}, err
	}
	notFound := response.notFoundHistoryIDs(request.HistoryIDsByKey)
	notFoundSet := make(map[string]bool, len(notFound))
	for _, historyID := range notFound {
		notFoundSet[historyID] = true
	}
	result := watchsync.ExportResult{
		Sent:     make([]string, 0, len(plays)-len(notFoundSet)),
		NotFound: make([]string, 0, len(notFoundSet)),
	}
	for _, play := range plays {
		if play.HistoryID == "" {
			continue
		}
		if notFoundSet[play.HistoryID] {
			result.NotFound = append(result.NotFound, play.HistoryID)
			continue
		}
		result.Sent = append(result.Sent, play.HistoryID)
	}
	return result, nil
}

func (p *Provider) scrobble(ctx context.Context, path string, cfg watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	payload := buildScrobblePayload(event)
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return fmt.Errorf("encode simkl scrobble payload: %w", err)
	}
	return p.do(ctx, http.MethodPost, path, cfg, conn.AccessToken, &body, nil)
}

func (p *Provider) do(ctx context.Context, method string, path string, cfg watchsync.ServerConfig, token string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create simkl request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("simkl-api-key", cfg.ClientID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("send simkl request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return simklConflictError{method: method, path: path}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("simkl request %s %s failed: status %d", method, path, resp.StatusCode)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode simkl response: %w", err)
	}
	return nil
}

type simklConflictError struct {
	method string
	path   string
}

func (e simklConflictError) Error() string {
	return fmt.Sprintf("simkl request %s %s conflicted", e.method, e.path)
}

type pinCodeResponse struct {
	Result          string `json:"result"`
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type pinStatusResponse struct {
	Result      string `json:"result"`
	Message     string `json:"message"`
	AccessToken string `json:"access_token"`
}

type simklActivities struct {
	All     string              `json:"all"`
	Movies  simklActivityBucket `json:"movies"`
	TVShows simklActivityBucket `json:"tv_shows"`
	Anime   simklActivityBucket `json:"anime"`
}

type simklActivityBucket struct {
	All             string `json:"all"`
	Playback        string `json:"playback"`
	Watching        string `json:"watching"`
	Completed       string `json:"completed"`
	RemovedFromList string `json:"removed_from_list"`
}

type simklWatchedBucket struct {
	cursorKey                  string
	activity                   string
	path                       string
	allowShowTimestampFallback bool
}

type simklAllItemsResponse struct {
	Movies []simklMovieItem `json:"movies"`
	Shows  []simklShowItem  `json:"shows"`
	Anime  []simklShowItem  `json:"anime"`
}

type simklMovieItem struct {
	Status        string     `json:"status"`
	LastWatchedAt *time.Time `json:"last_watched_at"`
	Movie         simklMovie `json:"movie"`
}

type simklShowItem struct {
	Status        string        `json:"status"`
	LastWatchedAt *time.Time    `json:"last_watched_at"`
	Show          simklShow     `json:"show"`
	Seasons       []simklSeason `json:"seasons"`
}

type simklSeason struct {
	Number   int            `json:"number"`
	Episodes []simklEpisode `json:"episodes"`
}

type simklEpisode struct {
	Title      string `json:"title"`
	Season     int    `json:"season"`
	Number     int    `json:"number"`
	Episode    int    `json:"episode"`
	TVDBSeason int    `json:"tvdb_season"`
	TVDBNumber int    `json:"tvdb_number"`
	TVDB       struct {
		Season  int `json:"season"`
		Episode int `json:"episode"`
	} `json:"tvdb"`
	WatchedAt *time.Time `json:"watched_at"`
	IDs       simklIDs   `json:"ids"`
}

type simklPlayback struct {
	ID       int64        `json:"id"`
	Type     string       `json:"type"`
	Progress float64      `json:"progress"`
	PausedAt time.Time    `json:"paused_at"`
	Movie    simklMovie   `json:"movie"`
	Show     simklShow    `json:"show"`
	Anime    simklShow    `json:"anime"`
	Episode  simklEpisode `json:"episode"`
}

type simklMovie struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   simklIDs `json:"ids"`
}

type simklShow struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   simklIDs `json:"ids"`
}

type simklIDs struct {
	Simkl int    `json:"simkl,omitempty"`
	Slug  string `json:"slug,omitempty"`
	IMDb  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

func (ids *simklIDs) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	ids.Simkl = intFromJSON(raw["simkl"])
	ids.Slug = stringFromJSON(raw["slug"])
	ids.IMDb = stringFromJSON(raw["imdb"])
	ids.TMDB = intFromJSON(raw["tmdb"])
	ids.TVDB = intFromJSON(raw["tvdb"])
	return nil
}

type simklHistoryPayload struct {
	Movies   []simklHistoryMovie   `json:"movies,omitempty"`
	Shows    []simklHistoryShow    `json:"shows,omitempty"`
	Episodes []simklHistoryEpisode `json:"episodes,omitempty"`
}

type simklHistoryMovie struct {
	Title     string   `json:"title,omitempty"`
	Year      int      `json:"year,omitempty"`
	WatchedAt string   `json:"watched_at,omitempty"`
	IDs       simklIDs `json:"ids,omitempty"`
}

type simklHistoryShow struct {
	Title   string               `json:"title,omitempty"`
	Year    int                  `json:"year,omitempty"`
	IDs     simklIDs             `json:"ids,omitempty"`
	Seasons []simklHistorySeason `json:"seasons,omitempty"`
}

type simklHistorySeason struct {
	Number    int                   `json:"number"`
	Episodes  []simklHistoryEpisode `json:"episodes,omitempty"`
	WatchedAt string                `json:"watched_at,omitempty"`
}

type simklHistoryEpisode struct {
	Number    int      `json:"number,omitempty"`
	WatchedAt string   `json:"watched_at,omitempty"`
	IDs       simklIDs `json:"ids,omitempty"`
}

type simklHistoryResponse struct {
	NotFound struct {
		Movies   []simklHistoryMovie   `json:"movies"`
		Shows    []simklHistoryShow    `json:"shows"`
		Episodes []simklHistoryEpisode `json:"episodes"`
	} `json:"not_found"`
}

func (r simklHistoryResponse) notFoundHistoryIDs(historyIDsByKey map[string][]string) []string {
	seen := make(map[string]bool)
	var historyIDs []string
	addByKeys := func(keys []string) {
		for _, key := range keys {
			for _, historyID := range historyIDsByKey[key] {
				if historyID == "" || seen[historyID] {
					continue
				}
				seen[historyID] = true
				historyIDs = append(historyIDs, historyID)
			}
		}
	}
	for _, movie := range r.NotFound.Movies {
		addByKeys(historyMovieMatchKeys(movie))
	}
	for _, show := range r.NotFound.Shows {
		addByKeys(historyShowMatchKeys(show))
	}
	for _, episode := range r.NotFound.Episodes {
		addByKeys(historyStandaloneEpisodeMatchKeys(episode))
	}
	return historyIDs
}

func (p *Provider) addRemovedListWarning(
	conn watchsync.Connection,
	activity string,
	cursorKey string,
	batch *watchsync.WatchedImportBatch,
) {
	if activity == "" || conn.SyncCursors[cursorKey] == activity {
		return
	}
	batch.Warnings = append(batch.Warnings, "simkl removed_from_list changed; removals are not imported")
	batch.UpdatedCursors[cursorKey] = activity
}

func watchedBuckets(activities simklActivities) []simklWatchedBucket {
	return []simklWatchedBucket{
		{
			cursorKey: simklCursorInboundMoviesCompleted,
			activity:  activities.Movies.Completed,
			path:      "/sync/all-items/movies/completed?extended=full&episode_watched_at=yes",
		},
		{
			cursorKey: simklCursorInboundShowsWatching,
			activity:  activities.TVShows.Watching,
			path:      "/sync/all-items/shows/watching?extended=full&episode_watched_at=yes",
		},
		{
			cursorKey:                  simklCursorInboundShowsCompleted,
			activity:                   activities.TVShows.Completed,
			path:                       "/sync/all-items/shows/completed?extended=full&episode_watched_at=yes",
			allowShowTimestampFallback: true,
		},
		{
			cursorKey: simklCursorInboundAnimeWatching,
			activity:  activities.Anime.Watching,
			path:      "/sync/all-items/anime/watching?extended=full_anime_seasons&episode_watched_at=yes",
		},
		{
			cursorKey:                  simklCursorInboundAnimeCompleted,
			activity:                   activities.Anime.Completed,
			path:                       "/sync/all-items/anime/completed?extended=full_anime_seasons&episode_watched_at=yes",
			allowShowTimestampFallback: true,
		},
	}
}

func shouldSkipSimklBucket(previous string, activity string) bool {
	if previous == "" {
		return false
	}
	if strings.TrimSpace(activity) == "" {
		return true
	}
	return previous == activity
}

func appendDateFrom(path string, dateFrom string) string {
	if strings.TrimSpace(dateFrom) == "" {
		return path
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "date_from=" + url.QueryEscape(dateFrom)
}

func watchedRowsFromAllItems(payload simklAllItemsResponse, allowShowTimestampFallback bool) ([]watchsync.RemoteWatch, []string) {
	rows := make([]watchsync.RemoteWatch, 0, len(payload.Movies))
	var warnings []string
	for _, movie := range payload.Movies {
		if movie.Status != "" && movie.Status != "completed" {
			continue
		}
		if movie.LastWatchedAt == nil {
			continue
		}
		key := movieKey(movie.Movie.IDs)
		if key == "" {
			warnings = append(warnings, "simkl watched movie skipped because it has no usable external id")
			continue
		}
		rows = append(rows, watchsync.RemoteWatch{
			Provider:        "simkl",
			ProviderItemKey: key,
			Kind:            historyimport.KindMovie,
			Title:           movie.Movie.Title,
			Year:            movie.Movie.Year,
			IMDbID:          movie.Movie.IDs.IMDb,
			TMDBID:          intString(movie.Movie.IDs.TMDB),
			TVDBID:          intString(movie.Movie.IDs.TVDB),
			PlayCount:       1,
			LastWatchedAt:   movie.LastWatchedAt,
		})
	}
	for _, show := range append(payload.Shows, payload.Anime...) {
		for _, season := range show.Seasons {
			for _, episode := range season.Episodes {
				watchedAt := episode.WatchedAt
				if watchedAt == nil && allowShowTimestampFallback && show.Status == "completed" {
					watchedAt = show.LastWatchedAt
				}
				if watchedAt == nil {
					continue
				}
				seasonNumber, number := episodeNumbers(episode, season.Number)
				key := episodeKey(show.Show.IDs, seasonNumber, number, episode.IDs)
				if key == "" {
					warnings = append(warnings, "simkl watched episode skipped because it has no usable external id path")
					continue
				}
				rows = append(rows, watchsync.RemoteWatch{
					Provider:        "simkl",
					ProviderItemKey: key,
					Kind:            historyimport.KindEpisode,
					Title:           episode.Title,
					IMDbID:          episode.IDs.IMDb,
					TMDBID:          intString(episode.IDs.TMDB),
					TVDBID:          intString(episode.IDs.TVDB),
					SeriesTitle:     show.Show.Title,
					SeriesYear:      show.Show.Year,
					SeriesIMDbID:    show.Show.IDs.IMDb,
					SeriesTMDBID:    intString(show.Show.IDs.TMDB),
					SeriesTVDBID:    intString(show.Show.IDs.TVDB),
					SeasonNumber:    seasonNumber,
					EpisodeNumber:   number,
					PlayCount:       1,
					LastWatchedAt:   watchedAt,
				})
			}
		}
	}
	return rows, warnings
}

func progressRowsFromPlayback(payload []simklPlayback, provider string) ([]watchsync.RemoteProgress, []string, bool) {
	rows := make([]watchsync.RemoteProgress, 0, len(payload))
	var warnings []string
	hasAnimeRows := false
	for _, item := range payload {
		switch item.Type {
		case "movie":
			key := movieKey(item.Movie.IDs)
			if key == "" {
				warnings = append(warnings, "simkl playback movie skipped because it has no usable external id")
				continue
			}
			rows = append(rows, watchsync.RemoteProgress{
				Provider:        provider,
				ProviderItemKey: key,
				Kind:            historyimport.KindMovie,
				Title:           item.Movie.Title,
				Year:            item.Movie.Year,
				IMDbID:          item.Movie.IDs.IMDb,
				TMDBID:          intString(item.Movie.IDs.TMDB),
				TVDBID:          intString(item.Movie.IDs.TVDB),
				ProgressPercent: item.Progress,
				PausedAt:        item.PausedAt,
			})
		case "episode", "show", "anime":
			show := item.Show
			if item.Type == "anime" || show.Title == "" && item.Anime.Title != "" {
				show = item.Anime
				hasAnimeRows = true
			}
			season, episode := episodeNumbers(item.Episode, 0)
			key := episodeKey(show.IDs, season, episode, item.Episode.IDs)
			if key == "" {
				warnings = append(warnings, "simkl playback episode skipped because it has no usable external id path")
				continue
			}
			rows = append(rows, watchsync.RemoteProgress{
				Provider:        provider,
				ProviderItemKey: key,
				Kind:            historyimport.KindEpisode,
				Title:           item.Episode.Title,
				SeriesTitle:     show.Title,
				SeriesYear:      show.Year,
				SeriesIMDbID:    show.IDs.IMDb,
				SeriesTMDBID:    intString(show.IDs.TMDB),
				SeriesTVDBID:    intString(show.IDs.TVDB),
				SeasonNumber:    season,
				EpisodeNumber:   episode,
				ProgressPercent: item.Progress,
				PausedAt:        item.PausedAt,
			})
		}
	}
	return rows, warnings, hasAnimeRows
}

func episodeNumbers(episode simklEpisode, seasonFallback int) (int, int) {
	season := episode.TVDB.Season
	number := episode.TVDB.Episode
	if season == 0 {
		season = episode.TVDBSeason
	}
	if number == 0 {
		number = episode.TVDBNumber
	}
	if season == 0 {
		season = episode.Season
	}
	if season == 0 {
		season = seasonFallback
	}
	if number == 0 {
		number = episode.Number
	}
	if number == 0 {
		number = episode.Episode
	}
	return season, number
}

func oldestCursor(values ...string) string {
	var oldest string
	var oldestTime time.Time
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			if oldest == "" {
				oldest = value
			}
			continue
		}
		if oldest == "" || parsed.Before(oldestTime) {
			oldest = value
			oldestTime = parsed
		}
	}
	return oldest
}

type simklHistoryRequest struct {
	Payload         simklHistoryPayload
	HistoryIDsByKey map[string][]string
}

func buildHistoryRequest(plays []watchsync.LocalPlay, includeWatchedAt bool) simklHistoryRequest {
	request := simklHistoryRequest{
		HistoryIDsByKey: make(map[string][]string),
	}
	for _, play := range plays {
		watchedAt := ""
		if includeWatchedAt && !play.WatchedAt.IsZero() {
			watchedAt = play.WatchedAt.UTC().Format(time.RFC3339)
		}
		switch play.Kind {
		case historyimport.KindMovie:
			movie := simklHistoryMovie{
				Title:     play.Title,
				Year:      play.Year,
				WatchedAt: watchedAt,
				IDs:       idsFromLocal(play.IMDbID, play.TMDBID, play.TVDBID),
			}
			request.Payload.Movies = append(request.Payload.Movies, movie)
			request.addHistoryID(play.HistoryID, historyMovieMatchKeys(movie))
		case historyimport.KindEpisode:
			show := simklHistoryShow{
				Title: play.SeriesTitle,
				Year:  play.SeriesYear,
				IDs:   idsFromLocal(play.SeriesIMDbID, play.SeriesTMDBID, play.SeriesTVDBID),
				Seasons: []simklHistorySeason{{
					Number: play.SeasonNumber,
					Episodes: []simklHistoryEpisode{{
						Number:    play.EpisodeNumber,
						WatchedAt: watchedAt,
						IDs:       idsFromLocal(play.IMDbID, play.TMDBID, play.TVDBID),
					}},
				}},
			}
			request.Payload.Shows = append(request.Payload.Shows, show)
			request.addHistoryID(play.HistoryID, historyShowRequestMatchKeys(show))
		}
	}
	return request
}

func buildHistoryPayload(plays []watchsync.LocalPlay, includeWatchedAt bool) simklHistoryPayload {
	return buildHistoryRequest(plays, includeWatchedAt).Payload
}

func (r simklHistoryRequest) addHistoryID(historyID string, keys []string) {
	if historyID == "" {
		return
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		r.HistoryIDsByKey[key] = append(r.HistoryIDsByKey[key], historyID)
	}
}

func historyMovieMatchKeys(movie simklHistoryMovie) []string {
	var keys []string
	watchedAt := strings.TrimSpace(movie.WatchedAt)
	for _, idKey := range historyIDMatchKeys(movie.IDs) {
		keys = appendHistoryWatchedVariants(keys, "movie:id:"+idKey, watchedAt)
	}
	if title := normalizedHistoryTitle(movie.Title); title != "" && movie.Year > 0 {
		keys = appendHistoryWatchedVariants(keys, fmt.Sprintf("movie:title:%s:%d", title, movie.Year), watchedAt)
	}
	return keys
}

func historyShowMatchKeys(show simklHistoryShow) []string {
	if len(show.Seasons) == 0 {
		return historyShowOnlyMatchKeys(show)
	}
	return historyShowEpisodeMatchKeys(show)
}

func historyShowRequestMatchKeys(show simklHistoryShow) []string {
	keys := historyShowOnlyMatchKeys(show)
	return append(keys, historyShowEpisodeMatchKeys(show)...)
}

func historyShowOnlyMatchKeys(show simklHistoryShow) []string {
	var keys []string
	for _, showKey := range historyShowIdentityKeys(show) {
		keys = append(keys, "show:"+showKey)
	}
	return keys
}

func historyShowEpisodeMatchKeys(show simklHistoryShow) []string {
	var keys []string
	showKeys := historyShowIdentityKeys(show)
	for _, season := range show.Seasons {
		for _, episode := range season.Episodes {
			for _, showKey := range showKeys {
				keys = append(keys, historyShowEpisodeIdentityKeys(showKey, season.Number, episode)...)
			}
			keys = append(keys, historyStandaloneEpisodeMatchKeys(episode)...)
		}
	}
	return keys
}

func historyShowIdentityKeys(show simklHistoryShow) []string {
	var keys []string
	for _, idKey := range historyIDMatchKeys(show.IDs) {
		keys = append(keys, "id:"+idKey)
	}
	if title := normalizedHistoryTitle(show.Title); title != "" && show.Year > 0 {
		keys = append(keys, fmt.Sprintf("title:%s:%d", title, show.Year))
	}
	return keys
}

func historyShowEpisodeIdentityKeys(showKey string, seasonNumber int, episode simklHistoryEpisode) []string {
	base := fmt.Sprintf("episode:show:%s:s%d:e%d", showKey, seasonNumber, episode.Number)
	return appendHistoryWatchedVariants(nil, base, strings.TrimSpace(episode.WatchedAt))
}

func historyStandaloneEpisodeMatchKeys(episode simklHistoryEpisode) []string {
	var keys []string
	watchedAt := strings.TrimSpace(episode.WatchedAt)
	for _, idKey := range historyIDMatchKeys(episode.IDs) {
		keys = appendHistoryWatchedVariants(keys, "episode:id:"+idKey, watchedAt)
	}
	return keys
}

func appendHistoryWatchedVariants(keys []string, base string, watchedAt string) []string {
	if base == "" {
		return keys
	}
	if watchedAt != "" {
		keys = append(keys, base+":watched:"+watchedAt)
	}
	return append(keys, base)
}

func historyIDMatchKeys(ids simklIDs) []string {
	keys := make([]string, 0, 4)
	if ids.IMDb != "" {
		keys = append(keys, "imdb:"+ids.IMDb)
	}
	if ids.TMDB > 0 {
		keys = append(keys, "tmdb:"+strconv.Itoa(ids.TMDB))
	}
	if ids.TVDB > 0 {
		keys = append(keys, "tvdb:"+strconv.Itoa(ids.TVDB))
	}
	if ids.Simkl > 0 {
		keys = append(keys, "simkl:"+strconv.Itoa(ids.Simkl))
	}
	return keys
}

func normalizedHistoryTitle(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(title)), " ")
}

func buildScrobblePayload(event watchsync.ScrobbleEvent) map[string]any {
	progress := 0.0
	if event.DurationSeconds > 0 {
		progress = event.PositionSeconds / event.DurationSeconds * 100
	}
	payload := map[string]any{"progress": progress}
	switch event.Kind {
	case historyimport.KindEpisode:
		payload["show"] = map[string]any{"ids": idsFromLocal(event.SeriesIMDbID, event.SeriesTMDBID, event.SeriesTVDBID)}
		payload["episode"] = map[string]any{
			"season": event.SeasonNumber,
			"number": event.EpisodeNumber,
			"ids":    idsFromLocal(event.IMDbID, event.TMDBID, event.TVDBID),
		}
	default:
		payload["movie"] = map[string]any{"ids": idsFromLocal(event.IMDbID, event.TMDBID, event.TVDBID)}
	}
	return payload
}

func idsFromLocal(imdbID, tmdbID, tvdbID string) simklIDs {
	return simklIDs{IMDb: imdbID, TMDB: parseInt(tmdbID), TVDB: parseInt(tvdbID)}
}

func intString(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func movieKey(ids simklIDs) string {
	switch {
	case ids.IMDb != "":
		return "imdb:" + ids.IMDb
	case ids.TMDB > 0:
		return "tmdb:" + strconv.Itoa(ids.TMDB)
	case ids.TVDB > 0:
		return "tvdb:" + strconv.Itoa(ids.TVDB)
	case ids.Simkl > 0:
		return "simkl:" + strconv.Itoa(ids.Simkl)
	default:
		return ""
	}
}

func episodeKey(showIDs simklIDs, season, episode int, episodeIDs simklIDs) string {
	switch {
	case episodeIDs.TVDB > 0:
		return "tvdb:" + strconv.Itoa(episodeIDs.TVDB)
	case episodeIDs.TMDB > 0:
		return "tmdb:" + strconv.Itoa(episodeIDs.TMDB)
	case episodeIDs.Simkl > 0:
		return "simkl:" + strconv.Itoa(episodeIDs.Simkl)
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

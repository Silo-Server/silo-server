package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL             = "https://api.themoviedb.org/3"
	projectAPIKey              = "4ef0d7355d9ffb5151e987764708ce96"
	maxRetries                 = 3
	maxResponseBody            = 1 << 20 // 1 MB
	maxCollectionPresetResults = 500
	defaultResponseCacheTTL    = 2 * time.Hour
	// certificationCacheTTL is deliberately much longer than the shared
	// response TTL: a certification is assigned at release and effectively
	// never changes. The stale-window failure mode is safe — a title that
	// gains a cert stays hidden (fail-closed) for at most the TTL.
	certificationCacheTTL = 7 * 24 * time.Hour
	// sharedFetchTimeout bounds a shared (caller-detached) singleflight
	// fetch; see cachedCertification and GetMediaDetail.
	sharedFetchTimeout = 30 * time.Second
	// mediaDetailCacheTTL is deliberately short: a detail is large, so the
	// cache holds only the titles being looked at right now. It is long enough
	// that a title page polling a download's progress every 30 seconds, and
	// everyone else viewing the title, share one fetch.
	mediaDetailCacheTTL = 2 * time.Minute
)

// Client is an HTTP client for the TMDB collection preset API surface.
type Client struct {
	httpClient           *http.Client
	apiKey               string
	baseURL              string
	limiter              *rate.Limiter
	discoverSectionCache *cache.TTLCache[*MediaPage]
	discoverPageCache    *cache.TTLCache[*MediaPage]
	externalIDCache      *cache.TTLCache[*ExternalIDs]
	certificationCache   *cache.TTLCache[string]
	mediaDetailCache     *cache.TTLCache[*MediaDetail]
	cacheGroup           singleflight.Group
	responseCacheTTL     time.Duration
}

// NewClient creates a TMDB API client with the given API key and rate limit
// (requests per second). If apiKey is empty, Silo's public project API key is
// used.

// ErrNotFound is wrapped by errors for titles TMDB does not have (HTTP 404).
var ErrNotFound = errors.New("tmdb: not found")

func NewClient(apiKey string, rateLimit int) *Client {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKey = projectAPIKey
	}
	return &Client{
		httpClient:           &http.Client{Timeout: 30 * time.Second},
		apiKey:               apiKey,
		baseURL:              defaultBaseURL,
		limiter:              rate.NewLimiter(rate.Limit(rateLimit), rateLimit),
		discoverSectionCache: cache.NewTTLCache[*MediaPage](),
		discoverPageCache:    cache.NewTTLCache[*MediaPage](),
		externalIDCache:      cache.NewTTLCache[*ExternalIDs](),
		certificationCache:   cache.NewTTLCache[string](),
		mediaDetailCache:     cache.NewTTLCache[*MediaDetail](),
		responseCacheTTL:     defaultResponseCacheTTL,
	}
}

// SetBaseURL overrides the API base URL. Used for testing.
func (c *Client) SetBaseURL(url string) {
	c.baseURL = url
}

// Close releases background cache sweepers owned by the client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	if c.discoverSectionCache != nil {
		c.discoverSectionCache.Close()
	}
	if c.discoverPageCache != nil {
		c.discoverPageCache.Close()
	}
	if c.externalIDCache != nil {
		c.externalIDCache.Close()
	}
	if c.certificationCache != nil {
		c.certificationCache.Close()
	}
	if c.mediaDetailCache != nil {
		c.mediaDetailCache.Close()
	}
}

// doGet executes a GET request against the TMDB API with rate limiting,
// exponential backoff on 5xx/429, and JSON decoding into dest.
func (c *Client) doGet(ctx context.Context, path string, dest any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}

	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	reqURL := c.baseURL + path + sep + "api_key=" + url.QueryEscape(c.apiKey)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// reqURL carries the API key and a *url.Error prints the whole URL, so
		// these errors drop the query string before callers log or store them.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return fmt.Errorf("tmdb: create request: %w", logredact.SanitizeURLError(err))
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("tmdb: request failed: %w", logredact.SanitizeURLError(err))
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt < maxRetries {
				backoff := retryAfterOrDefault(resp, attempt)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			return fmt.Errorf("tmdb: rate limited after %d retries", maxRetries)
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt) * time.Second
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			return fmt.Errorf("tmdb: server error %d after %d retries", resp.StatusCode, maxRetries)
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
			resp.Body.Close()
			// A 404 wraps ErrNotFound so callers can tell a missing title
			// from TMDB being unreachable.
			var notFound error
			if resp.StatusCode == http.StatusNotFound {
				notFound = ErrNotFound
			}
			var apiErr apiError
			if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.StatusMessage != "" {
				return errors.Join(notFound, fmt.Errorf("tmdb: HTTP %d: %s", resp.StatusCode, apiErr.StatusMessage))
			}
			return errors.Join(notFound, fmt.Errorf("tmdb: HTTP %d", resp.StatusCode))
		}

		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("tmdb: decode response: %w", decodeErr)
		}
		return nil
	}
	return fmt.Errorf("tmdb: max retries exceeded")
}

// retryAfterOrDefault parses the Retry-After header (seconds) or falls back
// to exponential backoff.
func retryAfterOrDefault(resp *http.Response, attempt int) time.Duration {
	if val := resp.Header.Get("Retry-After"); val != "" {
		if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(1<<attempt) * time.Second
}

// SearchMedia searches TMDB directly for movies, TV series, or both. mediaType
// accepts Silo-facing "movie", "series", or "all" values, plus TMDB-facing
// "tv" for callers already working at the provider boundary.
func (c *Client) SearchMedia(ctx context.Context, mediaType, query string, page int) (*MediaPage, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("tmdb: search query is required")
	}
	if page <= 0 {
		page = 1
	}

	switch mediaType {
	case "movie":
		values := url.Values{}
		values.Set("query", query)
		values.Set("include_adult", "false")
		values.Set("page", strconv.Itoa(page))
		var resp paginatedResponse[mediaMovieResponse]
		if err := c.doGet(ctx, "/search/movie?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeMoviePage(resp), nil
	case "series", "tv":
		values := url.Values{}
		values.Set("query", query)
		values.Set("include_adult", "false")
		values.Set("page", strconv.Itoa(page))
		var resp paginatedResponse[mediaTVResponse]
		if err := c.doGet(ctx, "/search/tv?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeTVPage(resp), nil
	case "all":
		values := url.Values{}
		values.Set("query", query)
		values.Set("include_adult", "false")
		values.Set("page", strconv.Itoa(page))
		var resp paginatedResponse[mediaMultiSearchResponse]
		if err := c.doGet(ctx, "/search/multi?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeMultiSearchPage(resp), nil
	default:
		return nil, fmt.Errorf("tmdb: invalid media type for search: %q", mediaType)
	}
}

// DiscoverSection fetches one of Silo's request-discovery sections directly
// from TMDB. It intentionally does not go through collection sync or
// collection templates.
func (c *Client) DiscoverSection(ctx context.Context, section string, page int) (*MediaPage, error) {
	if page <= 0 {
		page = 1
	}
	cacheKey := "discover_section:" + section + ":" + strconv.Itoa(page)
	if c.discoverSectionCache != nil {
		if cached, ok := c.discoverSectionCache.Get(cacheKey); ok {
			return cloneMediaPage(cached), nil
		}
	}

	value, err, _ := c.cacheGroup.Do(cacheKey, func() (any, error) {
		if c.discoverSectionCache != nil {
			if cached, ok := c.discoverSectionCache.Get(cacheKey); ok {
				return cached, nil
			}
		}
		page, err := c.fetchDiscoverSection(ctx, section, page)
		if err != nil {
			return nil, err
		}
		cached := cloneMediaPage(page)
		if c.discoverSectionCache != nil && c.responseCacheTTL > 0 {
			c.discoverSectionCache.Set(cacheKey, cached, c.responseCacheTTL)
		}
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	pageValue, ok := value.(*MediaPage)
	if !ok {
		return nil, fmt.Errorf("tmdb: invalid cached discovery section response")
	}
	return cloneMediaPage(pageValue), nil
}

func (c *Client) fetchDiscoverSection(ctx context.Context, section string, page int) (*MediaPage, error) {
	values := url.Values{}
	values.Set("page", strconv.Itoa(page))

	switch section {
	case "trending_movies":
		var resp paginatedResponse[mediaMovieResponse]
		if err := c.doGet(ctx, "/trending/movie/week?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeMoviePage(resp), nil
	case "trending_series":
		var resp paginatedResponse[mediaTVResponse]
		if err := c.doGet(ctx, "/trending/tv/week?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeTVPage(resp), nil
	case "popular_movies":
		var resp paginatedResponse[mediaMovieResponse]
		if err := c.doGet(ctx, "/movie/popular?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeMoviePage(resp), nil
	case "popular_series":
		var resp paginatedResponse[mediaTVResponse]
		if err := c.doGet(ctx, "/tv/popular?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeTVPage(resp), nil
	case "upcoming_movies":
		var resp paginatedResponse[mediaMovieResponse]
		if err := c.doGet(ctx, "/movie/upcoming?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeMoviePage(resp), nil
	case "on_air_series":
		var resp paginatedResponse[mediaTVResponse]
		if err := c.doGet(ctx, "/tv/on_the_air?"+values.Encode(), &resp); err != nil {
			return nil, err
		}
		return normalizeTVPage(resp), nil
	default:
		return nil, fmt.Errorf("tmdb: invalid discovery section: %q", section)
	}
}

func cloneMediaPage(page *MediaPage) *MediaPage {
	if page == nil {
		return nil
	}
	cloned := *page
	if page.Results != nil {
		cloned.Results = append([]MediaResult(nil), page.Results...)
	}
	return &cloned
}

func normalizeMoviePage(resp paginatedResponse[mediaMovieResponse]) *MediaPage {
	page := &MediaPage{
		Page:         resp.Page,
		TotalPages:   resp.TotalPages,
		TotalResults: resp.TotalResults,
		Results:      make([]MediaResult, 0, len(resp.Results)),
	}
	for _, item := range resp.Results {
		page.Results = append(page.Results, MediaResult{
			ID:           item.ID,
			MediaType:    "movie",
			Title:        item.Title,
			Overview:     item.Overview,
			PosterPath:   item.PosterPath,
			BackdropPath: item.BackdropPath,
			ReleaseDate:  item.ReleaseDate,
			Year:         releaseYear(item.ReleaseDate),
			Popularity:   item.Popularity,
			VoteAverage:  item.VoteAverage,
		})
	}
	return page
}

func normalizeTVPage(resp paginatedResponse[mediaTVResponse]) *MediaPage {
	page := &MediaPage{
		Page:         resp.Page,
		TotalPages:   resp.TotalPages,
		TotalResults: resp.TotalResults,
		Results:      make([]MediaResult, 0, len(resp.Results)),
	}
	for _, item := range resp.Results {
		page.Results = append(page.Results, MediaResult{
			ID:           item.ID,
			MediaType:    "series",
			Title:        item.Name,
			Overview:     item.Overview,
			PosterPath:   item.PosterPath,
			BackdropPath: item.BackdropPath,
			ReleaseDate:  item.FirstAirDate,
			Year:         releaseYear(item.FirstAirDate),
			Popularity:   item.Popularity,
			VoteAverage:  item.VoteAverage,
		})
	}
	return page
}

func normalizeMultiSearchPage(resp paginatedResponse[mediaMultiSearchResponse]) *MediaPage {
	page := &MediaPage{
		Page:         resp.Page,
		TotalPages:   resp.TotalPages,
		TotalResults: resp.TotalResults,
		Results:      make([]MediaResult, 0, len(resp.Results)),
	}
	for _, item := range resp.Results {
		switch item.MediaType {
		case "movie":
			page.Results = append(page.Results, MediaResult{
				ID:           item.ID,
				MediaType:    "movie",
				Title:        item.Title,
				Overview:     item.Overview,
				PosterPath:   item.PosterPath,
				BackdropPath: item.BackdropPath,
				ReleaseDate:  item.ReleaseDate,
				Year:         releaseYear(item.ReleaseDate),
				Popularity:   item.Popularity,
				VoteAverage:  item.VoteAverage,
			})
		case "tv":
			page.Results = append(page.Results, MediaResult{
				ID:           item.ID,
				MediaType:    "series",
				Title:        item.Name,
				Overview:     item.Overview,
				PosterPath:   item.PosterPath,
				BackdropPath: item.BackdropPath,
				ReleaseDate:  item.FirstAirDate,
				Year:         releaseYear(item.FirstAirDate),
				Popularity:   item.Popularity,
				VoteAverage:  item.VoteAverage,
			})
		}
	}
	return page
}

func releaseYear(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil {
		return 0
	}
	return year
}

func normalizeCollectionPreset(preset, mediaType, timeWindow string) (string, string, string, string, error) {
	switch preset {
	case "trending":
		switch mediaType {
		case "all", "movie", "tv":
		default:
			return "", "", "", "", fmt.Errorf("tmdb: invalid media type for preset %q: %q", preset, mediaType)
		}
		switch timeWindow {
		case "day", "week":
		default:
			return "", "", "", "", fmt.Errorf("tmdb: invalid time window for preset %q: %q", preset, timeWindow)
		}
		return preset, mediaType, timeWindow, fmt.Sprintf("/trending/%s/%s", mediaType, timeWindow), nil
	case "popular", "top_rated":
		switch mediaType {
		case "movie":
			return preset, mediaType, "", fmt.Sprintf("/movie/%s", preset), nil
		case "tv":
			return preset, mediaType, "", fmt.Sprintf("/tv/%s", preset), nil
		default:
			return "", "", "", "", fmt.Errorf("tmdb: invalid media type for preset %q: %q", preset, mediaType)
		}
	case "now_playing", "upcoming":
		if mediaType != "movie" {
			return "", "", "", "", fmt.Errorf("tmdb: preset %q requires media type %q", preset, "movie")
		}
		return preset, mediaType, "", fmt.Sprintf("/movie/%s", preset), nil
	case "airing_today", "on_the_air":
		if mediaType != "tv" {
			return "", "", "", "", fmt.Errorf("tmdb: preset %q requires media type %q", preset, "tv")
		}
		return preset, mediaType, "", fmt.Sprintf("/tv/%s", preset), nil
	default:
		return "", "", "", "", fmt.Errorf("tmdb: invalid preset: %q", preset)
	}
}

// GetCollectionPreset fetches a normalized preset collection from TMDB.
func (c *Client) GetCollectionPreset(ctx context.Context, preset, mediaType, timeWindow string, limit int) ([]CollectionResult, error) {
	_, normalizedMediaType, _, basePath, err := normalizeCollectionPreset(preset, mediaType, timeWindow)
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 20
	}
	if limit > maxCollectionPresetResults {
		limit = maxCollectionPresetResults
	}

	results := make([]CollectionResult, 0, limit)
	for page := 1; len(results) < limit; page++ {
		path := fmt.Sprintf("%s?page=%d", basePath, page)
		done := false

		switch preset {
		case "trending":
			var resp paginatedResponse[TrendingResult]
			if err := c.doGet(ctx, path, &resp); err != nil {
				return nil, err
			}
			for _, item := range resp.Results {
				title := item.Title
				if title == "" {
					title = item.Name
				}
				results = append(results, CollectionResult{
					ID:        item.ID,
					MediaType: item.MediaType,
					Title:     title,
				})
			}
			if page >= resp.TotalPages {
				done = true
			}
		case "popular", "top_rated":
			if normalizedMediaType == "tv" {
				var resp paginatedResponse[TVResult]
				if err := c.doGet(ctx, path, &resp); err != nil {
					return nil, err
				}
				for _, item := range resp.Results {
					results = append(results, CollectionResult{
						ID:        item.ID,
						MediaType: "tv",
						Title:     item.Name,
					})
				}
				if page >= resp.TotalPages {
					done = true
				}
				break
			}

			var resp paginatedResponse[MovieResult]
			if err := c.doGet(ctx, path, &resp); err != nil {
				return nil, err
			}
			for _, item := range resp.Results {
				results = append(results, CollectionResult{
					ID:        item.ID,
					MediaType: "movie",
					Title:     item.Title,
				})
			}
			if page >= resp.TotalPages {
				done = true
			}
		case "now_playing", "upcoming":
			var resp paginatedResponse[MovieResult]
			if err := c.doGet(ctx, path, &resp); err != nil {
				return nil, err
			}
			for _, item := range resp.Results {
				results = append(results, CollectionResult{
					ID:        item.ID,
					MediaType: "movie",
					Title:     item.Title,
				})
			}
			if page >= resp.TotalPages {
				done = true
			}
		case "airing_today", "on_the_air":
			var resp paginatedResponse[TVResult]
			if err := c.doGet(ctx, path, &resp); err != nil {
				return nil, err
			}
			for _, item := range resp.Results {
				results = append(results, CollectionResult{
					ID:        item.ID,
					MediaType: "tv",
					Title:     item.Name,
				})
			}
			if page >= resp.TotalPages {
				done = true
			}
		default:
			return nil, fmt.Errorf("tmdb: invalid preset: %q", preset)
		}

		if done {
			break
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Discover fetches results from TMDB's `/discover/{movie,tv}` endpoint with
// the supplied filters. The client paginates up to params.Limit, capped at
// maxCollectionPresetResults.
func (c *Client) Discover(ctx context.Context, mediaType string, params DiscoverParams) ([]CollectionResult, error) {
	switch mediaType {
	case "movie", "tv":
	default:
		return nil, fmt.Errorf("tmdb: invalid media type for discover: %q", mediaType)
	}

	if strings.TrimSpace(params.SortBy) == "" {
		return nil, fmt.Errorf("tmdb: discover requires sort_by")
	}

	basePath := "/discover/" + mediaType
	baseQuery := buildDiscoverQuery(mediaType, params)

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > maxCollectionPresetResults {
		limit = maxCollectionPresetResults
	}

	results := make([]CollectionResult, 0, limit)
	for page := 1; len(results) < limit; page++ {
		query := baseQuery + "&page=" + strconv.Itoa(page)
		path := basePath + "?" + query

		if mediaType == "tv" {
			var resp paginatedResponse[TVResult]
			if err := c.doGet(ctx, path, &resp); err != nil {
				return nil, err
			}
			for _, item := range resp.Results {
				results = append(results, CollectionResult{
					ID:        item.ID,
					MediaType: "tv",
					Title:     item.Name,
				})
			}
			if page >= resp.TotalPages || len(resp.Results) == 0 {
				break
			}
			continue
		}

		var resp paginatedResponse[MovieResult]
		if err := c.doGet(ctx, path, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Results {
			results = append(results, CollectionResult{
				ID:        item.ID,
				MediaType: "movie",
				Title:     item.Title,
			})
		}
		if page >= resp.TotalPages || len(resp.Results) == 0 {
			break
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// DiscoverPage fetches a single page from TMDB's /discover/{movie,tv} endpoint
// and returns the full MediaPage shape (with posters, overviews, etc.) so the
// request system can enrich it with availability and request state. Unlike
// Discover (which is intended for collection templates and returns just IDs
// and titles), DiscoverPage exposes single-page semantics: callers control
// pagination explicitly.
func (c *Client) DiscoverPage(ctx context.Context, mediaType string, params DiscoverParams, page int) (*MediaPage, error) {
	switch mediaType {
	case "movie", "tv":
	default:
		return nil, fmt.Errorf("tmdb: invalid media type for discover: %q", mediaType)
	}
	if strings.TrimSpace(params.SortBy) == "" {
		return nil, fmt.Errorf("tmdb: discover requires sort_by")
	}
	if page <= 0 {
		page = 1
	}

	query := buildDiscoverQuery(mediaType, params) + "&page=" + strconv.Itoa(page)
	path := "/discover/" + mediaType + "?" + query
	cacheKey := "discover_page:" + path
	if c.discoverPageCache != nil {
		if cached, ok := c.discoverPageCache.Get(cacheKey); ok {
			return cloneMediaPage(cached), nil
		}
	}

	value, err, _ := c.cacheGroup.Do(cacheKey, func() (any, error) {
		if c.discoverPageCache != nil {
			if cached, ok := c.discoverPageCache.Get(cacheKey); ok {
				return cached, nil
			}
		}
		page, err := c.fetchDiscoverPage(ctx, mediaType, path)
		if err != nil {
			return nil, err
		}
		cached := cloneMediaPage(page)
		if c.discoverPageCache != nil && c.responseCacheTTL > 0 {
			c.discoverPageCache.Set(cacheKey, cached, c.responseCacheTTL)
		}
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	pageValue, ok := value.(*MediaPage)
	if !ok {
		return nil, fmt.Errorf("tmdb: invalid cached discover page response")
	}
	return cloneMediaPage(pageValue), nil
}

func (c *Client) fetchDiscoverPage(ctx context.Context, mediaType, path string) (*MediaPage, error) {
	if mediaType == "tv" {
		var resp paginatedResponse[mediaTVResponse]
		if err := c.doGet(ctx, path, &resp); err != nil {
			return nil, err
		}
		return normalizeTVPage(resp), nil
	}
	var resp paginatedResponse[mediaMovieResponse]
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	return normalizeMoviePage(resp), nil
}

// buildDiscoverQuery composes the TMDB discover query string (without the
// leading "?" and without page or api_key — doGet handles those).
func buildDiscoverQuery(mediaType string, params DiscoverParams) string {
	values := url.Values{}
	values.Set("sort_by", params.SortBy)

	if genres := joinIntSlice(params.WithGenres, ","); genres != "" {
		values.Set("with_genres", genres)
	}
	if without := joinIntSlice(params.WithoutGenres, ","); without != "" {
		values.Set("without_genres", without)
	}
	if companies := joinIntSlice(params.WithCompanies, ","); companies != "" {
		values.Set("with_companies", companies)
	}
	if networks := joinIntSlice(params.WithNetworks, ","); networks != "" {
		values.Set("with_networks", networks)
	}
	if params.VoteCountGte > 0 {
		values.Set("vote_count.gte", strconv.Itoa(params.VoteCountGte))
	}
	if params.VoteAverageGte > 0 {
		values.Set("vote_average.gte", strconv.FormatFloat(params.VoteAverageGte, 'f', -1, 64))
	}
	if params.ReleaseDateGte != "" {
		if mediaType == "tv" {
			values.Set("first_air_date.gte", params.ReleaseDateGte)
		} else {
			values.Set("primary_release_date.gte", params.ReleaseDateGte)
		}
	}
	if params.ReleaseDateLte != "" {
		if mediaType == "tv" {
			values.Set("first_air_date.lte", params.ReleaseDateLte)
		} else {
			values.Set("primary_release_date.lte", params.ReleaseDateLte)
		}
	}
	if len(params.Certifications) > 0 {
		values.Set("certification_country", "US")
		values.Set("certification", strings.Join(params.Certifications, "|"))
	}
	if cert := strings.TrimSpace(params.CertificationLte); cert != "" {
		values.Set("certification_country", "US")
		values.Set("certification.lte", cert)
	}
	if params.WithRuntimeGte > 0 {
		values.Set("with_runtime.gte", strconv.Itoa(params.WithRuntimeGte))
	}
	if params.WithRuntimeLte > 0 {
		values.Set("with_runtime.lte", strconv.Itoa(params.WithRuntimeLte))
	}
	if lang := strings.TrimSpace(params.OriginalLanguage); lang != "" {
		values.Set("with_original_language", lang)
	}
	return values.Encode()
}

func joinIntSlice(values []int, sep string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, sep)
}

// GetCollection fetches a single TMDB collection (franchise/saga) and returns
// the curated ordered list of parts (all movies). The TMDB endpoint is
// `/collection/{id}`; results come back in TMDB's curated order, which the
// catalog sync preserves as the collection's display order.
//
// The id is the TMDB collection ID (e.g. 86311 for the Marvel Cinematic
// Universe). An id of 0 is rejected here — placeholder templates with
// collection_id=0 are surfaced as failures by the sync path, not silently
// turned into an empty fetch.
func (c *Client) GetCollection(ctx context.Context, id int) (*Collection, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: collection id must be > 0 (got %d)", id)
	}

	var resp collectionResponse
	if err := c.doGet(ctx, fmt.Sprintf("/collection/%d", id), &resp); err != nil {
		return nil, err
	}

	parts := make([]CollectionPart, 0, len(resp.Parts))
	for _, p := range resp.Parts {
		mediaType := p.MediaType
		if mediaType == "" {
			// TMDB /collection/{id} only ever returns movies; older docs
			// sometimes omit the field, so default explicitly rather than
			// leaking an empty string into the resolver.
			mediaType = "movie"
		}
		parts = append(parts, CollectionPart{
			ID:          p.ID,
			MediaType:   mediaType,
			Title:       p.Title,
			ReleaseDate: p.ReleaseDate,
		})
	}

	return &Collection{
		ID:    resp.ID,
		Name:  resp.Name,
		Parts: parts,
	}, nil
}

// GetList fetches the entries of a public, user-authored TMDB list in list
// order. Lists mix movies and TV shows; entries of any other media type are
// skipped. The endpoint pages 20 entries at a time, so reading stops at the
// list's last page or once limit entries are collected. A limit <= 0 reads
// the whole list, capped like the presets at maxCollectionPresetResults.
func (c *Client) GetList(ctx context.Context, id, limit int) ([]CollectionResult, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: list id must be > 0 (got %d)", id)
	}
	if limit <= 0 || limit > maxCollectionPresetResults {
		limit = maxCollectionPresetResults
	}

	results := make([]CollectionResult, 0, min(limit, 100))
	for page := 1; len(results) < limit; page++ {
		var resp listResponse
		if err := c.doGet(ctx, fmt.Sprintf("/list/%d?page=%d", id, page), &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Items {
			if item.MediaType != "movie" && item.MediaType != "tv" {
				continue
			}
			title := item.Title
			if title == "" {
				title = item.Name
			}
			results = append(results, CollectionResult{
				ID:        item.ID,
				MediaType: item.MediaType,
				Title:     title,
			})
		}
		if page >= resp.TotalPages || len(resp.Items) == 0 {
			break
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// GetMediaDetail fetches a single TMDB movie or series with credits, external
// IDs, recommendations, and the appropriate certification feed, returning a
// normalized MediaDetail. mediaType accepts Silo-facing "movie" or "series".
//
// Cast is sorted by TMDB billing order and capped at 24 entries to keep the
// payload bounded.
//
// A detail is cached for mediaDetailCacheTTL, and concurrent callers for one
// title share a fetch. Each caller gets its own copy.
func (c *Client) GetMediaDetail(ctx context.Context, mediaType string, id int) (*MediaDetail, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: media id must be > 0 (got %d)", id)
	}
	var fetch func(context.Context) (*MediaDetail, error)
	switch mediaType {
	case "movie":
		fetch = func(fetchCtx context.Context) (*MediaDetail, error) {
			path := fmt.Sprintf("/movie/%d?append_to_response=credits,external_ids,recommendations,release_dates,keywords", id)
			var resp movieDetailResponse
			if err := c.doGet(fetchCtx, path, &resp); err != nil {
				return nil, err
			}
			return normalizeMovieDetail(&resp), nil
		}
	case "series", "tv":
		mediaType = "tv"
		fetch = func(fetchCtx context.Context) (*MediaDetail, error) {
			path := fmt.Sprintf("/tv/%d?append_to_response=credits,external_ids,recommendations,content_ratings,keywords", id)
			var resp tvDetailResponse
			if err := c.doGet(fetchCtx, path, &resp); err != nil {
				return nil, err
			}
			return normalizeTVDetail(&resp), nil
		}
	default:
		return nil, fmt.Errorf("tmdb: invalid media type for detail: %q", mediaType)
	}

	cacheKey := "media_detail:" + mediaType + ":" + strconv.Itoa(id)
	if c.mediaDetailCache != nil {
		if cached, ok := c.mediaDetailCache.Get(cacheKey); ok {
			return cloneMediaDetail(cached), nil
		}
	}
	// DoChan + select for the same reason as cachedCertification: the shared
	// fetch survives any one caller's disconnect, and each caller stops
	// waiting on its own cancellation.
	resultCh := c.cacheGroup.DoChan(cacheKey, func() (any, error) {
		if c.mediaDetailCache != nil {
			if cached, ok := c.mediaDetailCache.Get(cacheKey); ok {
				return cached, nil
			}
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedFetchTimeout)
		defer cancel()
		detail, err := fetch(fetchCtx)
		if err != nil {
			return nil, err
		}
		if c.mediaDetailCache != nil {
			c.mediaDetailCache.Set(cacheKey, detail, mediaDetailCacheTTL)
		}
		return detail, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		detail, ok := result.Val.(*MediaDetail)
		if !ok {
			return nil, fmt.Errorf("tmdb: invalid cached media detail response")
		}
		return cloneMediaDetail(detail), nil
	}
}

// cloneMediaDetail copies a detail deeply enough that a caller changing its
// copy cannot change the cached one.
func cloneMediaDetail(detail *MediaDetail) *MediaDetail {
	if detail == nil {
		return nil
	}
	cloned := *detail
	cloned.Genres = slices.Clone(detail.Genres)
	if detail.Certifications != nil {
		cloned.Certifications = make(map[string][]string, len(detail.Certifications))
		for country, certs := range detail.Certifications {
			cloned.Certifications[country] = slices.Clone(certs)
		}
	}
	cloned.ProductionCompanies = slices.Clone(detail.ProductionCompanies)
	cloned.KeywordIDs = slices.Clone(detail.KeywordIDs)
	cloned.GenreIDs = slices.Clone(detail.GenreIDs)
	cloned.CompanyIDs = slices.Clone(detail.CompanyIDs)
	cloned.NetworkIDs = slices.Clone(detail.NetworkIDs)
	cloned.OriginCountries = slices.Clone(detail.OriginCountries)
	cloned.Networks = slices.Clone(detail.Networks)
	cloned.Seasons = slices.Clone(detail.Seasons)
	cloned.Cast = slices.Clone(detail.Cast)
	cloned.Creators = slices.Clone(detail.Creators)
	cloned.Recommendations = slices.Clone(detail.Recommendations)
	return &cloned
}

func normalizeMovieDetail(resp *movieDetailResponse) *MediaDetail {
	detail := &MediaDetail{
		MediaType:        "movie",
		ID:               resp.ID,
		IMDbID:           resp.IMDbID,
		Title:            resp.Title,
		OriginalTitle:    resp.OriginalTitle,
		Tagline:          resp.Tagline,
		Overview:         resp.Overview,
		PosterPath:       resp.PosterPath,
		BackdropPath:     resp.BackdropPath,
		ReleaseDate:      resp.ReleaseDate,
		Year:             releaseYear(resp.ReleaseDate),
		Runtime:          resp.Runtime,
		Genres:           namesFromGenres(resp.Genres),
		VoteAverage:      resp.VoteAverage,
		VoteCount:        resp.VoteCount,
		Status:           resp.Status,
		Homepage:         resp.Homepage,
		ContentRating:    pickMovieCertification(resp.ReleaseDates),
		USCertification:  pickUSMovieCertification(resp.ReleaseDates),
		Certifications:   movieCertifications(resp.ReleaseDates),
		OriginalLanguage: resp.OriginalLanguage,
		KeywordIDs:       keywordIDs(resp.Keywords.Keywords, resp.Keywords.Results),
		GenreIDs:         idsFromGenres(resp.Genres),
		OriginCountries:  resp.OriginCountry,
	}
	for _, company := range resp.ProductionCompanies {
		if name := strings.TrimSpace(company.Name); name != "" {
			detail.ProductionCompanies = append(detail.ProductionCompanies, name)
		}
		if company.ID > 0 {
			detail.CompanyIDs = append(detail.CompanyIDs, company.ID)
		}
	}
	if resp.ExternalIDs != nil {
		detail.TVDBID = resp.ExternalIDs.TVDBID
		if detail.IMDbID == "" {
			detail.IMDbID = resp.ExternalIDs.IMDbID
		}
	}
	if resp.Credits != nil {
		detail.Cast = normalizeCast(resp.Credits.Cast)
		detail.Director = pickDirector(resp.Credits.Crew)
	}
	if resp.Recommendations != nil {
		detail.Recommendations = make([]MediaResult, 0, len(resp.Recommendations.Results))
		for _, item := range resp.Recommendations.Results {
			detail.Recommendations = append(detail.Recommendations, MediaResult{
				ID:           item.ID,
				MediaType:    "movie",
				Title:        item.Title,
				Overview:     item.Overview,
				PosterPath:   item.PosterPath,
				BackdropPath: item.BackdropPath,
				ReleaseDate:  item.ReleaseDate,
				Year:         releaseYear(item.ReleaseDate),
				Popularity:   item.Popularity,
				VoteAverage:  item.VoteAverage,
			})
		}
	}
	return detail
}

func normalizeTVDetail(resp *tvDetailResponse) *MediaDetail {
	detail := &MediaDetail{
		MediaType:        "series",
		ID:               resp.ID,
		Title:            resp.Name,
		OriginalTitle:    resp.OriginalName,
		Tagline:          resp.Tagline,
		Overview:         resp.Overview,
		PosterPath:       resp.PosterPath,
		BackdropPath:     resp.BackdropPath,
		ReleaseDate:      resp.FirstAirDate,
		FirstAirDate:     resp.FirstAirDate,
		LastAirDate:      resp.LastAirDate,
		Year:             releaseYear(resp.FirstAirDate),
		Genres:           namesFromGenres(resp.Genres),
		VoteAverage:      resp.VoteAverage,
		VoteCount:        resp.VoteCount,
		Status:           resp.Status,
		Homepage:         resp.Homepage,
		NumberOfSeasons:  resp.NumberOfSeasons,
		NumberOfEpisodes: resp.NumberOfEpisodes,
		ContentRating:    pickTVRating(resp.ContentRatings),
		USCertification:  pickUSTVRating(resp.ContentRatings),
		Certifications:   tvCertifications(resp.ContentRatings),
		OriginalLanguage: resp.OriginalLanguage,
		KeywordIDs:       keywordIDs(resp.Keywords.Keywords, resp.Keywords.Results),
		GenreIDs:         idsFromGenres(resp.Genres),
		OriginCountries:  resp.OriginCountry,
	}
	if len(resp.EpisodeRunTime) > 0 {
		detail.Runtime = resp.EpisodeRunTime[0]
	}
	for _, network := range resp.Networks {
		if name := strings.TrimSpace(network.Name); name != "" {
			detail.Networks = append(detail.Networks, name)
		}
		if network.ID > 0 {
			detail.NetworkIDs = append(detail.NetworkIDs, network.ID)
		}
	}
	for _, season := range resp.Seasons {
		if season.SeasonNumber < 0 {
			continue
		}
		detail.Seasons = append(detail.Seasons, SeasonSummary{
			Number:       season.SeasonNumber,
			Name:         strings.TrimSpace(season.Name),
			EpisodeCount: season.EpisodeCount,
			AirDate:      season.AirDate,
			PosterPath:   season.PosterPath,
		})
	}
	if resp.ExternalIDs != nil {
		detail.IMDbID = resp.ExternalIDs.IMDbID
		detail.TVDBID = resp.ExternalIDs.TVDBID
	}
	if resp.Credits != nil {
		detail.Cast = normalizeCast(resp.Credits.Cast)
	}
	for _, person := range resp.CreatedBy {
		if name := strings.TrimSpace(person.Name); name != "" {
			detail.Creators = append(detail.Creators, name)
		}
	}
	if resp.Recommendations != nil {
		detail.Recommendations = make([]MediaResult, 0, len(resp.Recommendations.Results))
		for _, item := range resp.Recommendations.Results {
			detail.Recommendations = append(detail.Recommendations, MediaResult{
				ID:           item.ID,
				MediaType:    "series",
				Title:        item.Name,
				Overview:     item.Overview,
				PosterPath:   item.PosterPath,
				BackdropPath: item.BackdropPath,
				ReleaseDate:  item.FirstAirDate,
				Year:         releaseYear(item.FirstAirDate),
				Popularity:   item.Popularity,
				VoteAverage:  item.VoteAverage,
			})
		}
	}
	return detail
}

func idsFromGenres(genres []genreEntry) []int {
	var out []int
	for _, g := range genres {
		if g.ID > 0 {
			out = append(out, g.ID)
		}
	}
	return out
}

func namesFromGenres(genres []genreEntry) []string {
	if len(genres) == 0 {
		return nil
	}
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		if name := strings.TrimSpace(g.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// keywordIDs flattens one or more TMDB keyword lists (movies use the
// "keywords" array, tv uses "results") into their numeric ids.
func keywordIDs(groups ...[]idEntry) []int {
	var out []int
	for _, g := range groups {
		for _, e := range g {
			out = append(out, e.ID)
		}
	}
	return out
}

// normalizeCast sorts by billing order and caps the result so the response
// payload stays bounded — the request detail UI surfaces only the top of the
// list anyway.
func normalizeCast(cast []castEntry) []MediaCastMember {
	if len(cast) == 0 {
		return nil
	}
	sorted := make([]castEntry, len(cast))
	copy(sorted, cast)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Order < sorted[j].Order
	})
	const maxCast = 24
	if len(sorted) > maxCast {
		sorted = sorted[:maxCast]
	}
	out := make([]MediaCastMember, 0, len(sorted))
	for _, member := range sorted {
		name := strings.TrimSpace(member.Name)
		if name == "" {
			continue
		}
		out = append(out, MediaCastMember{
			Name:        name,
			Character:   strings.TrimSpace(member.Character),
			ProfilePath: member.ProfilePath,
			Order:       member.Order,
		})
	}
	return out
}

func pickDirector(crew []crewEntry) string {
	for _, member := range crew {
		if strings.EqualFold(member.Job, "Director") {
			return strings.TrimSpace(member.Name)
		}
	}
	return ""
}

// pickMovieCertification picks the US theatrical certification if available,
// then falls back to any non-empty US certification, then any non-empty
// certification at all. Type 3 is theatrical in TMDB's release-type taxonomy.
func pickMovieCertification(rd *releaseDatesResponse) string {
	if rd == nil {
		return ""
	}
	var fallbackUS, fallbackAny string
	for _, country := range rd.Results {
		isUS := strings.EqualFold(country.ISO3166, "US")
		for _, entry := range country.ReleaseDates {
			cert := strings.TrimSpace(entry.Certification)
			if cert == "" {
				continue
			}
			if isUS && entry.Type == 3 {
				return cert
			}
			if isUS && fallbackUS == "" {
				fallbackUS = cert
			}
			if fallbackAny == "" {
				fallbackAny = cert
			}
		}
	}
	if fallbackUS != "" {
		return fallbackUS
	}
	return fallbackAny
}

func pickTVRating(cr *contentRatingsResponse) string {
	if cr == nil {
		return ""
	}
	var fallback string
	for _, entry := range cr.Results {
		rating := strings.TrimSpace(entry.Rating)
		if rating == "" {
			continue
		}
		if strings.EqualFold(entry.ISO3166, "US") {
			return rating
		}
		if fallback == "" {
			fallback = rating
		}
	}
	return fallback
}

// GetExternalIDs fetches external IDs for a TMDB movie or TV entry.
// Uses the dedicated external_ids endpoint instead of append_to_response on
// the full detail, which would return a 100+ KB payload to extract a handful
// of identifiers.
func (c *Client) GetExternalIDs(ctx context.Context, mediaType string, id int) (*ExternalIDs, error) {
	path, err := externalIDsPath(mediaType, id)
	if err != nil {
		return nil, err
	}

	cacheKey := "external_ids:" + path
	if c.externalIDCache != nil {
		if cached, ok := c.externalIDCache.Get(cacheKey); ok {
			return cloneExternalIDs(cached), nil
		}
	}

	value, err, _ := c.cacheGroup.Do(cacheKey, func() (any, error) {
		if c.externalIDCache != nil {
			if cached, ok := c.externalIDCache.Get(cacheKey); ok {
				return cached, nil
			}
		}
		ids, err := c.fetchExternalIDs(ctx, path)
		if err != nil {
			return nil, err
		}
		cached := cloneExternalIDs(ids)
		if c.externalIDCache != nil && c.responseCacheTTL > 0 {
			c.externalIDCache.Set(cacheKey, cached, c.responseCacheTTL)
		}
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	ids, ok := value.(*ExternalIDs)
	if !ok {
		return nil, fmt.Errorf("tmdb: invalid cached external IDs response")
	}
	return cloneExternalIDs(ids), nil
}

// RefreshExternalIDs fetches an entry's external IDs from TMDB, skipping both
// the cache and any in-flight cached fetch, and caches the result. It serves
// callers acting on an ID that may have just been added on TMDB (for example an
// admin retrying a request after fixing it upstream).
func (c *Client) RefreshExternalIDs(ctx context.Context, mediaType string, id int) (*ExternalIDs, error) {
	path, err := externalIDsPath(mediaType, id)
	if err != nil {
		return nil, err
	}
	ids, err := c.fetchExternalIDs(ctx, path)
	if err != nil {
		return nil, err
	}
	if c.externalIDCache != nil && c.responseCacheTTL > 0 {
		c.externalIDCache.Set("external_ids:"+path, cloneExternalIDs(ids), c.responseCacheTTL)
	}
	return cloneExternalIDs(ids), nil
}

func externalIDsPath(mediaType string, id int) (string, error) {
	switch mediaType {
	case "movie":
		return fmt.Sprintf("/movie/%d/external_ids", id), nil
	case "tv":
		return fmt.Sprintf("/tv/%d/external_ids", id), nil
	default:
		return "", fmt.Errorf("tmdb: invalid media type: %q", mediaType)
	}
}

func cloneExternalIDs(ids *ExternalIDs) *ExternalIDs {
	if ids == nil {
		return nil
	}
	cloned := *ids
	return &cloned
}

// GetCertification returns the US content rating for a TMDB title ("PG-13",
// "TV-MA", ...), or "" when the title has no US certification. It uses the
// dedicated release_dates / content_ratings sub-resources instead of the full
// detail payload for the same reason GetExternalIDs does: the detail response
// is 100+ KB and cached only briefly, while these are a country list of a few
// KB.
//
// Unlike GetMediaDetail's display rating, this deliberately does NOT fall
// back to another country's certification: the value feeds the US-scale
// parental-control ladder, where a foreign "PG" (Canada, Australia, ...) is
// not evidence of US-PG content. No US entry means unresolved, which the
// ladder treats as fail-closed. mediaType accepts Silo-facing
// "movie"/"series" plus TMDB-facing "tv".
func (c *Client) GetCertification(ctx context.Context, mediaType string, id int) (string, error) {
	path, err := certificationPath(mediaType, id)
	if err != nil {
		return "", err
	}
	return c.cachedCertification(ctx, "certification:"+path, func(fetchCtx context.Context) (string, error) {
		return c.fetchCertification(fetchCtx, mediaType, path)
	})
}

// GetCertifications returns every country's certifications for a title,
// keyed by ISO 3166-1 code, from the same small sub-resource and cache as
// GetCertification. Request routing reads a title's own country's rating
// from it when the title has no US one; the parental-control path keeps to
// GetCertification.
func (c *Client) GetCertifications(ctx context.Context, mediaType string, id int) (map[string][]string, error) {
	path, err := certificationPath(mediaType, id)
	if err != nil {
		return nil, err
	}
	raw, err := c.cachedCertification(ctx, "certifications:"+path, func(fetchCtx context.Context) (string, error) {
		certs, err := c.fetchCertifications(fetchCtx, mediaType, path)
		if err != nil {
			return "", err
		}
		encoded, err := json.Marshal(certs)
		return string(encoded), err
	})
	if err != nil {
		return nil, err
	}
	var certs map[string][]string
	if err := json.Unmarshal([]byte(raw), &certs); err != nil {
		return nil, fmt.Errorf("tmdb: invalid cached certifications: %w", err)
	}
	return certs, nil
}

func certificationPath(mediaType string, id int) (string, error) {
	switch mediaType {
	case "movie":
		return fmt.Sprintf("/movie/%d/release_dates", id), nil
	case "series", "tv":
		return fmt.Sprintf("/tv/%d/content_ratings", id), nil
	default:
		return "", fmt.Errorf("tmdb: invalid media type: %q", mediaType)
	}
}

// cachedCertification fetches one certification value once across concurrent
// callers and caches it.
func (c *Client) cachedCertification(ctx context.Context, cacheKey string, fetch func(context.Context) (string, error)) (string, error) {
	if c.certificationCache != nil {
		if cached, ok := c.certificationCache.Get(cacheKey); ok {
			return cached, nil
		}
	}

	// DoChan + select rather than Do: the shared fetch must survive any one
	// caller's disconnect (it runs detached with its own bound), while each
	// caller must still be able to stop waiting on its own cancellation
	// instead of being pinned for up to the fetch timeout.
	resultCh := c.cacheGroup.DoChan(cacheKey, func() (any, error) {
		if c.certificationCache != nil {
			if cached, ok := c.certificationCache.Get(cacheKey); ok {
				return cached, nil
			}
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedFetchTimeout)
		defer cancel()
		cert, err := fetch(fetchCtx)
		if err != nil {
			return nil, err
		}
		// "" (no certification on TMDB) is cached too: it is by far the most
		// common case and refetching it on every page load would defeat the
		// cache exactly where fail-closed filtering needs it most.
		if c.certificationCache != nil {
			c.certificationCache.Set(cacheKey, cert, certificationCacheTTL)
		}
		return cert, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return "", result.Err
		}
		cert, ok := result.Val.(string)
		if !ok {
			return "", fmt.Errorf("tmdb: invalid cached certification response")
		}
		return cert, nil
	}
}

func (c *Client) fetchCertifications(ctx context.Context, mediaType, path string) (map[string][]string, error) {
	if mediaType == "movie" {
		var resp releaseDatesResponse
		if err := c.doGet(ctx, path, &resp); err != nil {
			return nil, err
		}
		return movieCertifications(&resp), nil
	}
	var resp contentRatingsResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	return tvCertifications(&resp), nil
}

// USCertificationFrom picks the US rating out of GetCertifications' answer
// the way GetCertification does: the strictest recognized movie
// certification, a series' first rating.
func USCertificationFrom(mediaType string, certs map[string][]string) string {
	us := certs["US"]
	if mediaType != "movie" {
		if len(us) > 0 {
			return us[0]
		}
		return ""
	}
	var picked string
	pickedRank := -1
	for _, cert := range us {
		if rank := usCertificationRank[strings.ToUpper(cert)]; rank > pickedRank || picked == "" {
			picked, pickedRank = cert, rank
		}
	}
	return picked
}

// movieCertifications lists each country's non-empty certifications, in
// TMDB's order, without repeats.
func movieCertifications(rd *releaseDatesResponse) map[string][]string {
	out := map[string][]string{}
	if rd == nil {
		return out
	}
	for _, country := range rd.Results {
		code := strings.ToUpper(strings.TrimSpace(country.ISO3166))
		for _, entry := range country.ReleaseDates {
			if cert := strings.TrimSpace(entry.Certification); code != "" && cert != "" && !slices.Contains(out[code], cert) {
				out[code] = append(out[code], cert)
			}
		}
	}
	return out
}

// tvCertifications lists each country's non-empty rating.
func tvCertifications(cr *contentRatingsResponse) map[string][]string {
	out := map[string][]string{}
	if cr == nil {
		return out
	}
	for _, entry := range cr.Results {
		code := strings.ToUpper(strings.TrimSpace(entry.ISO3166))
		if rating := strings.TrimSpace(entry.Rating); code != "" && rating != "" && !slices.Contains(out[code], rating) {
			out[code] = append(out[code], rating)
		}
	}
	return out
}

func (c *Client) fetchCertification(ctx context.Context, mediaType, path string) (string, error) {
	if mediaType == "movie" {
		var resp releaseDatesResponse
		if err := c.doGet(ctx, path, &resp); err != nil {
			return "", err
		}
		return pickUSMovieCertification(&resp), nil
	}
	var resp contentRatingsResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return "", err
	}
	return pickUSTVRating(&resp), nil
}

// usCertificationRank orders US movie certifications for strictest-wins
// resolution across multiple release entries. Mirrors TMDB's own
// /certification/movie/list ordering. Unknown strings (including "NR") rank
// -1 and lose to any recognized rating; a title with ONLY unknown/NR entries
// resolves to that string and fails closed downstream.
var usCertificationRank = map[string]int{
	"G": 1, "PG": 2, "PG-13": 3, "R": 4, "NC-17": 5,
}

// pickUSMovieCertification is the enforcement-path variant of
// pickMovieCertification: US entries only, no foreign fallback. A title can
// carry several US entries whose certifications disagree — festival "NR" next
// to a theatrical "PG-13", or re-releases rated "PG" and "R" — and TMDB's
// entry order is not meaningful, so the STRICTEST recognized rating wins:
// enforcement must not admit a title on its most lenient certificate.
func pickUSMovieCertification(rd *releaseDatesResponse) string {
	if rd == nil {
		return ""
	}
	var picked string
	pickedRank := -1
	for _, country := range rd.Results {
		if !strings.EqualFold(country.ISO3166, "US") {
			continue
		}
		for _, entry := range country.ReleaseDates {
			cert := strings.TrimSpace(entry.Certification)
			if cert == "" {
				continue
			}
			rank := usCertificationRank[strings.ToUpper(cert)] // unknown -> 0
			if rank > pickedRank || picked == "" {
				picked = cert
				pickedRank = rank
			}
		}
	}
	return picked
}

// pickUSTVRating is the enforcement-path variant of pickTVRating: US only.
func pickUSTVRating(cr *contentRatingsResponse) string {
	if cr == nil {
		return ""
	}
	for _, entry := range cr.Results {
		if strings.EqualFold(entry.ISO3166, "US") {
			if rating := strings.TrimSpace(entry.Rating); rating != "" {
				return rating
			}
		}
	}
	return ""
}

// External ID sources accepted by FindByExternalID.
const (
	ExternalSourceIMDb = "imdb_id"
	ExternalSourceTVDB = "tvdb_id"
)

// FindByExternalID looks a title up by another provider's ID through TMDB's
// /find/{external_id} endpoint. source is ExternalSourceIMDb or
// ExternalSourceTVDB. It returns every movie and series TMDB lists for the
// ID, with Silo-facing media types ("movie", "series"); no result is an empty
// slice, not an error. Results are not cached: callers use it to recover a
// title whose TMDB ID stopped resolving, which is rare and wants a fresh
// answer.
func (c *Client) FindByExternalID(ctx context.Context, source, externalID string) ([]MediaResult, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return nil, fmt.Errorf("tmdb: external id must not be empty")
	}
	switch source {
	case ExternalSourceIMDb, ExternalSourceTVDB:
	default:
		return nil, fmt.Errorf("tmdb: invalid external source %q", source)
	}
	path := "/find/" + url.PathEscape(externalID) + "?external_source=" + url.QueryEscape(source)
	var resp findResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	out := make([]MediaResult, 0, len(resp.MovieResults)+len(resp.TVResults))
	for _, item := range resp.MovieResults {
		out = append(out, MediaResult{
			ID:           item.ID,
			MediaType:    "movie",
			Title:        item.Title,
			Overview:     item.Overview,
			PosterPath:   item.PosterPath,
			BackdropPath: item.BackdropPath,
			ReleaseDate:  item.ReleaseDate,
			Year:         releaseYear(item.ReleaseDate),
			Popularity:   item.Popularity,
			VoteAverage:  item.VoteAverage,
		})
	}
	for _, item := range resp.TVResults {
		out = append(out, MediaResult{
			ID:           item.ID,
			MediaType:    "series",
			Title:        item.Name,
			Overview:     item.Overview,
			PosterPath:   item.PosterPath,
			BackdropPath: item.BackdropPath,
			ReleaseDate:  item.FirstAirDate,
			Year:         releaseYear(item.FirstAirDate),
			Popularity:   item.Popularity,
			VoteAverage:  item.VoteAverage,
		})
	}
	return out, nil
}

func (c *Client) fetchExternalIDs(ctx context.Context, path string) (*ExternalIDs, error) {
	var resp ExternalIDs
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

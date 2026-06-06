package introdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"golang.org/x/time/rate"
)

const (
	maxRetries      = 3
	maxResponseBody = 1 << 20 // 1 MB
	defaultTimeout  = 15 * time.Second
	defaultCacheTTL = 24 * time.Hour
)

// Client is an HTTP client for the TheIntroDB /v3/media endpoint. Each
// instance has its own rate limiter and response cache; concurrent fetches
// for the same lookup key collapse to a single HTTP round trip via the cache.
type Client struct {
	httpClient *http.Client
	mu         sync.RWMutex
	apiKey     string
	baseURL    string
	limiter    *rate.Limiter
	cache      *cache.TTLCache[*mediaResponse]
	cacheTTL   time.Duration
}

// NewClient builds a Client with the canonical rate limit and cache TTL.
// The apiKey may be empty — TheIntroDB serves read traffic without a key,
// the key only gates access to the caller's own pending submissions.
func NewClient(apiKey string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    DefaultBaseURL,
		// TheIntroDB documents 30 requests / 10 seconds per IP. We stay
		// conservatively below that: 2 req/s sustained, burst 5.
		limiter:  rate.NewLimiter(2, 5),
		cache:    cache.NewTTLCache[*mediaResponse](),
		cacheTTL: defaultCacheTTL,
	}
}

// SetBaseURL overrides the API base URL (used by tests).
func (c *Client) SetBaseURL(u string) {
	c.mu.Lock()
	c.baseURL = u
	c.mu.Unlock()
}

// SetAPIKey rotates the bearer token in-place. Safe to call concurrently
// with in-flight requests; subsequent requests use the new key.
func (c *Client) SetAPIKey(apiKey string) {
	c.mu.Lock()
	c.apiKey = strings.TrimSpace(apiKey)
	c.mu.Unlock()
}

// Close releases the background sweeper goroutine inside the response cache.
func (c *Client) Close() {
	if c.cache != nil {
		c.cache.Close()
	}
}

// FetchEpisode looks up segment timestamps for a TV episode.
// At least one of tmdbID, tvdbID, or imdbID must be non-empty. When several are
// present the preference order is tmdb → tvdb → imdb (matching TheIntroDB's own
// clients).
func (c *Client) FetchEpisode(ctx context.Context, tmdbID, tvdbID, imdbID string, season, episode int, durationMS int64) (*mediaResponse, error) {
	if tmdbID == "" && tvdbID == "" && imdbID == "" {
		return nil, fmt.Errorf("introdb: tmdb_id, tvdb_id, or imdb_id required")
	}
	if season <= 0 || episode <= 0 {
		return nil, fmt.Errorf("introdb: episode lookup requires season and episode > 0 (got %d/%d)", season, episode)
	}
	q := url.Values{}
	setPreferredID(q, tmdbID, tvdbID, imdbID)
	q.Set("season", strconv.Itoa(season))
	q.Set("episode", strconv.Itoa(episode))
	if durationMS > 0 {
		q.Set("duration_ms", strconv.FormatInt(durationMS, 10))
	}
	return c.fetch(ctx, q, cacheKeyEpisode(tmdbID, tvdbID, imdbID, season, episode, durationMS))
}

// FetchMovie looks up segment timestamps for a movie.
// At least one of tmdbID, tvdbID, or imdbID must be non-empty.
func (c *Client) FetchMovie(ctx context.Context, tmdbID, tvdbID, imdbID string, durationMS int64) (*mediaResponse, error) {
	if tmdbID == "" && tvdbID == "" && imdbID == "" {
		return nil, fmt.Errorf("introdb: tmdb_id, tvdb_id, or imdb_id required")
	}
	q := url.Values{}
	setPreferredID(q, tmdbID, tvdbID, imdbID)
	if durationMS > 0 {
		q.Set("duration_ms", strconv.FormatInt(durationMS, 10))
	}
	return c.fetch(ctx, q, cacheKeyMovie(tmdbID, tvdbID, imdbID, durationMS))
}

// setPreferredID writes exactly one id query parameter, preferring tmdb, then
// tvdb, then imdb. At least one is assumed non-empty by the callers.
func setPreferredID(q url.Values, tmdbID, tvdbID, imdbID string) {
	switch {
	case tmdbID != "":
		q.Set("tmdb_id", tmdbID)
	case tvdbID != "":
		q.Set("tvdb_id", tvdbID)
	default:
		q.Set("imdb_id", imdbID)
	}
}

func (c *Client) fetch(ctx context.Context, q url.Values, key string) (*mediaResponse, error) {
	if cached, ok := c.cache.Get(key); ok {
		return cached, nil
	}

	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	c.mu.RUnlock()

	reqURL := baseURL + "/media?" + q.Encode()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("introdb: create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Silo-Server/markers")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("introdb: request failed: %w", err)
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			// Cache negatives too so the next playback start doesn't trigger
			// another fetch for known-empty content.
			c.cache.Set(key, nil, c.cacheTTL)
			return nil, nil
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt < maxRetries {
				backoff := retryAfterOrDefault(resp, attempt)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("introdb: rate limited after %d retries", maxRetries)
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt) * time.Second
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("introdb: server error %d after %d retries", resp.StatusCode, maxRetries)
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
			resp.Body.Close()
			return nil, fmt.Errorf("introdb: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var out mediaResponse
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("introdb: decode response: %w", decodeErr)
		}
		c.cache.Set(key, &out, c.cacheTTL)
		return &out, nil
	}
	return nil, fmt.Errorf("introdb: max retries exceeded")
}

func retryAfterOrDefault(resp *http.Response, attempt int) time.Duration {
	if val := resp.Header.Get("Retry-After"); val != "" {
		if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(1<<attempt) * time.Second
}

func cacheKeyEpisode(tmdbID, tvdbID, imdbID string, season, episode int, durationMS int64) string {
	switch {
	case tmdbID != "":
		return fmt.Sprintf("tmdb:%s:s%de%d:d%d", tmdbID, season, episode, durationMS)
	case tvdbID != "":
		return fmt.Sprintf("tvdb:%s:s%de%d:d%d", tvdbID, season, episode, durationMS)
	default:
		return fmt.Sprintf("imdb:%s:s%de%d:d%d", imdbID, season, episode, durationMS)
	}
}

func cacheKeyMovie(tmdbID, tvdbID, imdbID string, durationMS int64) string {
	switch {
	case tmdbID != "":
		return fmt.Sprintf("tmdb:movie:%s:d%d", tmdbID, durationMS)
	case tvdbID != "":
		return fmt.Sprintf("tvdb:movie:%s:d%d", tvdbID, durationMS)
	default:
		return fmt.Sprintf("imdb:movie:%s:d%d", imdbID, durationMS)
	}
}

// submitSegment contributes a single segment via POST /v3/submit. The API key
// is required (submissions are credited to that account); returns an error if
// none is configured. Submissions are not cached. On 429 the usage-limit reset
// is surfaced in the error so callers can back off.
func (c *Client) submitSegment(ctx context.Context, body submitRequest) (*submitResponse, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	c.mu.RUnlock()
	if apiKey == "" {
		return nil, fmt.Errorf("introdb: submit requires an API key")
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("introdb: marshal submit: %w", err)
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/submit", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("introdb: create submit request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Silo-Server/markers")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introdb: submit request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("introdb: submit usage-limited; retry after %ds", usageResetSeconds(resp))
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		return nil, fmt.Errorf("introdb: submit HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out submitResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("introdb: decode submit response: %w", err)
	}
	return &out, nil
}

// fetchUserStats validates the configured key and returns contribution stats
// via GET /v3/user/stats.
func (c *Client) fetchUserStats(ctx context.Context) (*userStatsResponse, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	c.mu.RUnlock()
	if apiKey == "" {
		return nil, fmt.Errorf("introdb: user stats require an API key")
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/user/stats", nil)
	if err != nil {
		return nil, fmt.Errorf("introdb: create stats request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Silo-Server/markers")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introdb: stats request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		return nil, fmt.Errorf("introdb: stats HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out userStatsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("introdb: decode stats response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("introdb: stats error: %s", out.Error)
	}
	return &out, nil
}

// usageResetSeconds reads the usage/rate reset hint from a 429 response.
func usageResetSeconds(resp *http.Response) int {
	for _, h := range []string{"X-UsageLimit-Reset", "X-RateLimit-Reset", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			if s, err := strconv.Atoi(v); err == nil && s > 0 {
				return s
			}
		}
	}
	return 0
}

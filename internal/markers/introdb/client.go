package introdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
func (c *Client) SetBaseURL(u string) { c.baseURL = u }

// SetAPIKey rotates the bearer token in-place. Safe to call concurrently
// with in-flight requests; subsequent requests use the new key.
func (c *Client) SetAPIKey(apiKey string) { c.apiKey = strings.TrimSpace(apiKey) }

// Close releases the background sweeper goroutine inside the response cache.
func (c *Client) Close() {
	if c.cache != nil {
		c.cache.Close()
	}
}

// FetchEpisode looks up segment timestamps for a TV episode.
// At least one of tmdbID or imdbID must be non-empty.
func (c *Client) FetchEpisode(ctx context.Context, tmdbID, imdbID string, season, episode int, durationMS int64) (*mediaResponse, error) {
	if tmdbID == "" && imdbID == "" {
		return nil, fmt.Errorf("introdb: tmdb_id or imdb_id required")
	}
	if season <= 0 || episode <= 0 {
		return nil, fmt.Errorf("introdb: episode lookup requires season and episode > 0 (got %d/%d)", season, episode)
	}
	q := url.Values{}
	if tmdbID != "" {
		q.Set("tmdb_id", tmdbID)
	} else {
		q.Set("imdb_id", imdbID)
	}
	q.Set("season", strconv.Itoa(season))
	q.Set("episode", strconv.Itoa(episode))
	if durationMS > 0 {
		q.Set("duration_ms", strconv.FormatInt(durationMS, 10))
	}
	return c.fetch(ctx, q, cacheKeyEpisode(tmdbID, imdbID, season, episode))
}

// FetchMovie looks up segment timestamps for a movie.
// At least one of tmdbID or imdbID must be non-empty.
func (c *Client) FetchMovie(ctx context.Context, tmdbID, imdbID string, durationMS int64) (*mediaResponse, error) {
	if tmdbID == "" && imdbID == "" {
		return nil, fmt.Errorf("introdb: tmdb_id or imdb_id required")
	}
	q := url.Values{}
	if tmdbID != "" {
		q.Set("tmdb_id", tmdbID)
	} else {
		q.Set("imdb_id", imdbID)
	}
	if durationMS > 0 {
		q.Set("duration_ms", strconv.FormatInt(durationMS, 10))
	}
	return c.fetch(ctx, q, cacheKeyMovie(tmdbID, imdbID))
}

func (c *Client) fetch(ctx context.Context, q url.Values, key string) (*mediaResponse, error) {
	if cached, ok := c.cache.Get(key); ok {
		return cached, nil
	}

	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	reqURL := c.baseURL + "/media?" + q.Encode()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("introdb: create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Silo-Server/markers")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
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

func cacheKeyEpisode(tmdbID, imdbID string, season, episode int) string {
	if tmdbID != "" {
		return fmt.Sprintf("tmdb:%s:s%de%d", tmdbID, season, episode)
	}
	return fmt.Sprintf("imdb:%s:s%de%d", imdbID, season, episode)
}

func cacheKeyMovie(tmdbID, imdbID string) string {
	if tmdbID != "" {
		return "tmdb:movie:" + tmdbID
	}
	return "imdb:movie:" + imdbID
}

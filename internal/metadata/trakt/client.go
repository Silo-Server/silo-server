package trakt

import (
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

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL             = "https://api.trakt.tv"
	maxRetries                 = 3
	maxResponseBody            = 2 << 20
	maxCollectionPresetResults = 500
	defaultCollectionPageLimit = 20
	defaultCollectionRateLimit = 5
	traktAPIVersion            = "2"
)

// Client is an HTTP client for Trakt collection/discovery feeds.
type Client struct {
	httpClient *http.Client
	clientID   string
	baseURL    string
	limiter    *rate.Limiter
}

// NewClient creates a Trakt client. clientID is required by Trakt for API calls.
func NewClient(clientID string, rateLimit int) *Client {
	if rateLimit <= 0 {
		rateLimit = defaultCollectionRateLimit
	}
	return &Client{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		clientID:   strings.TrimSpace(clientID),
		baseURL:    defaultBaseURL,
		limiter:    rate.NewLimiter(rate.Limit(rateLimit), rateLimit),
	}
}

// SetBaseURL overrides the API base URL. Used by tests.
func (c *Client) SetBaseURL(raw string) {
	c.baseURL = strings.TrimRight(raw, "/")
}

// GetCollectionPreset fetches a normalized Trakt discovery feed.
func (c *Client) GetCollectionPreset(ctx context.Context, preset, mediaType string, limit int, accessToken string) ([]CollectionEntry, error) {
	path, err := collectionPresetPath(preset, mediaType)
	if err != nil {
		return nil, err
	}
	if preset == "recommended" && strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("trakt: recommended preset requires access token")
	}
	if limit <= 0 {
		limit = defaultCollectionPageLimit
	}
	if limit > maxCollectionPresetResults {
		limit = maxCollectionPresetResults
	}

	results := make([]CollectionEntry, 0, limit)
	for page := 1; len(results) < limit; page++ {
		pageLimit := limit - len(results)
		if pageLimit > defaultCollectionPageLimit {
			pageLimit = defaultCollectionPageLimit
		}
		reqPath := fmt.Sprintf("%s?page=%d&limit=%d", path, page, pageLimit)

		var pageEntries []CollectionEntry
		switch preset {
		case "trending":
			pageEntries, err = c.getTrending(ctx, reqPath, mediaType, accessToken)
		case "popular", "recommended":
			pageEntries, err = c.getMediaList(ctx, reqPath, mediaType, accessToken)
		default:
			err = fmt.Errorf("trakt: invalid preset %q", preset)
		}
		if err != nil {
			return nil, err
		}
		for i := range pageEntries {
			pageEntries[i].Rank = len(results) + i + 1
		}
		results = append(results, pageEntries...)
		if len(pageEntries) < pageLimit {
			break
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func collectionPresetPath(preset, mediaType string) (string, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return "", fmt.Errorf("trakt: media_type must be movie or tv")
	}
	switch preset {
	case "trending", "popular":
		if mediaType == "movie" {
			return "/movies/" + preset, nil
		}
		return "/shows/" + preset, nil
	case "recommended":
		if mediaType == "movie" {
			return "/recommendations/movies", nil
		}
		return "/recommendations/shows", nil
	default:
		return "", fmt.Errorf("trakt: preset must be trending, popular, or recommended")
	}
}

// GetUserList fetches the items of a user-authored Trakt list
// (/users/{user}/lists/{list}/items), preserving list order. Lists mix
// movies and shows; unknown item types (people, episodes, seasons) are
// skipped. Public lists need only the app client id; accessToken is passed
// through for the owner's private lists.
func (c *Client) GetUserList(ctx context.Context, user, list string, limit int, accessToken string) ([]CollectionEntry, error) {
	user = strings.TrimSpace(user)
	list = strings.TrimSpace(list)
	if user == "" || list == "" {
		return nil, errors.New("trakt: user list requires user and list slug")
	}
	if limit <= 0 {
		limit = defaultCollectionPageLimit
	}
	if limit > maxCollectionPresetResults {
		limit = maxCollectionPresetResults
	}

	basePath := fmt.Sprintf("/users/%s/lists/%s/items", url.PathEscape(user), url.PathEscape(list))
	results := make([]CollectionEntry, 0, limit)
	for page := 1; len(results) < limit; page++ {
		pageLimit := limit - len(results)
		if pageLimit > defaultCollectionPageLimit {
			pageLimit = defaultCollectionPageLimit
		}
		var resp []struct {
			Rank  int         `json:"rank"`
			Type  string      `json:"type"`
			Movie *traktMedia `json:"movie"`
			Show  *traktMedia `json:"show"`
		}
		reqPath := fmt.Sprintf("%s?page=%d&limit=%d", basePath, page, pageLimit)
		if err := c.doGet(ctx, reqPath, accessToken, &resp); err != nil {
			return nil, err
		}
		if len(resp) == 0 {
			break
		}
		for _, row := range resp {
			var entry CollectionEntry
			switch {
			case row.Type == "movie" && row.Movie != nil:
				entry = row.Movie.entry("movie")
			case row.Type == "show" && row.Show != nil:
				entry = row.Show.entry("tv")
			default:
				continue
			}
			entry.Rank = row.Rank
			results = append(results, entry)
			if len(results) >= limit {
				break
			}
		}
		if len(resp) < pageLimit {
			break
		}
	}
	return results, nil
}

func (c *Client) getTrending(ctx context.Context, path, mediaType, accessToken string) ([]CollectionEntry, error) {
	if mediaType == "movie" {
		var resp []struct {
			Movie traktMedia `json:"movie"`
		}
		if err := c.doGet(ctx, path, accessToken, &resp); err != nil {
			return nil, err
		}
		entries := make([]CollectionEntry, 0, len(resp))
		for _, row := range resp {
			entries = append(entries, row.Movie.entry("movie"))
		}
		return entries, nil
	}

	var resp []struct {
		Show traktMedia `json:"show"`
	}
	if err := c.doGet(ctx, path, accessToken, &resp); err != nil {
		return nil, err
	}
	entries := make([]CollectionEntry, 0, len(resp))
	for _, row := range resp {
		entries = append(entries, row.Show.entry("tv"))
	}
	return entries, nil
}

func (c *Client) getMediaList(ctx context.Context, path, mediaType, accessToken string) ([]CollectionEntry, error) {
	var resp []traktMedia
	if err := c.doGet(ctx, path, accessToken, &resp); err != nil {
		return nil, err
	}
	entries := make([]CollectionEntry, 0, len(resp))
	for _, row := range resp {
		entries = append(entries, row.entry(mediaType))
	}
	return entries, nil
}

func (c *Client) doGet(ctx context.Context, path string, accessToken string, dest any) error {
	if strings.TrimSpace(c.clientID) == "" {
		return errors.New("trakt: client id is required")
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}

	reqURL := c.baseURL + path
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return fmt.Errorf("trakt: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("trakt-api-version", traktAPIVersion)
		req.Header.Set("trakt-api-key", c.clientID)
		if strings.TrimSpace(accessToken) != "" {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("trakt: request failed: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt < maxRetries {
				if err := sleepContext(ctx, retryAfterOrDefault(resp, attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("trakt: rate limited after %d retries", maxRetries)
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt < maxRetries {
				if err := sleepContext(ctx, time.Duration(1<<attempt)*time.Second); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("trakt: server error %d after %d retries", resp.StatusCode, maxRetries)
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
			resp.Body.Close()
			return fmt.Errorf("trakt: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("trakt: decode response: %w", decodeErr)
		}
		return nil
	}
	return fmt.Errorf("trakt: max retries exceeded")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retryAfterOrDefault(resp *http.Response, attempt int) time.Duration {
	if val := resp.Header.Get("Retry-After"); val != "" {
		if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		if when, err := http.ParseTime(val); err == nil {
			if d := time.Until(when); d > 0 {
				return d
			}
		}
	}
	return time.Duration(1<<attempt) * time.Second
}

type traktMedia struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   traktIDs `json:"ids"`
}

type traktIDs struct {
	Trakt int    `json:"trakt"`
	TMDB  int    `json:"tmdb"`
	TVDB  int    `json:"tvdb"`
	IMDb  string `json:"imdb"`
}

func (m traktMedia) entry(mediaType string) CollectionEntry {
	return CollectionEntry{
		TraktID:   m.IDs.Trakt,
		TMDBID:    m.IDs.TMDB,
		TVDBID:    m.IDs.TVDB,
		IMDbID:    strings.TrimSpace(m.IDs.IMDb),
		MediaType: mediaType,
		Title:     m.Title,
		Year:      m.Year,
	}
}

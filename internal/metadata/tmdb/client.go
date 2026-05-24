package tmdb

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

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL             = "https://api.themoviedb.org/3"
	defaultAPIKey              = "4ef0d7355d9ffb5151e987764708ce96"
	maxRetries                 = 3
	maxResponseBody            = 1 << 20 // 1 MB
	maxCollectionPresetResults = 500
)

// Client is an HTTP client for the TMDB collection preset API surface.
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	limiter    *rate.Limiter
}

// NewClient creates a TMDB API client with the given API key and rate limit
// (requests per second). If apiKey is empty the built-in project key is used.
func NewClient(apiKey string, rateLimit int) *Client {
	if apiKey == "" {
		apiKey = defaultAPIKey
	}
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		limiter:    rate.NewLimiter(rate.Limit(rateLimit), rateLimit),
	}
}

// SetBaseURL overrides the API base URL. Used for testing.
func (c *Client) SetBaseURL(url string) {
	c.baseURL = url
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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return fmt.Errorf("tmdb: create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("tmdb: request failed: %w", err)
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
			var apiErr apiError
			if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.StatusMessage != "" {
				return fmt.Errorf("tmdb: HTTP %d: %s", resp.StatusCode, apiErr.StatusMessage)
			}
			return fmt.Errorf("tmdb: HTTP %d", resp.StatusCode)
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

// GetExternalIDs fetches external IDs for a TMDB movie or TV entry.
func (c *Client) GetExternalIDs(ctx context.Context, mediaType string, id int) (*ExternalIDs, error) {
	var path string
	switch mediaType {
	case "movie":
		path = fmt.Sprintf("/movie/%d?append_to_response=external_ids", id)
	case "tv":
		path = fmt.Sprintf("/tv/%d?append_to_response=external_ids", id)
	default:
		return nil, fmt.Errorf("tmdb: invalid media type: %q", mediaType)
	}

	var resp externalIDsResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	return resp.ExternalIDs, nil
}

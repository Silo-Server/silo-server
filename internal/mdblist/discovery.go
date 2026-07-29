// Package mdblist is a client for MDBList's public list discovery endpoints
// (/lists/search and /lists/top). These endpoints require an apikey, unlike
// fetching a list's items as JSON which does not.
package mdblist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const defaultBaseURL = "https://api.mdblist.com"

// ErrNotConfigured is returned when no MDBList apikey has been configured.
var ErrNotConfigured = errors.New("mdblist apikey is not configured")

// ListSummary mirrors the shape returned by /lists/search and /lists/top.
type ListSummary struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	UserName    string `json:"user_name"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	MediaType   string `json:"mediatype"`
	Items       int    `json:"items"`
	Likes       int    `json:"likes"`

	// URL is the canonical mdblist.com list page; append "/json" to get the
	// importable feed.
	URL string `json:"url,omitempty"`
}

// Client wraps a single MDBList apikey. Construct one per server config; it
// is safe for concurrent use because http.Client is. The apikey is atomic so
// admin settings changes apply to subsequent requests without restart.
type Client struct {
	apiKey  atomic.Pointer[string]
	baseURL string
	http    *http.Client
}

// NewClient returns a discovery client. apiKey may be empty — the Search/Top
// methods will then return ErrNotConfigured rather than calling out.
func NewClient(apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	c := &Client{
		baseURL: defaultBaseURL,
		http:    httpClient,
	}
	c.SetAPIKey(apiKey)
	return c
}

// SetAPIKey replaces the apikey used for subsequent requests. Safe for
// concurrent use.
func (c *Client) SetAPIKey(apiKey string) {
	trimmed := strings.TrimSpace(apiKey)
	c.apiKey.Store(&trimmed)
}

func (c *Client) currentAPIKey() string {
	if key := c.apiKey.Load(); key != nil {
		return *key
	}
	return ""
}

// Configured reports whether the client has an apikey set.
func (c *Client) Configured() bool {
	return c.currentAPIKey() != ""
}

// Search returns lists whose title matches query.
func (c *Client) Search(ctx context.Context, query string) ([]ListSummary, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	q := url.Values{}
	q.Set("apikey", c.currentAPIKey())
	q.Set("query", query)
	return c.fetchLists(ctx, "/lists/search", q)
}

// Top returns the public top lists ranked by Trakt likes.
func (c *Client) Top(ctx context.Context) ([]ListSummary, error) {
	q := url.Values{}
	q.Set("apikey", c.currentAPIKey())
	return c.fetchLists(ctx, "/lists/top", q)
}

// Check verifies that the configured API key can reach an authenticated
// discovery endpoint without exposing or persisting any returned list data.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.Top(ctx)
	return err
}

func (c *Client) fetchLists(ctx context.Context, path string, q url.Values) ([]ListSummary, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	u := c.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating mdblist request: %w", err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling mdblist: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("mdblist rejected apikey (status %d)", res.StatusCode)
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("mdblist rate limit exceeded")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mdblist request failed with status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading mdblist response: %w", err)
	}
	var lists []ListSummary
	if err := json.Unmarshal(body, &lists); err != nil {
		return nil, fmt.Errorf("parsing mdblist response: %w", err)
	}
	for i := range lists {
		lists[i].URL = canonicalListURL(lists[i].UserName, lists[i].Slug)
	}
	return lists, nil
}

// canonicalListURL returns the public mdblist.com page URL for a list. The
// JSON variant used by sync simply appends "/json".
func canonicalListURL(user, slug string) string {
	if user == "" || slug == "" {
		return ""
	}
	return fmt.Sprintf("https://mdblist.com/lists/%s/%s", user, slug)
}

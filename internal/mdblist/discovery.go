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
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
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
	// Redact the key this request sent: the setting can be replaced or
	// cleared while it's in flight, and the error quotes the key it sent.
	sentKey := q.Get("apikey")
	u := c.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating mdblist request: %w", sanitizeAPIKeyError(err, sentKey))
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling mdblist: %w", sanitizeAPIKeyError(err, sentKey))
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

// sanitizeAPIKeyError keeps the API key out of a request or transport
// error. logredact masks the apikey parameter in any *url.Error; the key can
// still appear elsewhere in the text (a transport or proxy quoting the whole
// request URL), so any remaining raw or query-escaped form is masked too.
func sanitizeAPIKeyError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	return maskAPIKeyError(logredact.SanitizeURLError(err), apiKey)
}

// maskAPIKeyError wraps err in a redactedError when its message holds the key.
func maskAPIKeyError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); redactAPIKey(msg, apiKey) != msg {
		masked := redactedError{message: redactAPIKey(msg, apiKey), cause: err, apiKey: apiKey}
		// A *url.Error asks its Err directly whether it timed out, so a masked
		// net.Error has to answer too.
		if ne, ok := err.(net.Error); ok { //nolint:errorlint // url.Error asserts its Err directly, so only err itself counts.
			return redactedNetError{redactedError: masked, net: ne}
		}
		return masked
	}
	return err
}

// redactedError carries a masked message. It has no Unwrap, so walking the
// chain with errors.Unwrap cannot reach the unmasked text; errors.Is and
// errors.As still see the cause, which keeps sentinel matching and net.Error
// timeout classification working at any depth. What As hands out is masked
// too: a *url.Error or net.Error found in the cause would otherwise quote
// the key in its own message.
type redactedError struct {
	message string
	cause   error
	apiKey  string
}

func (e redactedError) Error() string        { return e.message }
func (e redactedError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e redactedError) As(target any) bool {
	if !errors.As(e.cause, target) {
		return false
	}
	switch found := target.(type) {
	case **url.Error:
		if *found != nil {
			clone := **found
			clone.URL = redactAPIKey(clone.URL, e.apiKey)
			clone.Err = maskAPIKeyError(clone.Err, e.apiKey)
			*found = &clone
		}
	case *net.Error:
		if *found != nil && redactAPIKey((*found).Error(), e.apiKey) != (*found).Error() {
			*found = redactedNetError{
				redactedError: redactedError{message: redactAPIKey((*found).Error(), e.apiKey), cause: *found, apiKey: e.apiKey},
				net:           *found,
			}
		}
	}
	return true
}

// redactedNetError is a net.Error with a masked message that still reports
// the timeout it wraps.
type redactedNetError struct {
	redactedError
	net net.Error
}

func (e redactedNetError) Timeout() bool   { return e.net.Timeout() }
func (e redactedNetError) Temporary() bool { return e.net.Temporary() } //nolint:staticcheck // net.Error requires it.

// redactAPIKey masks every occurrence of the API key, raw or query-escaped.
func redactAPIKey(text, apiKey string) string {
	if apiKey == "" {
		return text
	}
	text = strings.ReplaceAll(text, apiKey, logredact.Placeholder)
	if escaped := url.QueryEscape(apiKey); escaped != apiKey {
		text = strings.ReplaceAll(text, escaped, logredact.Placeholder)
	}
	return text
}

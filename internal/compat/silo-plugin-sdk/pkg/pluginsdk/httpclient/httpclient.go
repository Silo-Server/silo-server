// Package httpclient is the shared outbound JSON-over-HTTP client for Silo
// plugins that talk to a credentialed third-party API (Sonarr/Radarr, Seerr,
// …). It carries an X-Api-Key header, caps the response body, and surfaces one
// typed error (*StatusError) for any non-2xx. A fully-empty 2xx body is treated
// as success (zero-value dest); callers detect "created but no body returned"
// by inspecting the decoded value (e.g. id == 0). The client is stateless;
// every call carries its own base URL + key.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20 // 1 MiB

// defaultHTTPClient is shared so its connection pool survives across calls
// (each New(nil) reuses one pooled Transport rather than minting a fresh one).
var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Client talks to one API instance.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// StatusError is any non-2xx response. Message is the parsed {"message":...}
// field when present, else the trimmed raw body; Body is always the trimmed raw
// body. Pointer receiver so errors.As(err, &se) works.
type StatusError struct {
	StatusCode int
	Body       string
	Message    string
}

func (e *StatusError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Body
	}
	if msg == "" {
		return fmt.Sprintf("httpclient: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("httpclient: HTTP %d: %s", e.StatusCode, msg)
}

// New builds a client. A nil hc gets a default 30s-timeout client. baseURL is
// right-trimmed of "/"; apiKey is space-trimmed.
func New(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = defaultHTTPClient
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: hc,
	}
}

// GetJSON issues a GET and decodes into dest (dest may be nil).
func (c *Client) GetJSON(ctx context.Context, path string, dest any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, dest)
}

// PostJSON issues a POST with a JSON body and decodes into dest.
func (c *Client) PostJSON(ctx context.Context, path string, body, dest any) error {
	return c.DoJSON(ctx, http.MethodPost, path, body, dest)
}

// DoJSON performs the request with the X-Api-Key header, mapping any non-2xx to
// *StatusError. A fully-empty 2xx body decodes to the zero value with nil error.
func (c *Client) DoJSON(ctx context.Context, method, path string, body, dest any) error {
	if c.baseURL == "" {
		return fmt.Errorf("httpclient: base url is required")
	}
	if c.apiKey == "" {
		return fmt.Errorf("httpclient: api key is required")
	}

	var reader io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("httpclient: encode request: %w", err)
		}
		reader = &buf
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("httpclient: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		trimmed := strings.TrimSpace(string(raw))
		return &StatusError{StatusCode: resp.StatusCode, Body: trimmed, Message: parseMessage(raw)}
	}
	if dest == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		return nil
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest)
	// Drain trailing bytes (bounded) so the connection can be pooled/reused on
	// every path, including when decode fails on a malformed body.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	if err != nil && err != io.EOF {
		return fmt.Errorf("httpclient: decode response: %w", err)
	}
	return nil
}

// parseMessage extracts the "message" field from an error body, falling back to
// the trimmed raw body.
func parseMessage(raw []byte) string {
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Message != "" {
		return envelope.Message
	}
	return strings.TrimSpace(string(raw))
}

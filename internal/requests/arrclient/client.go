package arrclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type HTTPError struct {
	StatusCode int
	Body       string
}

type DecodeError struct {
	StatusCode int
	Err        error
}

func (e HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("arr: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("arr: HTTP %d: %s", e.StatusCode, e.Body)
}

func (e DecodeError) Error() string {
	return fmt.Sprintf("arr: decode response: %v", e.Err)
}

func (e DecodeError) Unwrap() error {
	return e.Err
}

func IsEmptyOrTruncatedDecodeError(err error) bool {
	var decodeErr DecodeError
	if !errors.As(err, &decodeErr) {
		return false
	}
	return errors.Is(decodeErr.Err, io.EOF) || errors.Is(decodeErr.Err, io.ErrUnexpectedEOF)
}

func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: httpClient,
	}
}

func (c *Client) GetJSON(ctx context.Context, path string, dest any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, dest)
}

func (c *Client) PostJSON(ctx context.Context, path string, body, dest any) error {
	return c.DoJSON(ctx, http.MethodPost, path, body, dest)
}

func (c *Client) DoJSON(ctx context.Context, method, path string, body, dest any) error {
	if c.baseURL == "" {
		return fmt.Errorf("arr: base url is required")
	}
	if c.apiKey == "" {
		return fmt.Errorf("arr: api key is required")
	}

	var reader io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("arr: encode request: %w", err)
		}
		reader = &buf
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("arr: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("arr: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		return HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if dest == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest); err != nil {
		return DecodeError{StatusCode: resp.StatusCode, Err: err}
	}
	return nil
}

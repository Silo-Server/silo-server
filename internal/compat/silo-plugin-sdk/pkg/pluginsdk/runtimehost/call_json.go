package runtimehost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const DefaultMaxPluginJSONResponseBytes = 10 << 20

type CallPluginJSONRequest struct {
	InstallationID   int
	Method           string
	Path             string
	Headers          map[string]string
	Query            map[string]any
	Request          any
	Response         any
	MaxResponseBytes int
}

type HTTPStatusError struct {
	StatusCode int
	Body       []byte
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("runtimehost: plugin HTTP returned status %d", e.StatusCode)
}

func (c *Client) CallPluginJSON(ctx context.Context, req CallPluginJSONRequest) error {
	method := req.Method
	if method == "" {
		method = http.MethodGet
		if req.Request != nil {
			method = http.MethodPost
		}
	}

	headers := cloneHeaders(req.Headers)
	headers["Accept"] = "application/json"

	var body []byte
	if req.Request != nil {
		var err error
		body, err = json.Marshal(req.Request)
		if err != nil {
			return fmt.Errorf("runtimehost: encode json request: %w", err)
		}
		headers["Content-Type"] = "application/json"
	}

	resp, err := c.CallPluginHTTP(ctx, CallPluginHTTPRequest{
		InstallationID: req.InstallationID,
		Method:         method,
		Path:           req.Path,
		Headers:        headers,
		Body:           body,
		Query:          req.Query,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &HTTPStatusError{StatusCode: resp.StatusCode, Body: append([]byte(nil), resp.Body...)}
	}

	max := req.MaxResponseBytes
	if max <= 0 {
		max = DefaultMaxPluginJSONResponseBytes
	}
	if len(resp.Body) > max {
		return fmt.Errorf("runtimehost: json response exceeds %d bytes", max)
	}
	if req.Response == nil || len(resp.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Body, req.Response); err != nil {
		return fmt.Errorf("runtimehost: decode json response: %w", err)
	}
	return nil
}

func cloneHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

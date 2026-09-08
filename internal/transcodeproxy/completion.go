// Package transcodeproxy owns the private HTTP completion contract shared by
// Silo's integrated API proxy and its dedicated proxy node.
package transcodeproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// RequestHeader marks a segment hop whose immediate consumer is another
	// Silo server, not the playback client. The transcode node defers download
	// accounting until that server confirms downstream completion.
	RequestHeader = "X-Silo-Transcode-Proxy"
	// GenerationHeader carries an opaque session-incarnation and FFmpeg-timeline
	// token. It is private to Silo hops and must not be forwarded to the client.
	GenerationHeader = "X-Silo-Transcode-Segment-Generation"
)

var representationRequestHeaders = []string{
	"Range",
	"If-Range",
	"If-Match",
	"If-None-Match",
	"If-Modified-Since",
	"If-Unmodified-Since",
}

// PrepareRequest makes the upstream node serve the same byte representation
// requested by the downstream client and suppresses completion at this hop.
func PrepareRequest(upstream, downstream *http.Request) {
	upstream.Header.Set(RequestHeader, "1")
	for _, name := range representationRequestHeaders {
		for _, value := range downstream.Header.Values(name) {
			upstream.Header.Add(name, value)
		}
	}
}

// CopyResponseHeaders forwards public response metadata while retaining the
// generation token inside the trusted Silo hop.
func CopyResponseHeaders(dst, src http.Header) {
	for name, values := range src {
		if http.CanonicalHeaderKey(name) == GenerationHeader {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// FullRepresentationSize resolves the total entity size needed to distinguish
// a whole-file 206 response from an ordinary partial range.
func FullRepresentationSize(resp *http.Response) int64 {
	if resp == nil {
		return -1
	}
	if resp.StatusCode == http.StatusOK {
		return resp.ContentLength
	}
	if resp.StatusCode != http.StatusPartialContent {
		return -1
	}
	_, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/")
	if !ok || total == "*" {
		return -1
	}
	size, err := strconv.ParseInt(total, 10, 64)
	if err != nil || size <= 0 {
		return -1
	}
	return size
}

// Acknowledge reports a completed downstream response to the transcode node.
// The opaque generation token makes delayed acknowledgements harmless after a
// restart or reconstruction.
func Acknowledge(ctx context.Context, client *http.Client, targetURL, jwtSecret, generation string) error {
	if client == nil {
		client = http.DefaultClient
	}
	// The downstream request is commonly canceled as soon as its response body
	// reaches the client, while the handler is still issuing this acknowledgement.
	// Preserve request-scoped values, but give the post-response hop its own
	// bounded lifetime so a successful delivery is not mistaken for a disconnect.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ackCtx, http.MethodPost, targetURL+"/downloaded", nil)
	if err != nil {
		return fmt.Errorf("build acknowledgement: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtSecret)
	req.Header.Set(GenerationHeader, generation)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send acknowledgement: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("acknowledgement status %d", resp.StatusCode)
	}
	return nil
}

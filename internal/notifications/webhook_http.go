package notifications

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	webhookRequestTimeout = 10 * time.Second
	webhookMaxRedirects   = 3
	webhookUserAgent      = "Silo-Webhook/1.0"
)

// webhookSendResult is the structured outcome of one webhook POST.
type webhookSendResult struct {
	OK         bool
	HTTPStatus int           // 0 when no HTTP response was received
	RetryAfter time.Duration // from a 429 Retry-After header, when present
	Duration   time.Duration
	// Message is a short, non-sensitive diagnostic suitable for
	// failure_message ("404 Not Found", "dns lookup failed", ...). Never
	// includes URLs or payload contents.
	Message string
}

// newWebhookHTTPClient builds the webhook delivery client with its standard
// request timeout.
func newWebhookHTTPClient(allowPrivate func() bool) *http.Client {
	return newNotificationHTTPClient(allowPrivate, webhookRequestTimeout)
}

// newNotificationHTTPClient builds a delivery client with non-overridable TLS
// verification, bounded redirects, and a dialer Control hook that re-validates
// every resolved address at connect time (DNS rebinding mitigation — the guard
// runs on the address actually being connected to, each redirect hop included).
// Callers may use a longer total timeout when an upstream service has its own
// request deadline that must expire before Silo gives up waiting for a response.
func newNotificationHTTPClient(allowPrivate func() bool, requestTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			if allowPrivate != nil && allowPrivate() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("webhook dial: %w", err)
			}
			ip := net.ParseIP(host)
			if ip == nil || !webhookIPAllowed(ip) {
				return errPrivateDestination
			}
			return nil
		},
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
	}
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webhookMaxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != schemeHTTPS {
				return errors.New("redirect to non-https destination")
			}
			return nil
		},
	}
}

var errPrivateDestination = errors.New("destination resolves to a private or special-use network")

// sendWebhook POSTs body to url with the given extra headers and classifies
// the outcome. Success is any 2xx. The response body is drained (bounded) and
// discarded — Silo never consumes webhook responses for state.
func sendWebhook(ctx context.Context, client *http.Client, url string, body []byte, headers map[string]string) webhookSendResult {
	started := time.Now()
	result := func(ok bool, status int, message string) webhookSendResult {
		return webhookSendResult{
			OK:         ok,
			HTTPStatus: status,
			Duration:   time.Since(started),
			Message:    message,
		}
	}

	ctx, cancel := context.WithTimeout(ctx, webhookRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result(false, 0, "invalid webhook URL")
	}
	// HTTPS is enforced at registration; re-check at the last layer before
	// the wire so delivery never depends on upstream validation staying
	// perfect (webhook URLs are credentials and must not travel cleartext).
	if req.URL.Scheme != schemeHTTPS {
		return result(false, 0, "invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", webhookUserAgent)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return result(false, 0, classifyWebhookError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return result(true, resp.StatusCode, "")
	}
	out := result(false, resp.StatusCode, http.StatusText(resp.StatusCode))
	if out.Message == "" {
		out.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
	} else {
		out.Message = fmt.Sprintf("%d %s", resp.StatusCode, out.Message)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		out.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return out
}

// maxRetryAfter caps how far a destination's Retry-After header can push the
// next attempt; the longest scheduled backoff is 24h and a buggy or hostile
// header must not park deliveries beyond it.
const maxRetryAfter = 24 * time.Hour

// parseRetryAfter interprets a Retry-After header in both RFC 9110 forms —
// delta-seconds and HTTP-date — returning 0 when absent or unusable.
func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	delay := time.Duration(0)
	if seconds, err := strconv.Atoi(header); err == nil {
		delay = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(header); err == nil {
		delay = when.Sub(now)
	}
	if delay <= 0 {
		return 0
	}
	return min(delay, maxRetryAfter)
}

// classifyWebhookError maps transport errors to short diagnostic classes.
// Messages must stay free of URLs and payload contents.
func classifyWebhookError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns lookup failed"
	}
	if errors.Is(err, errPrivateDestination) {
		return "destination resolves to a private network"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "tls"):
		return "tls handshake failed"
	case strings.Contains(message, "connection refused"):
		return "connection refused"
	case strings.Contains(message, "too many redirects"):
		return "too many redirects"
	case strings.Contains(message, "redirect to non-https"):
		return "redirect to non-https destination"
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline"):
		return "request timed out"
	default:
		return "connection failed"
	}
}

package webhooksync

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

const (
	WebhookBodyCaptureLimit = 16 * 1024
	webhookBodyExcerptLimit = 2048
)

var sensitiveWebhookKeyFragments = []string{
	"token",
	"access_token",
	"authorization",
	"x-emby-token",
	"api_key",
	"apikey",
	"secret",
}

var rawSensitiveValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)("?(?:token|access_token|authorization|x-emby-token|api_key|apikey|secret)"?\s*:\s*)("(?:\\.|[^"])*"|[^,\}\]\s]+)`),
	regexp.MustCompile(`(?i)\b(?:token|access_token|authorization|x-emby-token|api_key|apikey|secret)=([^&\s]+)`),
	regexp.MustCompile(`(?i)\b(?:authorization|x-emby-token)\s*:\s*[^\r\n]+`),
}

type bodyCaptureReadCloser struct {
	body   io.ReadCloser
	buf    bytes.Buffer
	limit  int
	closed bool
}

func NewBodyCaptureReadCloser(body io.ReadCloser, limit int) *bodyCaptureReadCloser {
	if limit <= 0 {
		limit = WebhookBodyCaptureLimit
	}
	return &bodyCaptureReadCloser{body: body, limit: limit}
}

func (c *bodyCaptureReadCloser) Read(p []byte) (int, error) {
	n, err := c.body.Read(p)
	if n > 0 && c.buf.Len() < c.limit {
		remaining := c.limit - c.buf.Len()
		if remaining > n {
			remaining = n
		}
		_, _ = c.buf.Write(p[:remaining])
	}
	return n, err
}

func (c *bodyCaptureReadCloser) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.body.Close()
}

func (c *bodyCaptureReadCloser) Captured() []byte {
	return slices.Clone(c.buf.Bytes())
}

func SanitizeWebhookBodyExcerpt(contentType string, body []byte) string {
	if len(body) == 0 {
		return ""
	}

	mediaType, params, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "", "application/json", "text/json":
		return truncateWebhookExcerpt(sanitizeJSONString(body))
	case "application/x-www-form-urlencoded":
		return truncateWebhookExcerpt(sanitizeFormBody(body))
	case "multipart/form-data":
		return truncateWebhookExcerpt(sanitizeMultipartBody(body, params["boundary"]))
	default:
		return truncateWebhookExcerpt(redactRawWebhookText(string(body)))
	}
}

func sanitizeJSONString(body []byte) string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return redactRawWebhookText(string(body))
	}
	sanitized := sanitizeWebhookValue("", payload)
	encoded, err := json.MarshalIndent(sanitized, "", "  ")
	if err != nil {
		return redactRawWebhookText(string(body))
	}
	return string(encoded)
}

func sanitizeFormBody(body []byte) string {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return redactRawWebhookText(string(body))
	}
	for _, key := range []string{"payload", "data", "json", "body"} {
		if raw := strings.TrimSpace(values.Get(key)); raw != "" {
			return sanitizeEmbeddedWebhookPayload(raw)
		}
	}

	redacted := url.Values{}
	for key, vals := range values {
		for _, value := range vals {
			if isSensitiveWebhookKey(key) {
				redacted.Add(key, "[redacted]")
				continue
			}
			redacted.Add(key, redactRawWebhookText(value))
		}
	}
	return redacted.Encode()
}

func sanitizeMultipartBody(body []byte, boundary string) string {
	if strings.TrimSpace(boundary) == "" {
		return redactRawWebhookText(string(body))
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	fields := url.Values{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return redactRawWebhookText(string(body))
		}
		partBody, err := io.ReadAll(io.LimitReader(part, WebhookBodyCaptureLimit))
		if err != nil {
			return redactRawWebhookText(string(body))
		}
		name := part.FormName()
		if name == "" {
			continue
		}
		if raw := strings.TrimSpace(string(partBody)); raw != "" {
			switch strings.ToLower(name) {
			case "payload", "data", "json", "body":
				return sanitizeEmbeddedWebhookPayload(raw)
			default:
				fields.Add(name, raw)
			}
		}
	}
	if len(fields) == 0 {
		return redactRawWebhookText(string(body))
	}
	redacted := url.Values{}
	for key, vals := range fields {
		for _, value := range vals {
			if isSensitiveWebhookKey(key) {
				redacted.Add(key, "[redacted]")
				continue
			}
			redacted.Add(key, redactRawWebhookText(value))
		}
	}
	return redacted.Encode()
}

func sanitizeEmbeddedWebhookPayload(raw string) string {
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return sanitizeJSONString([]byte(trimmed))
		}
		if values, err := url.ParseQuery(trimmed); err == nil && len(values) > 0 {
			return sanitizeFormBody([]byte(trimmed))
		}
	}
	return redactRawWebhookText(raw)
}

func sanitizeWebhookValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for nestedKey, nestedValue := range typed {
			if isSensitiveWebhookKey(nestedKey) {
				out[nestedKey] = "[redacted]"
				continue
			}
			out[nestedKey] = sanitizeWebhookValue(nestedKey, nestedValue)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeWebhookValue(key, item))
		}
		return out
	case string:
		if isSensitiveWebhookKey(key) {
			return "[redacted]"
		}
		return redactRawWebhookText(typed)
	default:
		return typed
	}
}

func redactRawWebhookText(raw string) string {
	normalized := strings.ToValidUTF8(raw, "")
	for _, pattern := range rawSensitiveValuePatterns {
		normalized = pattern.ReplaceAllStringFunc(normalized, redactRawWebhookMatch)
	}
	return normalized
}

func redactRawWebhookMatch(match string) string {
	if idx := strings.Index(match, ":"); idx >= 0 {
		return match[:idx+1] + " [redacted]"
	}
	if idx := strings.Index(match, "="); idx >= 0 {
		return match[:idx+1] + "[redacted]"
	}
	return "[redacted]"
}

func truncateWebhookExcerpt(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	runes := []rune(raw)
	if len(runes) <= webhookBodyExcerptLimit {
		return raw
	}
	return string(runes[:webhookBodyExcerptLimit]) + "..."
}

func isSensitiveWebhookKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, fragment := range sensitiveWebhookKeyFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func BuildWebhookRequestLogContext(r *http.Request, bodyExcerpt string) WebhookRequestLogContext {
	return WebhookRequestLogContext{
		RequestID:   getRequestID(r),
		ClientIP:    getClientIP(r),
		ContentType: strings.TrimSpace(r.Header.Get("Content-Type")),
		UserAgent:   strings.TrimSpace(r.UserAgent()),
		PathPattern: getWebhookPathPattern(r),
		BodyExcerpt: bodyExcerpt,
	}
}

func getRequestID(r *http.Request) string {
	return strings.TrimSpace(chimw.GetReqID(r.Context()))
}

func getClientIP(r *http.Request) string {
	return strings.TrimSpace(clientip.FromContext(r.Context()))
}

func getWebhookPathPattern(r *http.Request) string {
	if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
		if pattern := strings.TrimSpace(routeCtx.RoutePattern()); pattern != "" {
			return pattern
		}
	}
	return "/api/v2/webhook-sync/webhooks/{secret}"
}

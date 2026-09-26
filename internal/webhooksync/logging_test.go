package webhooksync

import (
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeWebhookBodyExcerptJSONRedactsSensitiveFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{"token":"abc","Authorization":"Bearer 123","nested":{"api_key":"secret"},"safe":"ok"}`)
	got := SanitizeWebhookBodyExcerpt("application/json", body)

	for _, forbidden := range []string{"abc", "Bearer 123", "secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected %q to be redacted from %q", forbidden, got)
		}
	}
	if !strings.Contains(got, `"safe": "ok"`) {
		t.Fatalf("expected safe field to remain visible: %q", got)
	}
}

func TestSanitizeWebhookBodyExcerptFormPrefersSemanticPayload(t *testing.T) {
	t.Parallel()

	body := "noise=ignored&payload=" + url.QueryEscape(`{"token":"abc","safe":"ok"}`)
	got := SanitizeWebhookBodyExcerpt("application/x-www-form-urlencoded", []byte(body))

	if strings.Contains(got, "noise=ignored") {
		t.Fatalf("expected semantic payload field to be preferred: %q", got)
	}
	if strings.Contains(got, "abc") {
		t.Fatalf("expected token to be redacted: %q", got)
	}
	if !strings.Contains(got, `"safe": "ok"`) {
		t.Fatalf("expected payload json to be preserved: %q", got)
	}
}

func TestSanitizeWebhookBodyExcerptMultipartPrefersSemanticPayload(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	writer := multipart.NewWriter(&builder)
	payload, err := writer.CreateFormField("payload")
	if err != nil {
		t.Fatalf("CreateFormField(payload) error = %v", err)
	}
	if _, err := payload.Write([]byte(`{"access_token":"abc","safe":"ok"}`)); err != nil {
		t.Fatalf("payload.Write() error = %v", err)
	}
	other, err := writer.CreateFormField("extra")
	if err != nil {
		t.Fatalf("CreateFormField(extra) error = %v", err)
	}
	if _, err := other.Write([]byte("ignored")); err != nil {
		t.Fatalf("other.Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	got := SanitizeWebhookBodyExcerpt(writer.FormDataContentType(), []byte(builder.String()))
	if strings.Contains(got, "ignored") {
		t.Fatalf("expected multipart payload field to be preferred: %q", got)
	}
	if strings.Contains(got, "abc") {
		t.Fatalf("expected access token to be redacted: %q", got)
	}
	if !strings.Contains(got, `"safe": "ok"`) {
		t.Fatalf("expected payload json to be preserved: %q", got)
	}
}

func TestBuildWebhookRequestLogContextUsesRedactedPathPattern(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/v1/webhook-sync/webhooks/raw-secret", strings.NewReader(""))
	ctx := BuildWebhookRequestLogContext(req, "")
	if strings.Contains(ctx.PathPattern, "raw-secret") {
		t.Fatalf("expected secret to be redacted from path pattern: %q", ctx.PathPattern)
	}
	if ctx.PathPattern != "/api/v2/webhook-sync/webhooks/{secret}" {
		t.Fatalf("unexpected fallback path pattern: %q", ctx.PathPattern)
	}
}

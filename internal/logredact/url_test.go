package logredact

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeURLRemovesSecretBearingComponents(t *testing.T) {
	const raw = "https://operator:node-password@node.example:9443/transcode?access_token=query-secret#fragment-secret"
	if got, want := SanitizeURL(raw), "https://node.example:9443/transcode"; got != want {
		t.Fatalf("SanitizeURL() = %q, want %q", got, want)
	}
}

func TestSanitizeURLErrorRemovesRequestedURLSecretsAndPreservesCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := &url.Error{
		Op:  "Get",
		URL: "https://operator:node-password@node.example:9443/hw-capabilities?access_token=query-secret#fragment-secret",
		Err: &url.Error{
			Op:  "dial",
			URL: "tcp://operator:node-password@node.example:9443?access_token=nested-secret#nested-fragment",
			Err: cause,
		},
	}

	sanitized := SanitizeURLError(err)
	message := sanitized.Error()
	for _, secret := range []string{"operator", "node-password", "query-secret", "fragment-secret", "nested-secret", "nested-fragment"} {
		if strings.Contains(message, secret) {
			t.Fatalf("sanitized error contains %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "https://node.example:9443/hw-capabilities") ||
		!strings.Contains(message, "connection refused") {
		t.Fatalf("sanitized error lost useful diagnostics: %q", message)
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized error does not preserve its cause: %v", sanitized)
	}
}

func TestSanitizeURLErrorFindsWrappedURLError(t *testing.T) {
	cause := errors.New("connection refused")
	err := errors.Join(errors.New("capability request failed"), &url.Error{
		Op:  "Get",
		URL: "https://operator:node-password@node.example/hw-capabilities?access_token=query-secret",
		Err: cause,
	})

	sanitized := SanitizeURLError(err)
	if message := sanitized.Error(); strings.Contains(message, "node-password") || strings.Contains(message, "query-secret") {
		t.Fatalf("sanitized wrapped error leaked credentials: %q", message)
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized wrapped error does not preserve its cause: %v", sanitized)
	}
	if !strings.Contains(sanitized.Error(), "capability request failed") {
		t.Fatalf("sanitized joined error lost its outer context: %q", sanitized.Error())
	}
}

func TestSanitizeURLErrorPreservesSingleWrapperContext(t *testing.T) {
	cause := errors.New("connection refused")
	err := fmt.Errorf("fetch node capabilities: %w", &url.Error{
		Op:  "Get",
		URL: "https://operator:node-password@node.example/hw-capabilities?access_token=query-secret",
		Err: cause,
	})

	sanitized := SanitizeURLError(err)
	message := sanitized.Error()
	for _, secret := range []string{"operator", "node-password", "query-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("sanitized wrapped error contains %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "fetch node capabilities") ||
		!strings.Contains(message, "https://node.example/hw-capabilities") ||
		!strings.Contains(message, "connection refused") {
		t.Fatalf("sanitized wrapped error lost outer diagnostics: %q", message)
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized wrapped error does not preserve its cause: %v", sanitized)
	}
}

func TestSanitizeURLErrorPreservesMultiWrapperChain(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	err := fmt.Errorf("transcode start failed: %w",
		fmt.Errorf("request node: %w", &url.Error{
			Op:  "Post",
			URL: "http://node-operator:node-secret@node.example:8080/transcode/start",
			Err: cause,
		}))

	sanitized := SanitizeURLError(err)
	message := sanitized.Error()
	for _, secret := range []string{"node-operator", "node-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("sanitized chained error contains %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "transcode start failed") || !strings.Contains(message, "request node") {
		t.Fatalf("sanitized chained error lost outer messages: %q", message)
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized chained error does not preserve its cause: %v", sanitized)
	}
}

func TestSanitizeURLErrorReplacesRawTextInPrefixWrapper(t *testing.T) {
	cause := errors.New("connection refused")
	err := fmt.Errorf("%w: extra", &url.Error{
		Op:  "Get",
		URL: "https://operator:node-password@node.example/hw-capabilities?access_token=query-secret",
		Err: cause,
	})

	sanitized := SanitizeURLError(err)
	message := sanitized.Error()
	for _, secret := range []string{"operator", "node-password", "query-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("sanitized prefix-wrapped error contains %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "https://node.example/hw-capabilities") ||
		!strings.Contains(message, "extra") ||
		!strings.Contains(message, "connection refused") {
		t.Fatalf("sanitized prefix-wrapped error lost diagnostics: %q", message)
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized prefix-wrapped error does not preserve its cause: %v", sanitized)
	}
}

func TestSanitizeURLErrorPreservesMultiPercentWFmtMessage(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	err := fmt.Errorf("capability request failed (%w; %w)",
		&url.Error{
			Op:  "Get",
			URL: "https://operator:node-password@node.example/hw-capabilities?access_token=query-secret",
			Err: cause,
		},
		errors.New("cache miss"))

	sanitized := SanitizeURLError(err)
	message := sanitized.Error()
	for _, secret := range []string{"operator", "node-password", "query-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("sanitized multi-wrapped error contains %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "capability request failed (") || !strings.Contains(message, "; cache miss)") {
		t.Fatalf("multi-%%w fmt.Errorf message format was lost: %q", message)
	}
	if !errors.Is(sanitized, cause) {
		t.Fatalf("sanitized multi-wrapped error does not preserve its cause: %v", sanitized)
	}
}

func TestSanitizeURLFailsClosedForMalformedInput(t *testing.T) {
	const secret = "node-password"
	got := SanitizeURL("https://operator:" + secret + "@node.example/\x00")
	if strings.Contains(got, secret) {
		t.Fatalf("malformed URL leaked credentials: %q", got)
	}
}

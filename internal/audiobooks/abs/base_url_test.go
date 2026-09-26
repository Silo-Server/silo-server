package abs

import (
	"net/http"
	"testing"
)

func TestABSBaseURLUsesVersionedPluginContentMount(t *testing.T) {
	h := &Handler{deps: Dependencies{InstallID: func() string { return "42" }}}

	proxied := &http.Request{Header: http.Header{
		"X-Forwarded-Proto": []string{"https"},
		"X-Forwarded-Host":  []string{"silo.example.test"},
		"X-Silo-User-Id":    []string{"user-1"},
	}}
	if got, want := h.absBaseURL(proxied), "https://silo.example.test/api/v2/plugin-content/plugins/42"; got != want {
		t.Fatalf("proxied ABS base URL = %q, want %q", got, want)
	}

	standalone := &http.Request{Host: "silo.example.test"}
	if got, want := h.absBaseURL(standalone), "http://silo.example.test"; got != want {
		t.Fatalf("standalone ABS base URL = %q, want %q", got, want)
	}
}

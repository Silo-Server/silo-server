package plugins

import (
	"context"
	"testing"
)

func TestValidateProviderStreamURLRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/stream",
		"http://10.0.0.7/stream",
		"http://169.254.169.254/latest/meta-data",
		"http://localhost/stream",
	} {
		if _, err := validateProviderStreamURL(context.Background(), raw); err == nil {
			t.Fatalf("validateProviderStreamURL(%q) succeeded, want rejection", raw)
		}
	}
}

func TestValidateProviderStreamURLAcceptsPublicIP(t *testing.T) {
	got, err := validateProviderStreamURL(context.Background(), "https://1.1.1.1/stream?token=x")
	if err != nil {
		t.Fatalf("validateProviderStreamURL returned error: %v", err)
	}
	if got != "https://1.1.1.1/stream?token=x" {
		t.Fatalf("validated URL = %q", got)
	}
}

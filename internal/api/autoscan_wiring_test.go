package api

import (
	"testing"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// TestCheckRequestIntegrationUsable verifies the Fix 4 gate: a reused Requests
// connection is rejected when the integration is disabled or has no base_url, so
// the autoscan engine skips it instead of polling an unusable target.
func TestCheckRequestIntegrationUsable(t *testing.T) {
	t.Run("enabled with base_url is usable", func(t *testing.T) {
		if err := checkRequestIntegrationUsable("req-1", true, "http://radarr:7878"); err != nil {
			t.Fatalf("expected usable, got %v", err)
		}
	})
	t.Run("disabled is rejected", func(t *testing.T) {
		if err := checkRequestIntegrationUsable("req-1", false, "http://radarr:7878"); err == nil {
			t.Fatal("expected error for disabled integration")
		}
	})
	t.Run("blank base_url is rejected", func(t *testing.T) {
		if err := checkRequestIntegrationUsable("req-1", true, "   "); err == nil {
			t.Fatal("expected error for blank base_url")
		}
	})
}

// TestCheckRequestIntegrationUsableGatesReuse is the Task 17 regression test. It
// locks the autoscan→requests credential-reuse behavior after the
// request_integrations table was generalized into the two-tier connection
// registry (Task 9/10): autoscan must keep reusing the Sonarr/Radarr connection
// rows via the same enabled + base_url gate, regardless of the added
// capability/installation columns.
func TestCheckRequestIntegrationUsableGatesReuse(t *testing.T) {
	// Enabled + non-blank base_url -> usable (autoscan can reuse the connection).
	if err := checkRequestIntegrationUsable("int-1", true, "http://sonarr:8989"); err != nil {
		t.Fatalf("enabled integration with base_url should be usable, got: %v", err)
	}
	// Disabled -> error (autoscan must not poll a disabled connection).
	if err := checkRequestIntegrationUsable("int-1", false, "http://sonarr:8989"); err == nil {
		t.Fatal("disabled integration must be rejected")
	}
	// Blank base_url -> error.
	if err := checkRequestIntegrationUsable("int-1", true, "   "); err == nil {
		t.Fatal("blank base_url must be rejected")
	}
}

// Compile-time guard: autoscan's RequestIntegrationLookup reads these fields off
// mediarequests.Integration. If any is renamed/removed, autoscan reuse breaks —
// fail the build here rather than silently at runtime.
var _ = func(in mediarequests.Integration) (string, string, bool) {
	return in.BaseURL, in.APIKeyRef, in.Enabled
}

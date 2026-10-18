package api

import "testing"

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

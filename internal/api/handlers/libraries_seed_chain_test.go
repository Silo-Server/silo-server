package handlers

import (
	"testing"
)

// A specialist provider that opts out of default-enable (default_enabled=false)
// must be seeded installed-but-off and ranked by its declared priority — never
// jumping ahead of the general providers just because a user might enable it.
// A provider that does not declare the content level at all must be excluded
// entirely, not shown as a disabled row. Together these are the regressions that
// let silo.sportarr land at position 1 enabled, and let the audiobook/ebook/manga
// providers clutter every TV series and movie library's provider chain.
func TestBuildSeededChainEntries_OptOutAndLevelScoping(t *testing.T) {
	candidates := []seedCandidate{
		{installationID: 1, capabilityID: "tmdb", supportsLevel: true, declaredPriority: 3, defaultEnabled: true},
		{installationID: 2, capabilityID: "tvdb", supportsLevel: true, declaredPriority: 2, defaultEnabled: true},
		{installationID: 22, capabilityID: "sportarr", supportsLevel: true, declaredPriority: 50, defaultEnabled: false},
		// Declares only {audiobook} — does not support the series level, so it
		// must not appear in a series library's chain at all.
		{installationID: 3, capabilityID: "audiobook-metadata", supportsLevel: false, declaredPriority: 0, defaultEnabled: true},
		{installationID: 8, capabilityID: "ebook-metadata", supportsLevel: false, declaredPriority: 0, defaultEnabled: true},
	}

	entries := buildSeededChainEntries("series", candidates)

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (unsupported providers excluded), got %d", len(entries))
	}

	// Ordering: general providers by declared priority (tvdb 2, tmdb 3), then
	// the opted-out specialist by its declared priority (50).
	type want struct {
		installationID int
		enabled        bool
	}
	wants := []want{
		{2, true},   // tvdb
		{1, true},   // tmdb
		{22, false}, // sportarr — declared but opted out
	}
	for i, w := range wants {
		e := entries[i]
		if e.PluginInstallationID != w.installationID {
			t.Errorf("position %d: got installation %d, want %d", i, e.PluginInstallationID, w.installationID)
		}
		if e.Enabled != w.enabled {
			t.Errorf("position %d (installation %d): enabled=%v, want %v", i, e.PluginInstallationID, e.Enabled, w.enabled)
		}
		if e.Priority != i {
			t.Errorf("position %d: got priority %d, want %d", i, e.Priority, i)
		}
		if e.ContentLevel != "series" {
			t.Errorf("position %d: got content level %q, want series", i, e.ContentLevel)
		}
		if e.CapabilityType != "metadata_provider.v1" {
			t.Errorf("position %d: got capability type %q", i, e.CapabilityType)
		}
	}

	// The audiobook/ebook providers must be entirely absent.
	for _, e := range entries {
		if e.PluginInstallationID == 3 || e.PluginInstallationID == 8 {
			t.Errorf("provider %d does not declare series and must not be seeded", e.PluginInstallationID)
		}
	}
}

// A provider with default_enabled=true (the default) that declares the level is
// seeded enabled, unchanged from the pre-flag behavior.
func TestBuildSeededChainEntries_DefaultsEnabled(t *testing.T) {
	entries := buildSeededChainEntries("movie", []seedCandidate{
		{installationID: 1, capabilityID: "tmdb", supportsLevel: true, declaredPriority: 2, defaultEnabled: true},
	})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !entries[0].Enabled {
		t.Errorf("provider with default_enabled=true should be seeded enabled")
	}
}

// A legacy provider that declares no default_priority map at all makes no claim
// and stays eligible for every level (parked last, disabled) — preserving the
// pre-existing catch-all behavior for providers that never enumerated levels.
func TestBuildSeededChainEntries_LegacyCatchAllParkedLast(t *testing.T) {
	entries := buildSeededChainEntries("series", []seedCandidate{
		{installationID: 1, capabilityID: "tmdb", supportsLevel: true, declaredPriority: 3, defaultEnabled: true},
		{installationID: 9, capabilityID: "legacy", supportsLevel: true, declaredPriority: 0, defaultEnabled: true},
	})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].PluginInstallationID != 1 || !entries[0].Enabled {
		t.Errorf("tmdb should be first and enabled, got %+v", entries[0])
	}
	if entries[1].PluginInstallationID != 9 || entries[1].Enabled {
		t.Errorf("legacy catch-all should be parked last and disabled, got %+v", entries[1])
	}
}

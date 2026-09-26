package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type stubWatchProviderLister struct {
	summaries []watchsync.ProviderSummary
}

func (s stubWatchProviderLister) List() []watchsync.ProviderSummary { return s.summaries }

func providerStatsByKey(t *testing.T, stats []WatchProviderStats, key string) WatchProviderStats {
	t.Helper()
	for _, entry := range stats {
		if entry.Provider == key {
			return entry
		}
	}
	t.Fatalf("provider %q missing from %+v", key, stats)
	return WatchProviderStats{}
}

func TestMergeWatchProviderStatsIncludesRegisteredProvidersWithoutActivity(t *testing.T) {
	merged := mergeWatchProviderStats([]watchsync.ProviderSummary{
		{
			Key:          "trakt",
			DisplayName:  "Trakt",
			Capabilities: watchsync.Capabilities{ExportWatched: true, ScrobblePlayback: true},
		},
		{Key: "mdblist", DisplayName: "MDBList"},
	}, nil)

	if len(merged) != 2 {
		t.Fatalf("expected 2 providers, got %d: %+v", len(merged), merged)
	}

	mdblist := providerStatsByKey(t, merged, "mdblist")
	if !mdblist.Registered {
		t.Fatalf("registered provider should be marked registered: %+v", mdblist)
	}
	if mdblist.DisplayName != "MDBList" {
		t.Fatalf("display name = %q, want MDBList", mdblist.DisplayName)
	}
	if mdblist.ConnectedProfiles != 0 || mdblist.SyncRuns24h != 0 || mdblist.LastSyncCompletedAt != nil {
		t.Fatalf("provider without activity should be all zeros: %+v", mdblist)
	}
	if mdblist.Scrobbling || mdblist.Exporting {
		t.Fatalf("capabilities should follow the registry summary: %+v", mdblist)
	}

	trakt := providerStatsByKey(t, merged, "trakt")
	if !trakt.Scrobbling || !trakt.Exporting {
		t.Fatalf("trakt capabilities not carried through: %+v", trakt)
	}
}

func TestMergeWatchProviderStatsOverlaysActivityAndOrdersByKey(t *testing.T) {
	synced := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	merged := mergeWatchProviderStats([]watchsync.ProviderSummary{
		{Key: "trakt", DisplayName: "Trakt"},
		{Key: "simkl", DisplayName: "Simkl"},
	}, []WatchProviderStats{
		{
			Provider:            "trakt",
			ConnectedProfiles:   2,
			SyncRuns24h:         5,
			SyncErrors24h:       1,
			ImportedWatched24h:  30,
			ExportedWatched24h:  4,
			PendingExports:      7,
			LastSyncCompletedAt: &synced,
		},
	})

	got := make([]string, 0, len(merged))
	for _, entry := range merged {
		got = append(got, entry.Provider)
	}
	// Deterministic order: sorted by provider key, not by map iteration.
	if len(got) != 2 || got[0] != "simkl" || got[1] != "trakt" {
		t.Fatalf("provider order = %v, want [simkl trakt]", got)
	}

	trakt := providerStatsByKey(t, merged, "trakt")
	if trakt.ConnectedProfiles != 2 || trakt.SyncRuns24h != 5 || trakt.PendingExports != 7 {
		t.Fatalf("activity not overlaid onto the registered provider: %+v", trakt)
	}
	if trakt.LastSyncCompletedAt == nil || !trakt.LastSyncCompletedAt.Equal(synced) {
		t.Fatalf("last sync time not carried through: %+v", trakt)
	}
	if trakt.DisplayName != "Trakt" {
		t.Fatalf("registry display name should win over the key: %q", trakt.DisplayName)
	}
}

func TestMergeWatchProviderStatsKeepsUnregisteredProviderWithRows(t *testing.T) {
	// A provider whose plugin was uninstalled still has rows in the
	// watch-provider tables; its history stays visible, flagged as unregistered
	// and named by its key.
	merged := mergeWatchProviderStats([]watchsync.ProviderSummary{
		{Key: "trakt", DisplayName: "Trakt"},
	}, []WatchProviderStats{
		{Provider: "letterboxd", ConnectedProfiles: 1, SyncRuns24h: 3},
	})

	legacy := providerStatsByKey(t, merged, "letterboxd")
	if legacy.Registered {
		t.Fatalf("unregistered provider should not be marked registered: %+v", legacy)
	}
	if legacy.DisplayName != "letterboxd" {
		t.Fatalf("display name = %q, want the provider key", legacy.DisplayName)
	}
	if legacy.SyncRuns24h != 3 {
		t.Fatalf("activity dropped for unregistered provider: %+v", legacy)
	}
}

func TestMergeWatchProviderStatsWithoutRegistrySerializesAsArray(t *testing.T) {
	merged := mergeWatchProviderStats(listWatchProviders(nil), nil)
	if merged == nil {
		t.Fatal("expected an empty slice, not nil")
	}

	encoded, err := json.Marshal(AdminStats{WatchProviders: merged})
	if err != nil {
		t.Fatalf("marshal admin stats: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal admin stats: %v", err)
	}
	if string(decoded["watch_providers"]) != "[]" {
		t.Fatalf("watch_providers = %s, want []", decoded["watch_providers"])
	}
}

func TestListWatchProvidersToleratesNilRegistry(t *testing.T) {
	var registry *watchsync.Registry
	if got := listWatchProviders(registry); got != nil {
		t.Fatalf("nil registry should list no providers, got %+v", got)
	}
	if got := listWatchProviders(stubWatchProviderLister{}); got != nil {
		t.Fatalf("empty lister should list no providers, got %+v", got)
	}
}

package plugins

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestCatalogEntriesForDiscoveryUsesApprovedCommunityOwnerForTransferredPlugins(t *testing.T) {
	entries := []CatalogEntry{
		{
			RepositoryID: 1,
			SourceKind:   RepositorySourceSilo,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.requests.arr", Version: "0.1.0"},
		},
		{
			RepositoryID: 2,
			SourceKind:   RepositorySourceApprovedCommunity,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.requests.arr", Version: "0.1.1"},
		},
	}

	result := catalogEntriesForDiscovery(entries)
	if len(result) != 1 {
		t.Fatalf("catalog entries = %d, want 1", len(result))
	}
	if result[0].RepositoryID != 2 || result[0].Manifest.GetVersion() != "0.1.1" {
		t.Fatalf("selected repository/version = %d/%s, want 2/0.1.1", result[0].RepositoryID, result[0].Manifest.GetVersion())
	}
}

func TestCatalogEntriesForDiscoveryHidesTransferredPluginWhenCommunityIsDisabled(t *testing.T) {
	result := catalogEntriesForDiscovery([]CatalogEntry{{
		RepositoryID: 1,
		SourceKind:   RepositorySourceSilo,
		Manifest:     &pluginv1.PluginManifest{PluginId: "silo.requests.seerr", Version: "0.1.0"},
	}})
	if len(result) != 0 {
		t.Fatalf("catalog entries = %d, want transferred plugin hidden", len(result))
	}
}

func TestCatalogEntriesForDiscoveryPreventsExternalSourceShadowing(t *testing.T) {
	result := catalogEntriesForDiscovery([]CatalogEntry{
		{
			RepositoryID: 9,
			SourceKind:   RepositorySourceExternal,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "99.0.0"},
		},
		{
			RepositoryID: 1,
			SourceKind:   RepositorySourceSilo,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "1.0.0"},
		},
	})
	if len(result) != 1 {
		t.Fatalf("catalog entries = %d, want 1", len(result))
	}
	if result[0].RepositoryID != 1 {
		t.Fatalf("selected repository = %d, want official repository 1", result[0].RepositoryID)
	}
}

func TestCatalogEntriesForDiscoveryShowsLatestVersionFromSelectedRepository(t *testing.T) {
	result := catalogEntriesForDiscovery([]CatalogEntry{
		{
			RepositoryID: 1,
			SourceKind:   RepositorySourceSilo,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "1.2.9"},
		},
		{
			RepositoryID: 1,
			SourceKind:   RepositorySourceSilo,
			Manifest:     &pluginv1.PluginManifest{PluginId: "silo.tmdb", Version: "1.2.10"},
		},
	})
	if len(result) != 1 {
		t.Fatalf("catalog entries = %d, want 1", len(result))
	}
	if result[0].Manifest.GetVersion() != "1.2.10" {
		t.Fatalf("selected version = %s, want 1.2.10", result[0].Manifest.GetVersion())
	}
}

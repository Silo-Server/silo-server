package handlers

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// One projection serves both listeners: the catalog entry keeps the manifest
// presentation source URL as repo_url when the index gives none, and never
// carries stored configuration.
func TestPluginCatalogEntryViewProjection(t *testing.T) {
	t.Parallel()
	entry := plugins.CatalogEntry{RepositoryID: 7, RepositoryDisplayName: "Synthetic", SourceKind: plugins.RepositorySourceExternal, ArchiveURL: "https://example.invalid/a.zip", Manifest: &pluginv1.PluginManifest{PluginId: "org.example.a", Version: "1.0.0", Presentation: &pluginv1.PluginPresentation{DisplayName: "A", SourceUrl: "https://example.invalid/src"}, Capabilities: []*pluginv1.CapabilityDescriptor{{Type: "metadata_provider.v1", Id: "meta"}}}}
	got := pluginCatalogEntryView(entry)
	if got.RepositoryID != 7 || got.PluginID != "org.example.a" || got.RepoURL != "https://example.invalid/src" || len(got.Capabilities) != 1 || got.Presentation == nil || got.Presentation.DisplayName != "A" {
		t.Fatalf("view = %#v", got)
	}
	if got.Routes == nil || got.Assets == nil || got.GlobalConfigSchema == nil || got.UserConfigSchema == nil {
		t.Fatal("collections must be empty, never nil")
	}
	entry.RepoURL = "https://example.invalid/index"
	if pluginCatalogEntryView(entry).RepoURL != "https://example.invalid/index" {
		t.Fatal("index repo URL must win over presentation source")
	}
}

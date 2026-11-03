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

// Silo checks a repository for new versions only while it is enabled, and
// refuses installs from a disabled one, so an installation from any disabled
// repository reports its updates paused.
func TestPluginInstallationSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		repository *plugins.Repository
		wantKind   string
		wantName   string
		wantPaused bool
	}{
		{name: "no repository", wantKind: plugins.RepositorySourceExternal},
		{name: "enabled external", repository: &plugins.Repository{SourceKind: plugins.RepositorySourceExternal, DisplayName: "Mine", Enabled: true}, wantKind: plugins.RepositorySourceExternal, wantName: "Mine"},
		{name: "disabled external", repository: &plugins.Repository{SourceKind: plugins.RepositorySourceExternal, DisplayName: "Mine"}, wantKind: plugins.RepositorySourceExternal, wantName: "Mine", wantPaused: true},
		{name: "hidden approved community", repository: &plugins.Repository{SourceKind: plugins.RepositorySourceApprovedCommunity, DisplayName: "Approved community"}, wantKind: plugins.RepositorySourceApprovedCommunity, wantName: "Approved community", wantPaused: true},
		{name: "enabled silo", repository: &plugins.Repository{SourceKind: plugins.RepositorySourceSilo, DisplayName: "Silo maintained", Enabled: true}, wantKind: plugins.RepositorySourceSilo, wantName: "Silo maintained"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, name, paused := installationSource(tt.repository)
			if kind != tt.wantKind || name != tt.wantName || paused != tt.wantPaused {
				t.Fatalf("installationSource() = (%q, %q, %v), want (%q, %q, %v)", kind, name, paused, tt.wantKind, tt.wantName, tt.wantPaused)
			}
		})
	}
}

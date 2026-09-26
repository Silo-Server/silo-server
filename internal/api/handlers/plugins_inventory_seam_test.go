package handlers

import (
	"errors"
	"net/http"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// The shared inventory seam refuses unwired stores with a 503 APIError so the
// v2 listener answers dependency_unavailable rather than panicking.
func TestPluginInventorySeamRefusesUnwiredStores(t *testing.T) {
	t.Parallel()
	var apiErr *APIError
	_, err := (&PluginHandler{}).ListAdminPluginCatalog(t.Context())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("catalog err = %v", err)
	}
	_, err = (&PluginHandler{}).ListAdminPluginInstallations(t.Context())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("installations err = %v", err)
	}
	var nilHandler *PluginHandler
	if _, err = nilHandler.ListAdminPluginCatalog(t.Context()); !errors.As(err, &apiErr) {
		t.Fatalf("nil handler err = %v", err)
	}
}

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

// Mutation seams refuse before any store or service access when unwired,
// and refuse an empty key before resolving the installation.
func TestPluginMutationSeamsRefuseUnwiredAndEmptyKey(t *testing.T) {
	t.Parallel()
	var apiErr *APIError
	var validation *plugins.ConfigValidationError
	empty := &PluginHandler{}
	if err := empty.SetAdminPluginConfig(t.Context(), 1, PluginConfigInput{Key: "k"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("config err = %v", err)
	}
	if _, err := empty.TestAdminPluginConfig(t.Context(), 1, PluginConfigInput{Key: "k"}); !errors.As(err, &apiErr) {
		t.Fatalf("probe err = %v", err)
	}
	if err := empty.SetAdminPluginAuthBinding(t.Context(), 1, PluginAuthBindingInput{CapabilityID: "c"}); !errors.As(err, &apiErr) {
		t.Fatalf("auth err = %v", err)
	}
	if err := empty.SetAdminPluginTaskBinding(t.Context(), 1, "c", PluginTaskBindingInput{}); !errors.As(err, &apiErr) {
		t.Fatalf("task err = %v", err)
	}
	// A wired service but an empty key is the plugin validation error, judged
	// before the installation lookup so no store is touched.
	withService := &PluginHandler{service: &plugins.Service{}}
	if err := withService.SetAdminPluginConfig(t.Context(), 1, PluginConfigInput{Key: " "}); !errors.As(err, &validation) {
		t.Fatalf("empty key err = %v", err)
	}
	if _, err := withService.TestAdminPluginConfig(t.Context(), 1, PluginConfigInput{}); !errors.As(err, &validation) {
		t.Fatalf("empty probe key err = %v", err)
	}
	// A wired service without an installation store cannot resolve the
	// target and refuses rather than mutating.
	if err := withService.SetAdminPluginConfig(t.Context(), 1, PluginConfigInput{Key: "k"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("no store err = %v", err)
	}
}

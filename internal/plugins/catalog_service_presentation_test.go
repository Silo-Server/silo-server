package plugins

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchRepositoryIndexPreservesPresentationAndRepositoryURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "plugins": [{
            "repo_url": "https://github.com/Silo-Server/example-plugin",
            "manifest": {
              "plugin_id": "silo.example",
              "version": "1.0.0",
              "presentation": {
                "display_name": "Example Plugin",
                "summary": "Explains the example.",
                "source_url": "https://github.com/Silo-Server/example-plugin",
                "changelog_url": "https://github.com/Silo-Server/example-plugin/releases"
              }
            }
          }]
        }`))
	}))
	defer server.Close()

	service := NewCatalogService(nil, CatalogServiceOptions{HTTPClient: server.Client()})
	index, err := service.fetchRepositoryIndex(t.Context(), server.URL)
	if err != nil {
		t.Fatalf("fetchRepositoryIndex() error = %v", err)
	}
	if len(index.Plugins) != 1 {
		t.Fatalf("plugins length = %d, want 1", len(index.Plugins))
	}

	plugin := index.Plugins[0]
	if plugin.RepoURL != "https://github.com/Silo-Server/example-plugin" {
		t.Fatalf("repo_url = %q", plugin.RepoURL)
	}
	presentation := plugin.Manifest.GetPresentation()
	if presentation.GetDisplayName() != "Example Plugin" {
		t.Fatalf("display_name = %q", presentation.GetDisplayName())
	}
	if presentation.GetSummary() != "Explains the example." {
		t.Fatalf("summary = %q", presentation.GetSummary())
	}
	if presentation.GetChangelogUrl() != "https://github.com/Silo-Server/example-plugin/releases" {
		t.Fatalf("changelog_url = %q", presentation.GetChangelogUrl())
	}
}

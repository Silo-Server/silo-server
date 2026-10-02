package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

func TestConfiguredBrowseUserStateBackend(t *testing.T) {
	for _, backend := range []string{"postgres", sqliteUserStoreBackend, ""} {
		t.Run(backend, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.UserDB.Backend = backend
			deps := withDefaults(Dependencies{Config: cfg, BrowseRepo: &catalog.BrowseRepository{}, ItemRepo: &catalog.ItemRepository{}, DetailSvc: &catalog.DetailService{}})
			content, ok := deps.ContentService.(*directContentService)
			if !ok || content.catalogUserState != (backend != sqliteUserStoreBackend) {
				t.Fatalf("configured backend %q: content=%+v", backend, content)
			}
		})
	}
}

package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func (h *AdminHandler) GetCatalogSearchStatus(ctx context.Context) catalog.CatalogSearchRuntimeStatus {
	if h == nil || h.CatalogSearchStatus == nil {
		return catalog.CatalogSearchRuntimeStatus{
			ConfiguredProvider: catalog.SearchProviderPostgres,
			ActiveProvider:     catalog.SearchProviderPostgres,
			Meilisearch: catalog.CatalogSearchMeiliStatus{
				Configured:   false,
				Healthy:      false,
				CircuitState: "not_configured",
			},
			Index: catalog.CatalogSearchIndexStateStatus{
				ExpectedSchemaVersion: catalog.SearchMeilisearchSchemaVersion,
			},
			Tasks: []catalog.CatalogSearchTaskLink{
				{Key: "sync_catalog_search_index", Name: "Sync Catalog Search Index", Href: "/admin/tasks/sync_catalog_search_index"},
				{Key: "rebuild_catalog_search_index", Name: "Rebuild Catalog Search Index", Href: "/admin/tasks/rebuild_catalog_search_index"},
			},
		}
	}
	return h.CatalogSearchStatus.Status(ctx)
}

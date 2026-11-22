package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminItemMetadata struct {
	calls, userID int
	id            string
	mode          adminjob.ItemRefreshMode
	update        handlers.UpdateItemMetadataRequest
	clear         []string
}

func (f *fakeAdminItemMetadata) CreateItemMetadataRefresh(_ context.Context, id string, mode adminjob.ItemRefreshMode, userID int) (*models.AdminJob, error) {
	f.calls++
	f.id, f.mode, f.userID = id, mode, userID
	return &models.AdminJob{ID: "item-refresh-1", JobType: adminjob.JobTypeItemRefresh, Status: adminjob.StatusQueued, CreatedByUserID: userID, RequestedAt: fixedTime()}, nil
}
func (f *fakeAdminItemMetadata) UpdateCatalogItemMetadata(_ context.Context, id string, req handlers.UpdateItemMetadataRequest, cleared []string) (*catalogsvc.ItemDetail, error) {
	f.calls++
	f.id, f.update, f.clear = id, req, cleared
	return &catalogsvc.ItemDetail{ContentID: id, Title: "Updated", Type: "movie"}, nil
}
func adminCatalogItemMetadataFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_item_refresh_queued", operationID: "refreshAdminItemMetadata", method: "POST", path: Prefix + "/admin/items/item-1/refresh-metadata", body: `{"mode":"complete"}`, headers: bearer(memberToken), status: 202, schema: "#/components/schemas/AdminTaskJob", assertHeaders: []string{"Content-Type", "Location", "Retry-After"}, scenario: "A delegated curator receives a persisted refresh job without administrator-only payloads."},
		{name: "admin_item_metadata_updated", operationID: "updateAdminItemMetadata", method: "PATCH", path: Prefix + "/admin/items/item-1/metadata", body: `{"title":"Updated"}`, headers: bearer(memberToken), status: 200, schema: "#/components/schemas/CatalogItemDetail", assertHeaders: []string{"Content-Type"}, scenario: "Successful curation returns canonical catalog detail after persistence."},
	}
}

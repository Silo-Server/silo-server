package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminCatalogTransfer struct {
	calls  int
	err    error
	source handlers.CatalogImportSourceSelection
}

func (f *fakeAdminCatalogTransfer) ExportCatalog(context.Context, catalogseed.ExportOptions) ([]byte, error) {
	f.calls++
	return []byte{31, 139, 8, 0, 1, 2, 3}, f.err
}
func (f *fakeAdminCatalogTransfer) CreateCatalogExportJob(context.Context, int, catalogseed.ExportOptions) (*models.AdminJob, error) {
	f.calls++
	return &models.AdminJob{ID: "catalog-job", JobType: adminjob.JobTypeCatalogExport, Status: adminjob.StatusQueued, RequestedAt: fixedTime()}, f.err
}
func (f *fakeAdminCatalogTransfer) ImportCatalog(_ context.Context, source handlers.CatalogImportSourceSelection, _ catalogseed.ImportOptions) (*catalogseed.ImportResult, error) {
	f.calls++
	f.source = source
	return &catalogseed.ImportResult{ItemsCreated: 3}, f.err
}
func (f *fakeAdminCatalogTransfer) CreateCatalogImportJob(_ context.Context, _ int, source handlers.CatalogImportSourceSelection, _ catalogseed.ImportOptions) (*models.AdminJob, error) {
	f.calls++
	f.source = source
	return &models.AdminJob{ID: "catalog-job", JobType: adminjob.JobTypeCatalogImport, Status: adminjob.StatusQueued, RequestedAt: fixedTime()}, f.err
}
func (f *fakeAdminCatalogTransfer) PublishCatalogExportJob(context.Context, string) (*models.AdminJob, error) {
	f.calls++
	return &models.AdminJob{ID: "catalog-job", PublishedAt: new(fixedTime()), PublicURL: "https://example.invalid/download"}, f.err
}
func (f *fakeAdminCatalogTransfer) GetCatalogSearchStatus(context.Context) catalogsvc.CatalogSearchRuntimeStatus {
	return catalogsvc.CatalogSearchRuntimeStatus{Index: catalogsvc.CatalogSearchIndexStateStatus{LastProcessedEventID: 9007199254740993}}
}

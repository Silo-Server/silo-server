package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

func (f *fakeAdminCollections) AdminCollectionTemplateCatalog(context.Context) templates.Catalog {
	f.catalogReads++
	if f.catalog != nil {
		return *f.catalog
	}
	return templates.CatalogDefault()
}

const adminTemplatesPath = "/api/v2/admin/collections/templates"

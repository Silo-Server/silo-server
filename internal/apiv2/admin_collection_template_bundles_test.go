package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

func (f *fakeAdminCollections) ListAdminCollectionTemplates(context.Context) ([]templates.BundleWithTemplates, error) {
	if f.bundles != nil {
		return f.bundles, nil
	}
	return templates.Default.BundlesWithTemplates(), nil
}

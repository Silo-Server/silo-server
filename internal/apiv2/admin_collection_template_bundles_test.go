package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

func (f *fakeAdminCollections) ListAdminCollectionTemplates(context.Context) ([]templates.BundleWithTemplates, error) {
	f.bundleReads++
	if f.bundles != nil {
		return f.bundles, nil
	}
	return templates.Default.BundlesWithTemplates(), nil
}

const templateBundlesPath = "/api/v2/admin/collections/template-bundles"

// TestAdminTemplateBundlesRequireActingAdmin restates #193 S5 for the bundle
// list: a regular account and a non-primary profile on an admin account are
// refused before the catalog is read.
func TestAdminTemplateBundlesRequireActingAdmin(t *testing.T) {
	f := newFakeAdminCollections()
	h := adminCollectionsTestHandler(t, f)
	for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		requireProblem(t, do(t, h, http.MethodGet, templateBundlesPath, "", headers), TypePermissionDenied)
	}
	if f.bundleReads != 0 {
		t.Fatal("refused request read the bundle catalog")
	}
}

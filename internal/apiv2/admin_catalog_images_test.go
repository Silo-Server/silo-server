package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminImages struct {
	calls      int
	rows       []handlers.AdminImageEntryView
	request    handlers.AdminItemImageRequest
	uploadType string
	upload     []byte
	uploadErr  error
}

func (f *fakeAdminImages) GetAdminItemImages(context.Context, string) (handlers.AdminItemImagesView, error) {
	f.calls++
	return handlers.AdminItemImagesView{Images: f.rows, ProviderErrors: map[string]string{"provider": "private upstream details"}}, nil
}
func (f *fakeAdminImages) ApplyAdminItemImage(_ context.Context, id string, r handlers.AdminItemImageRequest) (handlers.AdminItemImageResult, error) {
	f.calls++
	f.request = r
	return handlers.AdminItemImageResult{ContentID: id, StoredPath: "immutable-artwork", Revision: "revision", Thumbhash: "hash"}, nil
}
func (f *fakeAdminImages) UploadAdminItemImage(_ context.Context, id, imageType string, data []byte) (handlers.AdminItemImageResult, error) {
	f.calls++
	f.uploadType, f.upload = imageType, data
	if f.uploadErr != nil {
		return handlers.AdminItemImageResult{}, f.uploadErr
	}
	return handlers.AdminItemImageResult{ContentID: id, StoredPath: "uploaded-artwork", Revision: "revision", Thumbhash: "hash"}, nil
}
func adminCatalogImagesFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_item_images", operationID: "listAdminItemImages", method: "GET", path: Prefix + "/admin/items/item-1/images", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminImagesPage", assertHeaders: []string{"Content-Type"}, scenario: "Administrator image choices use a bounded collection with current selection."},
		{name: "admin_item_image_apply", operationID: "applyAdminItemImage", method: "POST", path: Prefix + "/admin/items/item-1/images/apply", body: `{"original_url":"source","type":"poster"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminImageApplied", assertHeaders: []string{"Content-Type"}, scenario: "Synchronous artwork publication returns the immutable revision."},
	}
}

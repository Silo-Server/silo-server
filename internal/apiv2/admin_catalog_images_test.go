package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata"
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
func TestAdminImageListLeadsWithLocalArtwork(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminCatalogImages = &fakeAdminImages{rows: []handlers.AdminImageEntryView{
		{ProviderID: "tvdb", OriginalURL: "unrated", Type: "poster"},
		{ProviderID: "tmdb", OriginalURL: "rated", Type: "poster", Rating: 9},
		{ProviderID: metadata.LocalImageProviderID, OriginalURL: "file:///fanart.jpg", Type: "backdrop"},
		{ProviderID: metadata.LocalImageProviderID, OriginalURL: "file:///backdrop1.jpg", Type: "backdrop"},
	}}
	rec := do(t, newTestHandler(t, deps), "GET", Prefix+"/admin/items/item-1/images", "", bearer(adminToken))
	var page AdminImagesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range page.Items {
		got = append(got, row.OriginalURL)
	}
	// Local choices keep discovery order rather than sorting by path.
	if want := []string{"file:///fanart.jpg", "file:///backdrop1.jpg", "rated", "unrated"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestAdminImagePagesAndApply(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminImages{rows: []handlers.AdminImageEntryView{{ProviderID: "p", OriginalURL: "b", URL: "display-b", Type: "poster", Rating: 9}, {ProviderID: "p", OriginalURL: "a", URL: "display-a", Type: "poster", Rating: 1}}}
	deps.AdminCatalogImages = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/item-1/images"
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var page AdminImagesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(page.Items) != 1 || !page.Page.HasMore || page.Items[0].OriginalURL != "b" || strings.Contains(rec.Body.String(), "private upstream") {
		t.Fatalf("first %d %s", rec.Code, rec.Body)
	}
	if f.rows[0].OriginalURL != "b" {
		t.Fatal("sorted service slice")
	}
	cursor := page.Page.NextCursor
	f.rows[0].URL = "rotated display URL"
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("rotated display %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].OriginalURL != "a" || page.Page.HasMore {
		t.Fatalf("continuation lost rating order: %+v", page)
	}
	f.rows[0].OriginalURL = "changed"
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 400 {
		t.Fatalf("changed choices %d %s", rec.Code, rec.Body)
	}
	before := f.calls
	rec = do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 400 || f.calls != before {
		t.Fatalf("scope %d", rec.Code)
	}
	rec = do(t, h, "POST", path+"/apply", `{"original_url":"source","type":"poster","provider_id":"p"}`, bearer(memberToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("auth %d", rec.Code)
	}
	rec = do(t, h, "POST", path+"/apply", `{"original_url":"source","type":"poster","provider_id":"p"}`, bearer(adminToken))
	if rec.Code != 200 || f.request.OriginalURL != "source" || !strings.Contains(rec.Body.String(), `"revision":"revision"`) {
		t.Fatalf("apply %d %s", rec.Code, rec.Body)
	}
	before = f.calls
	rec = do(t, h, "POST", path+"/apply", `{"original_url":"","type":"poster"}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid %d", rec.Code)
	}
	f.rows = nil
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty %d %s", rec.Code, rec.Body)
	}
}
func adminCatalogImagesFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_item_images", operationID: "listAdminItemImages", method: "GET", path: Prefix + "/admin/items/item-1/images", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminImagesPage", assertHeaders: []string{"Content-Type"}, scenario: "Administrator image choices use a bounded collection with current selection."},
		{name: "admin_item_image_apply", operationID: "applyAdminItemImage", method: "POST", path: Prefix + "/admin/items/item-1/images/apply", body: `{"original_url":"source","type":"poster"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminImageApplied", assertHeaders: []string{"Content-Type"}, scenario: "Synchronous artwork publication returns the immutable revision."},
	}
}

// itemImageForm builds an upload form with a type field (left out when empty)
// and an image part of size bytes.
func itemImageForm(t *testing.T, imageType, contentType string, size int) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if imageType != "" {
		if err := w.WriteField("type", imageType); err != nil {
			t.Fatal(err)
		}
	}
	if size >= 0 {
		part, err := w.CreatePart(map[string][]string{
			"Content-Disposition": {`form-data; name="image"; filename="poster.png"`},
			"Content-Type":        {contentType},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bytes.Repeat([]byte{0x89}, size)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	return buf.String(), w.FormDataContentType()
}

func TestAdminImageUpload(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminImages{}
	deps.AdminCatalogImages = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/item-1/images/upload"
	post := func(body, ct, token string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, path, body, with(bearer(token), "Content-Type", ct))
	}

	body, ct := itemImageForm(t, "logo", "image/png", 64)
	requireProblem(t, post(body, ct, memberToken), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized upload reached the service")
	}
	rec := post(body, ct, adminToken)
	var applied AdminImageApplied
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("upload %d %s", rec.Code, rec.Body)
	}
	if applied.ContentID != "item-1" || applied.StoredPath != "uploaded-artwork" || f.uploadType != "logo" || len(f.upload) != 64 {
		t.Fatalf("applied %+v, service got %q with %d bytes", applied, f.uploadType, len(f.upload))
	}

	// The form is checked before the service sees it. Huma validates the type
	// field under form.type; the problem reports it in the body, like a part.
	body, ct = itemImageForm(t, "banner", "image/png", 16)
	if p := requireProblem(t, post(body, ct, adminToken), TypeValidationFailed); len(p.Errors) == 0 || p.Errors[0].Location != "body.type" {
		t.Fatalf("errors = %+v, want body.type", p.Errors)
	}
	body, ct = itemImageForm(t, "poster", "", -1)
	requireProblem(t, post(body, ct, adminToken), TypeValidationFailed)
	body, ct = itemImageForm(t, "backdrop", "image/jpeg", metadata.MaxUploadedImageBytes+1)
	requireProblem(t, post(body, ct, adminToken), TypePayloadTooLarge)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"type":"poster"}`, bearer(adminToken)), TypeUnsupportedMediaType)
	if f.calls != 1 {
		t.Fatalf("service saw %d calls, want the one accepted upload", f.calls)
	}

	// A browser that cannot name the file's type sends it as
	// application/octet-stream; the service checks its bytes instead.
	body, ct = itemImageForm(t, "poster", "application/octet-stream", 16)
	if rec := post(body, ct, adminToken); rec.Code != http.StatusOK || f.calls != 2 || f.uploadType != "poster" {
		t.Fatalf("untyped part %d, service calls %d", rec.Code, f.calls)
	}

	body, ct = itemImageForm(t, "poster", "image/webp", 16)
	f.uploadErr = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "The file is not a JPEG, PNG or WebP image"}
	requireProblem(t, post(body, ct, adminToken), TypeValidationFailed)
	f.uploadErr = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Message: "Uploading images needs artwork storage"}
	requireProblem(t, post(body, ct, adminToken), TypeDependencyUnavailable)
	deps.AdminCatalogImages = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeDependencyUnavailable)
}

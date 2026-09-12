package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeBrandingAssets struct {
	uploads, deletes  int
	kind, contentType string
	size              int
	err               error
}

func (f *fakeBrandingAssets) UploadAdminBrandingAsset(_ context.Context, kind, contentType string, data []byte) (handlers.BrandingAssetView, error) {
	f.uploads++
	f.kind, f.contentType, f.size = kind, contentType, len(data)
	if f.err != nil {
		return handlers.BrandingAssetView{}, f.err
	}
	return handlers.BrandingAssetView{Kind: kind, Ref: "abcdef0123456789.webp", URL: "/api/v1/branding/assets/" + kind + "?v=abcdef0123456789.webp"}, nil
}
func (f *fakeBrandingAssets) DeleteAdminBrandingAsset(_ context.Context, kind string) error {
	f.deletes++
	f.kind = kind
	return f.err
}

func TestAdminBrandingAssetUpload(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeBrandingAssets)
	deps.AdminBrandingAssets = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/branding/assets/wordmark"
	body, ct := posterForm(t, "file", "image/png", 64)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(memberToken), "Content-Type", ct)), TypePermissionDenied)
	if f.uploads != 0 {
		t.Fatal("unauthorized upload reached the service")
	}
	rec := do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"wordmark"`) || !strings.Contains(rec.Body.String(), `"ref":"abcdef0123456789.webp"`) || !strings.Contains(rec.Body.String(), `"url":"/api/v2/branding/assets/wordmark?v=abcdef0123456789.webp"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if f.kind != "wordmark" || f.contentType != "image/png" || f.size != 64 {
		t.Fatalf("seam got %q %q %d", f.kind, f.contentType, f.size)
	}
	// Unknown slot: the path enum refuses before the service.
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/branding/assets/banner", body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	// Per-kind cap: a favicon over 1 MiB is too large even though login_bg allows 12 MiB.
	body, ct = posterForm(t, "file", "image/png", (1<<20)+1)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/branding/assets/favicon", body, with(bearer(adminToken), "Content-Type", ct)), TypePayloadTooLarge)
	// A part media type the contract does not list is a validation failure at body.file.
	body, ct = posterForm(t, "file", "image/gif", 16)
	p := requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	if len(p.Errors) == 0 || p.Errors[0].Location != "body.file" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// JSON on the multipart operation.
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(adminToken)), TypeUnsupportedMediaType)
	if f.uploads != 1 {
		t.Fatalf("service saw %d uploads, want the one accepted", f.uploads)
	}
	// The service's own unsupported-image decision (favicon passthrough) is a field error.
	f.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Unsupported image type", Field: "file"}
	body, ct = posterForm(t, "file", "image/png", 16)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Asset upload storage (S3) is not configured"}
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeDependencyUnavailable)
	deps.AdminBrandingAssets = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeDependencyUnavailable)
}

func TestAdminBrandingAssetDelete(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeBrandingAssets)
	deps.AdminBrandingAssets = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/branding/assets/login_bg"
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(memberToken)), TypePermissionDenied)
	rec := do(t, h, http.MethodDelete, path, "", bearer(adminToken))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || f.deletes != 1 || f.kind != "login_bg" {
		t.Fatal(rec.Code, rec.Body.String(), f.deletes, f.kind)
	}
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/admin/branding/assets/banner", "", bearer(adminToken)), TypeValidationFailed)
	f.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to remove asset"}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(adminToken)), TypeInternalError)
	deps.AdminBrandingAssets = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodDelete, path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

package apiv2

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/danielgtaylor/huma/v2"
)

// AdminBrandingAssetService is the slice of *handlers.BrandingHandler the
// branding asset operations use.
type AdminBrandingAssetService interface {
	UploadAdminBrandingAsset(ctx context.Context, kind, contentType string, data []byte) (handlers.BrandingAssetView, error)
	DeleteAdminBrandingAsset(ctx context.Context, kind string) error
}

// brandingAssetFormOverhead is the multipart framing allowance above the largest
// per-kind image cap (login_bg, 12 MiB); the per-kind cap is enforced below.
const brandingAssetFormOverhead = 1 << 20

type AdminBrandingAssetKindInput struct {
	Kind string `path:"kind" enum:"wordmark,wordmark_light,mark,mark_light,favicon,login_bg" doc:"Branding asset slot" example:"wordmark"`
}
type AdminBrandingAssetForm struct {
	File huma.FormFile `form:"file" contentType:"image/png,image/jpeg,image/webp,image/x-icon,image/vnd.microsoft.icon,image/svg+xml" required:"true" doc:"The image: PNG, JPEG or WebP for every kind; favicon also accepts ICO and SVG. Per-kind caps: 8 MiB (wordmark, wordmark_light, mark, mark_light), 1 MiB (favicon), 12 MiB (login_bg)."`
}
type AdminBrandingAssetUploadInput struct {
	Kind    string `path:"kind" enum:"wordmark,wordmark_light,mark,mark_light,favicon,login_bg" doc:"Branding asset slot" example:"wordmark"`
	RawBody huma.MultipartFormFiles[AdminBrandingAssetForm]
}
type AdminBrandingAsset struct {
	Kind string `json:"kind" enum:"wordmark,wordmark_light,mark,mark_light,favicon,login_bg"`
	Ref  string `json:"ref" doc:"Content-addressed reference of the stored bytes"`
	URL  string `json:"url" doc:"Stable public asset path with the ref as cache-buster"`
}
type AdminBrandingAssetOutput struct{ Body AdminBrandingAsset }

func adminBrandingAssetProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // the seam returns the value directly
	if !ok {
		return serviceProblem(err)
	}
	switch {
	case apiErr.Field != "":
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	case apiErr.Status == http.StatusServiceUnavailable:
		return NewProblem(TypeDependencyUnavailable, apiErr.Message).WithRetryAfter(30)
	}
	return serviceProblem(err)
}

func registerAdminBrandingAssets(reg *Registry) {
	maxKind := branding.MaxUploadBytes(branding.KindLoginBg)
	upload := humaOp(http.MethodPost, Prefix+"/admin/branding/assets/{kind}", "uploadAdminBrandingAsset", "admin-settings",
		"Store a custom branding image for one slot and point the slot at it. One unconditional assignment: a delayed retry replaces a newer choice, so clients never retry automatically. No cache purge or client refresh receipt.")
	upload.Errors = append(upload.Errors, http.StatusRequestEntityTooLarge)
	Register(reg, Operation{Operation: upload, Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(upload.Method), ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, MaxBodyBytes: maxKind + brandingAssetFormOverhead},
		func(ctx context.Context, in *AdminBrandingAssetUploadInput) (*AdminBrandingAssetOutput, error) {
			if reg.deps.AdminBrandingAssets == nil {
				return nil, unavailable("branding")
			}
			form := in.RawBody.Data()
			if form == nil || !form.File.IsSet {
				return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
					WithErrors(ProblemError{Location: "body.file", Code: codeRequired, Detail: "an image file is required"})
			}
			limit := branding.MaxUploadBytes(branding.AssetKind(in.Kind))
			tooLarge := NewProblem(TypePayloadTooLarge, "The image exceeds the maximum size for this asset.")
			if limit <= 0 || form.File.Size > limit {
				return nil, tooLarge
			}
			data, err := io.ReadAll(io.LimitReader(form.File, limit+1))
			if err != nil {
				return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
			}
			if int64(len(data)) > limit {
				return nil, tooLarge
			}
			view, err := reg.deps.AdminBrandingAssets.UploadAdminBrandingAsset(ctx, in.Kind, form.File.ContentType, data)
			if err != nil {
				return nil, adminBrandingAssetProblem(err)
			}
			return &AdminBrandingAssetOutput{Body: AdminBrandingAsset{
				Kind: view.Kind,
				Ref:  view.Ref,
				URL:  Prefix + "/branding/assets/" + url.PathEscape(view.Kind) + "?v=" + url.QueryEscape(view.Ref),
			}}, nil
		})
	remove := humaOp(http.MethodDelete, Prefix+"/admin/branding/assets/{kind}", "deleteAdminBrandingAsset", "admin-settings",
		"Clear the custom image of one branding slot so the bundled default serves again. Stored bytes are left in place. A delayed retry also clears an image uploaded after the first success, so clients never retry automatically.")
	remove.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: remove, Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(remove.Method), ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable},
		func(ctx context.Context, in *AdminBrandingAssetKindInput) (*struct{}, error) {
			if reg.deps.AdminBrandingAssets == nil {
				return nil, unavailable("branding")
			}
			if err := reg.deps.AdminBrandingAssets.DeleteAdminBrandingAsset(ctx, in.Kind); err != nil {
				return nil, adminBrandingAssetProblem(err)
			}
			return &struct{}{}, nil
		})
}

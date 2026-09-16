package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/branding"
)

const (
	brandingFieldKind = "kind"
	brandingFieldFile = "file"
)

func unknownBrandingKind() *APIError {
	return fieldError(brandingFieldKind, "Unknown branding asset")
}

// BrandingAssetView is the stored custom asset a v2 upload acknowledges.
type BrandingAssetView struct {
	Kind string
	Ref  string
	URL  string
}

// UploadAdminBrandingAsset stores a custom branding image and points the kind
// at it. The write is one unconditional settings assignment after the object
// upload; a delayed retry replaces whatever an administrator chose in between,
// so the v2 operation is non_retryable. Size limits are the caller's.
func (h *BrandingHandler) UploadAdminBrandingAsset(ctx context.Context, kind, contentType string, data []byte) (BrandingAssetView, error) {
	if h == nil || !h.svc.HasStorage() {
		return BrandingAssetView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Branding is not configured")
	}
	if !branding.IsValidKind(kind) {
		return BrandingAssetView{}, unknownBrandingKind()
	}
	ref, err := h.svc.UploadAsset(ctx, branding.AssetKind(kind), data, contentType)
	switch {
	case errors.Is(err, branding.ErrUnsupportedImage):
		return BrandingAssetView{}, fieldError(brandingFieldFile, "Unsupported image type; use PNG, JPEG, WebP (or PNG/ICO/SVG for favicon)")
	case errors.Is(err, branding.ErrInvalidKind):
		return BrandingAssetView{}, unknownBrandingKind()
	case err != nil:
		return BrandingAssetView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to store asset")
	}
	return BrandingAssetView{Kind: kind, Ref: ref, URL: branding.AssetURLFor(branding.AssetKind(kind), ref)}, nil
}

// DeleteAdminBrandingAsset clears the custom asset of a kind. Clearing an
// already-empty kind converges, but a delayed retry also clears an asset
// uploaded after the first success; the v2 operation is non_retryable.
func (h *BrandingHandler) DeleteAdminBrandingAsset(ctx context.Context, kind string) error {
	if h == nil || h.svc == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Branding is not configured")
	}
	if !branding.IsValidKind(kind) {
		return unknownBrandingKind()
	}
	if err := h.svc.DeleteAsset(ctx, branding.AssetKind(kind)); err != nil {
		if errors.Is(err, branding.ErrInvalidKind) {
			return unknownBrandingKind()
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove asset")
	}
	return nil
}

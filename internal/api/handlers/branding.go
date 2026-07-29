package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/branding"
)

// BrandingHandler exposes the public branding read, the public asset serving
// endpoint, and the admin asset upload/delete endpoints. All branding logic is
// delegated to the branding.Service.
type BrandingHandler struct {
	svc *branding.Service
}

// NewBrandingHandler constructs a BrandingHandler around a branding service.
func NewBrandingHandler(svc *branding.Service) *BrandingHandler {
	return &BrandingHandler{svc: svc}
}

// brandingResponse is returned by GET /theme/branding. It is a superset of the
// historical {server_name, login_subtitle} shape — new fields are additive per
// the v1 API rules. Asset URLs are stable, cache-bustable paths (empty when no
// custom asset is set).
type brandingResponse struct {
	ServerName       string `json:"server_name"`
	LoginSubtitle    string `json:"login_subtitle"`
	AccentColor      string `json:"accent_color,omitempty"`
	DefaultTheme     string `json:"default_theme,omitempty"`
	WordmarkURL      string `json:"wordmark_url,omitempty"`
	MarkURL          string `json:"mark_url,omitempty"`
	FaviconURL       string `json:"favicon_url,omitempty"`
	LoginBgURL       string `json:"login_bg_url,omitempty"`
	StorageAvailable bool   `json:"storage_available"`
}

// HandleGetBranding returns the server branding configuration. Public endpoint —
// no authentication required so branding applies before login (white-label).
func (h *BrandingHandler) HandleGetBranding(w http.ResponseWriter, r *http.Request) {
	snap := h.svc.Load(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, brandingResponse{
		ServerName:       snap.ServerName,
		LoginSubtitle:    snap.LoginSubtitle,
		AccentColor:      snap.AccentColor,
		DefaultTheme:     snap.DefaultTheme,
		WordmarkURL:      snap.AssetURL(branding.KindWordmark),
		MarkURL:          snap.AssetURL(branding.KindMark),
		FaviconURL:       snap.AssetURL(branding.KindFavicon),
		LoginBgURL:       snap.AssetURL(branding.KindLoginBg),
		StorageAvailable: h.svc.HasStorage(),
	})
}

// HandleServeAsset streams a custom branding asset. Public endpoint — assets are
// non-sensitive and must load before login. Assets are content-addressed, so
// they are served with a long immutable cache lifetime.
func (h *BrandingHandler) HandleServeAsset(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if !branding.IsValidKind(kind) {
		writeError(w, http.StatusNotFound, "not_found", "Unknown branding asset")
		return
	}

	data, contentType, ref, err := h.svc.GetAsset(r.Context(), branding.AssetKind(kind))
	switch {
	case errors.Is(err, branding.ErrAssetNotConfigured):
		writeError(w, http.StatusNotFound, "not_found", "No custom asset configured")
		return
	case errors.Is(err, branding.ErrStorageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Asset storage is not configured")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load asset")
		return
	}

	etag := `"` + ref + `"`
	// Uploaded assets (e.g. SVG favicons) are admin-controlled but served from
	// the app origin. Prevent content-type sniffing and neutralize scripts in a
	// directly-navigated SVG (stored-XSS defense).
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", branding.AssetContentSecurityPolicy)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// HandleUploadAsset accepts a multipart image upload for a branding asset kind
// and records it. Admin-only (wired behind requireActingAdmin).
func (h *BrandingHandler) HandleUploadAsset(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if !branding.IsValidKind(kind) {
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown branding asset")
		return
	}
	if !h.svc.HasStorage() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Asset upload storage (S3) is not configured")
		return
	}

	maxBytes := branding.MaxUploadBytes(branding.AssetKind(kind))
	if err := r.ParseMultipartForm(maxBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing file field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}
	if int64(len(data)) > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Image exceeds the maximum size for this asset")
		return
	}

	ref, err := h.svc.UploadAsset(r.Context(), branding.AssetKind(kind), data, header.Header.Get("Content-Type"))
	switch {
	case errors.Is(err, branding.ErrUnsupportedImage):
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported image type; use PNG, JPEG, WebP (or PNG/ICO/SVG for favicon)")
		return
	case errors.Is(err, branding.ErrStorageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Asset upload storage (S3) is not configured")
		return
	case errors.Is(err, branding.ErrInvalidKind):
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown branding asset")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store asset")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"kind": kind,
		"ref":  ref,
		"url":  branding.AssetURLFor(branding.AssetKind(kind), ref),
	})
}

// HandleDeleteAsset clears a custom branding asset. Admin-only.
func (h *BrandingHandler) HandleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if !branding.IsValidKind(kind) {
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown branding asset")
		return
	}
	if err := h.svc.DeleteAsset(r.Context(), branding.AssetKind(kind)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove asset")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package handlers

import (
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
//
// default_theme and the light logo URLs are retired from the web client and
// from /api/v2, but this frozen v1 response keeps reporting whatever is stored
// until v1 retires.
type brandingResponse struct {
	ServerName       string `json:"server_name"`
	LoginSubtitle    string `json:"login_subtitle"`
	AccentColor      string `json:"accent_color,omitempty"`
	DefaultTheme     string `json:"default_theme,omitempty"`
	WordmarkURL      string `json:"wordmark_url,omitempty"`
	WordmarkLightURL string `json:"wordmark_light_url,omitempty"`
	MarkURL          string `json:"mark_url,omitempty"`
	MarkLightURL     string `json:"mark_light_url,omitempty"`
	FaviconURL       string `json:"favicon_url,omitempty"`
	LoginBgURL       string `json:"login_bg_url,omitempty"`
	StorageAvailable bool   `json:"storage_available"`
}

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

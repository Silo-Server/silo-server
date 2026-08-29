package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const DirectLibraryArtworkParam = "identity"

type directLibraryArtworkResolver interface {
	ResolveFile(ctx context.Context, reference string, identity artworkurl.LibraryIdentity, ifNoneMatch string) (metadata.DirectLibraryArtworkFile, error)
}

type DirectLibraryArtworkHandler struct {
	resolver directLibraryArtworkResolver
	signer   *artworkurl.Signer
}

func NewDirectLibraryArtworkHandler(resolver directLibraryArtworkResolver, signer *artworkurl.Signer) *DirectLibraryArtworkHandler {
	if resolver == nil || signer == nil {
		return nil
	}
	return &DirectLibraryArtworkHandler{resolver: resolver, signer: signer}
}

func (h *DirectLibraryArtworkHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	reference, identity, expiresAt, err := h.signer.VerifyLibraryURL(
		chi.URLParam(r, DirectLibraryArtworkParam),
		r.URL.Query().Get(artworkurl.ExpiresParam),
		r.URL.Query().Get(artworkurl.SignatureParam),
		now,
	)
	if errors.Is(err, artworkurl.ErrExpired) {
		artworkmetrics.Delivery("direct_library", "expired_signature")
		writeError(w, http.StatusUnauthorized, "artwork_url_expired", "Artwork URL expired")
		return
	}
	if err != nil {
		artworkmetrics.Delivery("direct_library", "invalid_signature")
		artworkNotFound(w)
		return
	}
	artwork, err := h.resolver.ResolveFile(r.Context(), reference, identity, r.Header.Get("If-None-Match"))
	if err != nil {
		artworkmetrics.Delivery("direct_library", "miss")
		artworkNotFound(w)
		return
	}
	header := w.Header()
	if artwork.MediaType != "" {
		header.Set("Content-Type", artwork.MediaType)
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", artworkContentSecurityPolicy)
	header.Set("ETag", `"`+artwork.Fingerprint+`"`)
	header.Set("Cache-Control", artworkCacheControl(expiresAt, now))
	if artwork.NotModified {
		artworkmetrics.Delivery("direct_library", "conditional_hit")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	artworkmetrics.Delivery("direct_library", "served")
	defer func() { _ = artwork.File.Close() }()
	counting := &artworkCountingResponseWriter{ResponseWriter: w}
	http.ServeContent(counting, r, "artwork", artwork.ModTime, artwork.File)
	artworkmetrics.DeliveryBytes("direct_library", counting.bytes)
}

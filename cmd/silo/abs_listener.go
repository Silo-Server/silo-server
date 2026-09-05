package main

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// absMounter is the narrow interface the listener needs from the
// Audiobookshelf handler.
type absMounter interface {
	Mount(r chi.Router)
}

// newAudiobookshelfListener builds the Audiobookshelf-compatible listener: a
// dedicated http.Server on its own port (default :13378), mirroring the
// Jellyfin compat layout. The ABS handler mounts onto a fresh chi router here
// so /ping, /healthcheck, /status, /login, /socket.io, etc. own the URL space
// at the root — no SPA fallback, no collision with silo's /api/v1.
//
// It lives in its own file and its own function because the route inventory's
// exclusion for it names this function. A file-wide or package-wide exclusion
// would silently cover any other router a later change adds to cmd/silo; this
// one covers exactly the compatibility listener it was written for. ABS is an
// external wire contract, not Silo's native API, so it is out of scope for the
// v2 migration and carries no inventory rows.
func newAudiobookshelfListener(listen string, handler absMounter, ipResolver *clientip.Resolver) *http.Server {
	absRouter := chi.NewRouter()
	if ipResolver != nil {
		absRouter.Use(clientip.Middleware(ipResolver))
	}
	absRouter.Use(chimiddleware.Recoverer)
	absRouter.Use(httpstream.CompressExcept(5, abs.SkipMediaCompression))
	handler.Mount(absRouter)
	return &http.Server{
		Addr:              listen,
		Handler:           absRouter,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
}

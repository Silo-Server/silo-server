package httpstream

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// CompressExcept mounts chi's compressor but leaves the ResponseWriter
// untouched when skip reports true, preserving io.ReaderFrom on bulk routes.
func CompressExcept(level int, skip func(*http.Request) bool, types ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		compressed := middleware.Compress(level, types...)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			compressed.ServeHTTP(w, r)
		})
	}
}

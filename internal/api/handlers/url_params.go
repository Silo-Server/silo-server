package handlers

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// decodedURLParam returns the named chi route parameter with percent-encoding
// removed. chi matches routes against the raw (escaped) request path, so
// parameters arrive still encoded when clients escape reserved characters —
// e.g. encodeURIComponent("plugin:6:introdb") arrives as
// "plugin%3A6%3Aintrodb". Returns an error for malformed escape sequences.
func decodedURLParam(r *http.Request, name string) (string, error) {
	return url.PathUnescape(chi.URLParam(r, name))
}

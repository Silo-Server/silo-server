// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Handlers is the fixture's handler type.
type Handlers struct{}

// Health writes JSON.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Create reads JSON.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	_ = json.NewEncoder(w).Encode(body)
}

func baseMiddleware(next http.Handler) http.Handler { return next }
func requireAdmin(next http.Handler) http.Handler   { return next }
func rateLimit(next http.Handler) http.Handler      { return next }

func registerExtras(r chi.Router, h *Handlers) {
	r.Get("/extras", h.Health)
}

// NewRouter is the fixture listener entry point.
func NewRouter(enableAdmin bool, adminHandler *Handlers) http.Handler {
	r := chi.NewRouter()
	r.Use(baseMiddleware)
	h := &Handlers{}
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.Health)
		if enableAdmin {
			r.Group(func(r chi.Router) {
				r.Use(requireAdmin)
				r.Post("/admin/things", adminHandler.Create)
			})
		} else {
			r.Post("/admin/things", h.Create)
		}
		r.With(rateLimit).HandleFunc("/wildcard", h.Health)
		registerExtras(r, h)
	})
	return r
}

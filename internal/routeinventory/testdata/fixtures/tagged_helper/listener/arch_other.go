//go:build !amd64

package listener

import "github.com/go-chi/chi/v5"

func arch(r chi.Router) {}

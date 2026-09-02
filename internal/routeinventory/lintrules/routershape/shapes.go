// Package routershape backs the legacy native route inventory's lint rule. The
// ruleguard rule in this directory (build tag "ruleguard") reports a type
// assertion or type switch whose target can register routes; it decides that
// by asking whether the target type implements one of the single-method
// interfaces below. Each interface spells one registration method with the
// signature chi.Router or *http.ServeMux gives it, so an alias, a defined
// type, an interface embedding chi.Router and a structural interface that
// spells the method itself all satisfy the same test. Nothing imports this
// package at runtime.
package routershape

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Handles is chi.Router.Handle and (*http.ServeMux).Handle.
type Handles interface{ Handle(string, http.Handler) }

// HandlesFunc is chi.Router.HandleFunc.
type HandlesFunc interface {
	HandleFunc(string, http.HandlerFunc)
}

// HandlesMuxFunc is (*http.ServeMux).HandleFunc.
type HandlesMuxFunc interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}

// Methods is chi.Router.Method.
type Methods interface {
	Method(string, string, http.Handler)
}

// MethodFuncs is chi.Router.MethodFunc.
type MethodFuncs interface {
	MethodFunc(string, string, http.HandlerFunc)
}

// Verb registrations, one per chi.Router verb method.
type (
	Connects interface {
		Connect(string, http.HandlerFunc)
	}
	Deletes interface {
		Delete(string, http.HandlerFunc)
	}
	Gets interface {
		Get(string, http.HandlerFunc)
	}
	Heads interface {
		Head(string, http.HandlerFunc)
	}
	Optionss interface {
		Options(string, http.HandlerFunc)
	}
	Patches interface {
		Patch(string, http.HandlerFunc)
	}
	Posts interface {
		Post(string, http.HandlerFunc)
	}
	Puts interface {
		Put(string, http.HandlerFunc)
	}
	Traces interface {
		Trace(string, http.HandlerFunc)
	}
)

// Mounts is chi.Router.Mount.
type Mounts interface{ Mount(string, http.Handler) }

// Uses is chi.Router.Use.
type Uses interface {
	Use(...func(http.Handler) http.Handler)
}

// NotFounds is chi.Router.NotFound.
type NotFounds interface{ NotFound(http.HandlerFunc) }

// MethodNotAlloweds is chi.Router.MethodNotAllowed.
type MethodNotAlloweds interface{ MethodNotAllowed(http.HandlerFunc) }

// Routes is chi.Router.Route: it hands a router to its callback and returns
// one, so a value carrying it alone is a registration surface.
type Routes interface {
	Route(string, func(chi.Router)) chi.Router
}

// Groups is chi.Router.Group.
type Groups interface {
	Group(func(chi.Router)) chi.Router
}

// Withs is chi.Router.With: the router it returns registers on the shared tree.
type Withs interface {
	With(...func(http.Handler) http.Handler) chi.Router
}

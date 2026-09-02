//go:build ruleguard

// Package lintrules holds the golangci-lint (gocritic/ruleguard) rules that the
// legacy native route inventory's completeness guarantee relies on. See
// docs/architecture/api-contract.md, "Legacy native route inventory".
package lintrules

import "github.com/quasilyte/go-ruleguard/dsl"

// routerTypeRecovery forbids recovering a router from a value outside tests.
//
// Every inventoried listener hands its router out as an http.Handler. The
// generator enumerates registrations inside the listener entry functions and
// refuses router constructions elsewhere; a type assertion or type switch back
// to chi.Router, chi.Routes, *chi.Mux or *http.ServeMux is the one remaining
// way to register a route after the entry function returned, so it is a lint
// error rather than something the generator tries to trace. Tests are exempt
// (see the exclusion in .golangci.yml): they assert to chi.Routes to walk a
// router, and register nothing that ships.
func routerTypeRecovery(m dsl.Matcher) {
	m.Import("github.com/go-chi/chi/v5")

	m.Match(`$x.($t)`).
		Where(m["t"].Type.Is(`chi.Router`) || m["t"].Type.Is(`chi.Routes`) ||
			m["t"].Type.Is(`*chi.Mux`) || m["t"].Type.Is(`*http.ServeMux`)).
		Report(`route inventory: type assertion to a router type recovers a router the inventory cannot see; register inside the listener entry function`)

	m.Match(
		`switch $x.(type) { $*_; case $t: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $t: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $t, $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $*_, $t: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $*_, $t: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $*_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $*_, $t, $*_: $*_; $*_ }`,
	).
		Where(m["t"].Type.Is(`chi.Router`) || m["t"].Type.Is(`chi.Routes`) ||
			m["t"].Type.Is(`*chi.Mux`) || m["t"].Type.Is(`*http.ServeMux`)).
		Report(`route inventory: type switch on a router type recovers a router the inventory cannot see; register inside the listener entry function`)
}

//go:build ruleguard

package lintrules

import "github.com/quasilyte/go-ruleguard/dsl"

// routerTypeRecovery reports a type assertion, type switch case or generic
// instantiation whose type can register routes: chi.Router, chi.Routes,
// *chi.Mux, *http.ServeMux, and any alias, defined type, embedding interface
// or structural interface whose method set carries one registration method
// with chi's or net/http's signature (see shapes.go). The check is on the
// resolved type's method set, not the spelling. Route(), Group() and With()
// count on their own: each returns a chi.Router (Route and Group hand one to
// their callback as well), so a structural interface spelling only one of
// them recovers a full router.
//
// A type that is not a router but happens to carry one chi-shaped registration
// method (a fake registry with Get(string, http.HandlerFunc), say) is reported
// too; that is a false positive by design, and `//nolint:gocritic // reason`
// on the line is the accepted escape. No such type exists in the tree today.
//
// The rule also reports the reflect calls that reach a method or the memory
// behind a value (MethodByName, Method, NumMethod, NewAt, UnsafePointer,
// UnsafeAddr, Pointer), which recover a router without naming its type.
//
// This is defense in depth. The guarantee itself is structural: every listener
// entry function returns a sealed handler whose dynamic type is never a router,
// so no assertion can recover one (the generator checks that shape). The rule
// catches the attempt early and covers routers that are not listeners. Tests
// are exempt (see .golangci.yml): they walk routers and register nothing that
// ships.
func routerTypeRecovery(m dsl.Matcher) {
	m.Import("github.com/go-chi/chi/v5")

	registers := func(t dsl.Var) bool {
		return t.Type.Underlying().Is(`*chi.Mux`) ||
			t.Type.Underlying().Is(`*http.ServeMux`) ||
			t.Type.Implements(`chi.Routes`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Handles`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.HandlesFunc`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.HandlesMuxFunc`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Methods`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.MethodFuncs`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Connects`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Deletes`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Gets`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Heads`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Optionss`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Patches`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Posts`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Puts`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Traces`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Mounts`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Uses`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.NotFounds`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.MethodNotAlloweds`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Routes`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Groups`) ||
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.Withs`)
	}

	m.Match(`$x.($t)`).
		Where(registers(m["t"])).
		Report(`route inventory: type assertion to a type that registers routes recovers a router the inventory cannot see; register inside the listener constructor`)

	// gogrep has no clause-position wildcard and does not retry another
	// `$*_` split once Where rejects the first binding, so the router type is
	// spelled at explicit positions: first to fourth in a clause's type list,
	// and the clause at every position relative to a default clause (no
	// default, default last with and without cases between, default in the
	// middle), for the plain, `$_ :=`, and init-statement switch forms. The
	// bound: a router fifth or later in one clause's type list is not
	// reported here; the generator reports every position.
	m.Match(
		`switch $x.(type) { $*_; case $t, $*_: $*_; $*_ }`,
		`switch $x.(type) { case $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $_, $t, $*_: $*_; $*_ }`,
		`switch $x.(type) { case $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $x.(type) { case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $x.(type) { case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { case $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { case $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { case $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { case $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { case $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { case $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $_; $_ := $x.(type) { $*_; case $_, $_, $_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
	).
		Where(registers(m["t"])).
		Report(`route inventory: type switch on a type that registers routes recovers a router the inventory cannot see; register inside the listener constructor`)

	// A one-argument instantiation is an IndexExpr, which ruleguard visits
	// wherever it appears, so `$_[$t]` alone covers the value, the call and
	// the composite literal. A multi-argument instantiation is an
	// IndexListExpr, which ruleguard's walker never visits (nor anything
	// inside it), so it is matched at the node that contains it: a call, an
	// assignment, a var declaration, a return, or a composite literal, with
	// the router type spelled at explicit positions one to four (gogrep does
	// not retry a `$*_` split once Where rejects the first binding). The
	// bound: a router fifth or later in the type-argument list, a
	// multi-argument instantiation passed directly as a call argument or as
	// a composite-literal element, and an inferred instantiation (no index
	// expression at all) are reported only by the generator.
	m.Match(
		`$_[$t]`,
		`$_[$t, $*_]($*_)`,
		`$_[$_, $t, $*_]($*_)`,
		`$_[$_, $_, $t, $*_]($*_)`,
		`$_[$_, $_, $_, $t, $*_]($*_)`,
		`$_ := $_[$t, $*_]`,
		`$_ := $_[$_, $t, $*_]`,
		`$_ := $_[$_, $_, $t, $*_]`,
		`$_ := $_[$_, $_, $_, $t, $*_]`,
		`$_ = $_[$t, $*_]`,
		`$_ = $_[$_, $t, $*_]`,
		`$_ = $_[$_, $_, $t, $*_]`,
		`$_ = $_[$_, $_, $_, $t, $*_]`,
		`var $_ = $_[$t, $*_]`,
		`var $_ = $_[$_, $t, $*_]`,
		`var $_ = $_[$_, $_, $t, $*_]`,
		`var $_ = $_[$_, $_, $_, $t, $*_]`,
		`var $_ $_[$t, $*_]`,
		`var $_ $_[$_, $t, $*_]`,
		`var $_ $_[$_, $_, $t, $*_]`,
		`var $_ $_[$_, $_, $_, $t, $*_]`,
		`var $_ $_[$t, $*_] = $_`,
		`var $_ $_[$_, $t, $*_] = $_`,
		`var $_ $_[$_, $_, $t, $*_] = $_`,
		`var $_ $_[$_, $_, $_, $t, $*_] = $_`,
		`return $_[$t, $*_]`,
		`return $_[$_, $t, $*_]`,
		`return $_[$_, $_, $t, $*_]`,
		`return $_[$_, $_, $_, $t, $*_]`,
		`$_[$t, $*_]{$*_}`,
		`$_[$_, $t, $*_]{$*_}`,
		`$_[$_, $_, $t, $*_]{$*_}`,
		`$_[$_, $_, $_, $t, $*_]{$*_}`,
	).
		Where(registers(m["t"])).
		Report(`route inventory: a generic instantiated with a type that registers routes can assert a handler to a router the inventory cannot see`)

	m.Match(`$v.MethodByName($_)`, `$v.Method($_)`, `$v.NumMethod()`).
		Where(m["v"].Type.Is(`reflect.Value`) || m["v"].Type.Is(`reflect.Type`)).
		Report(`route inventory: reflect method lookup can call a registration method the inventory cannot see; register inside the listener constructor`)

	m.Match(`$v.UnsafePointer()`, `$v.UnsafeAddr()`, `$v.Pointer()`).
		Where(m["v"].Type.Is(`reflect.Value`)).
		Report(`route inventory: reflect.Value address exposure can rebuild a pointer to a router the inventory cannot see`)

	m.Match(`reflect.NewAt($_, $_)`).
		Report(`route inventory: reflect.NewAt rebuilds a typed pointer to a router the inventory cannot see`)
}

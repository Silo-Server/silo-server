//go:build ruleguard

package lintrules

import "github.com/quasilyte/go-ruleguard/dsl"

// routerTypeRecovery reports a type assertion, type switch case or generic
// instantiation whose type can register routes: chi.Router, chi.Routes,
// *chi.Mux, *http.ServeMux, and any alias, defined type, embedding interface
// or structural interface whose method set carries one registration method
// with chi's or net/http's signature (see shapes.go). The check is on the
// resolved type's method set, not the spelling. Route(), Group() and With()
// are not in the list on their own: chi.Router carries the others too, and a
// structural interface spelling only Route still needs a Router to call it on.
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
			t.Type.Implements(`github.com/Silo-Server/silo-server/internal/routeinventory/lintrules/routershape.MethodNotAlloweds`)
	}

	m.Match(`$x.($t)`).
		Where(registers(m["t"])).
		Report(`route inventory: type assertion to a type that registers routes recovers a router the inventory cannot see; register inside the listener constructor`)

	// gogrep has no clause-position wildcard, so the router case is spelled
	// at every position relative to a default clause: no default, default
	// last (with and without cases between), and default in the middle. The
	// set was checked against a 92-shape matrix of case order, binding, body
	// and multi-type clauses; every shape is reported.
	m.Match(
		`switch $x.(type) { $*_; case $*_, $t, $*_: $*_; $*_ }`,
		`switch $x.(type) { case $*_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $x.(type) { $*_; case $*_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $*_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $x.(type) { $*_; case $*_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $*_, $t, $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { case $*_, $t, $*_: $*_; case $*_: $*_; $*_ }`,
		`switch $_ := $x.(type) { $*_; case $*_, $t, $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $*_, $t, $*_: $*_; case $*_: $*_; $*_; default: $*_ }`,
		`switch $_ := $x.(type) { $*_; case $*_, $t, $*_: $*_; default: $*_; case $*_: $*_; $*_ }`,
	).
		Where(registers(m["t"])).
		Report(`route inventory: type switch on a type that registers routes recovers a router the inventory cannot see; register inside the listener constructor`)

	m.Match(`$_[$t]($*_)`, `$_[$*_, $t]($*_)`, `$_[$t, $*_]($*_)`, `$_[$*_, $t, $*_]($*_)`).
		Where(registers(m["t"])).
		Report(`route inventory: a generic instantiated with a type that registers routes can assert a handler to a router the inventory cannot see`)

	m.Match(`$v.MethodByName($_)`).
		Where(m["v"].Type.Is(`reflect.Value`)).
		Report(`route inventory: reflect.Value.MethodByName can call a registration method the inventory cannot see; register inside the listener constructor`)
}

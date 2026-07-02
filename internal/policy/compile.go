package policy

import (
	"strings"

	"github.com/open-policy-agent/opa/v1/ast"
)

// LockedCapabilities returns the builtin set allowed for administrator-authored
// Rego. It removes non-pure and network-capable builtins from OPA's current
// capabilities.
func LockedCapabilities() *ast.Capabilities {
	caps := ast.CapabilitiesForThisVersion()
	builtins := caps.Builtins[:0]
	for _, builtin := range caps.Builtins {
		if lockedBuiltin(builtin.Name) {
			continue
		}
		builtins = append(builtins, builtin)
	}
	caps.Builtins = builtins
	return caps
}

func lockedBuiltin(name string) bool {
	switch name {
	case "http.send", "opa.runtime", "rego.parse_module", "rego.parse_modules":
		return true
	}
	return strings.HasPrefix(name, "net.")
}

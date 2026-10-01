// Package envutil holds the parsers shared by everything that reads
// configuration out of the environment.
//
// It exists because "is this environment variable on?" had been re-implemented
// nine times across the tree, each copy accepting a slightly different set of
// spellings — so whether SILO_X=enabled worked depended on which subsystem read
// it. New env flags belong here; the remaining ad hoc copies should migrate as
// the code around them is touched.
package envutil

import (
	"os"
	"strings"
)

// Truthy reports whether a raw environment value means "on". Case and
// surrounding whitespace are ignored. Anything else, including an empty or
// unset value, is false — a flag has to be turned on deliberately.
func Truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

// Bool reports whether the named environment variable is set to a truthy value.
func Bool(name string) bool { return Truthy(os.Getenv(name)) }

// BoolDefault reports whether the named environment variable is on, falling back
// to def when the variable is unset or empty — whitespace-only counts as empty,
// since a value that survives a shell only as spaces was never really supplied.
//
// A value that IS present but unrecognised ("flase", "no", "0") reads as false
// rather than as def. For a flag that defaults on, that means a typo in the kill
// switch turns the flag OFF, which is the fail-safe direction: the operator was
// reaching for "off", and a mistyped disable that silently left the feature
// running is the failure that actually hurts.
func BoolDefault(name string, def bool) bool {
	if !IsSet(name) {
		return def
	}
	return Truthy(os.Getenv(name))
}

// IsSet reports whether the named environment variable carries a non-empty value
// once surrounding whitespace is trimmed. It answers "did the operator touch this
// knob?", which a default-on flag has to ask separately from "is it on?" — an
// unset knob and one explicitly set to false want different behaviour when
// something else would otherwise derive the value.
func IsSet(name string) bool { return strings.TrimSpace(os.Getenv(name)) != "" }

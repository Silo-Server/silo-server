package silo_custom.scope

import rego.v1

override(_, _) := result if {
	result := {}
	false
}

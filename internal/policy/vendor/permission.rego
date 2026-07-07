package silo.permission

import rego.v1

decision := tightened if {
	override := data.silo_custom.permission.override(base_decision, input)
	tightened := tighten(base_decision, override)
} else := base_decision

base_decision := acting_admin_decision(input) if {
	input.permission == "acting_admin"
} else := marker_edit_decision(input) if {
	input.permission == "marker_edit"
} else := metadata_curation_decision(input) if {
	input.permission == "metadata_curation"
} else := deny("unknown permission", "unknown_permission")

acting_admin_decision(i) := allow if {
	acting_admin_allowed(i)
} else := deny("user disabled", "user_disabled") if {
	not user_enabled(i)
} else := deny("admin role required", "admin_role_required") if {
	not admin_role(i)
} else := deny("primary profile required", "primary_profile_required")

marker_edit_decision(i) := allow if {
	effective_permission_allowed(i, "marker_edit")
} else := deny("user disabled", "user_disabled") if {
	not user_enabled(i)
} else := deny("marker edit permission required", "marker_edit_permission_required")

metadata_curation_decision(i) := allow if {
	acting_admin_allowed(i)
} else := allow if {
	user_enabled(i)
	assigned_permission(i, "metadata_curation")
	target_libraries_allowed(i)
} else := deny("user disabled", "user_disabled") if {
	not user_enabled(i)
} else := deny("metadata curation permission required", "metadata_curation_permission_required") if {
	not assigned_permission(i, "metadata_curation")
} else := deny("item is outside user libraries", "item_outside_user_libraries")

effective_permission_allowed(i, permission) if {
	user_enabled(i)
	assignable_permission(permission)
	admin_role(i)
} else if {
	user_enabled(i)
	assigned_permission(i, permission)
}

acting_admin_allowed(i) if {
	user_enabled(i)
	admin_role(i)
	declared_profile_id(i) == ""
} else if {
	user_enabled(i)
	admin_role(i)
	declared_profile_id(i) != ""
	acting_as_primary(i)
}

target_libraries_allowed(i) if {
	count(target_library_ids(i)) > 0
	not user_libraries_restricted(i)
} else if {
	count(target_library_ids(i)) > 0
	user_libraries_restricted(i)
	every id in target_library_ids(i) {
		has_value(user_library_ids(i), id)
	}
}

assignable_permission("marker_edit")
assignable_permission("metadata_curation")

assigned_permission(i, permission) if {
	some idx
	assigned_permissions(i)[idx] == permission
}

admin_role(i) if {
	role(i) == "admin"
}

user_enabled(i) if {
	object.get(i, "user_enabled", false) == true
}

acting_as_primary(i) if {
	object.get(i, "acting_as_primary", false) == true
}

user_libraries_restricted(i) if {
	object.get(i, "user_libraries_restricted", false) == true
}

role(i) := object.get(i, "role", "")

declared_profile_id(i) := object.get(i, "declared_profile_id", "")

assigned_permissions(i) := values if {
	values := object.get(i, "assigned_permissions", [])
	is_array(values)
} else := []

target_library_ids(i) := values if {
	values := object.get(i, "target_library_ids", [])
	is_array(values)
} else := []

user_library_ids(i) := values if {
	values := object.get(i, "user_library_ids", [])
	is_array(values)
} else := []

has_value(values, value) if {
	some i
	values[i] == value
}

allow := {
	"allowed": true,
	"reason": "allowed",
	"reason_code": "",
}

# reason is human-readable free text; reason_code is the machine contract Go
# consumers switch on (see policy.ReasonCode* constants).
deny(reason, code) := {
	"allowed": false,
	"reason": reason,
	"reason_code": code,
}

tighten(base, override) := result if {
	allowed := tightened_allowed(base, override)
	reason := tightened_reason(base, override, allowed)
	reason_code := tightened_reason_code(base, override, allowed)
	result := {
		"allowed": allowed,
		"reason": reason,
		"reason_code": reason_code,
	}
}

# Only a literal boolean true keeps the base grant; any other override value
# (including malformed non-boolean output) tightens to a deny.
override_grants(override) if {
	object.get(override, "allowed", true) == true
}

tightened_allowed(base, override) := true if {
	base.allowed
	override_grants(override)
} else := false

tightened_reason(base, override, allowed) := reason if {
	not allowed
	base.allowed
	not override_grants(override)
	reason := nonempty_string_or_default(object.get(override, "reason", ""), base.reason)
} else := base.reason

# Overrides only carry free-text reasons, so a custom deny always gets the
# fixed custom_denial code; a base deny keeps its vendor code.
tightened_reason_code(base, override, allowed) := "custom_denial" if {
	not allowed
	base.allowed
	not override_grants(override)
} else := object.get(base, "reason_code", "")

nonempty_string_or_default(value, fallback) := value if {
	is_string(value)
	value != ""
} else := fallback

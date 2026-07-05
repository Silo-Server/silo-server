package silo.permission

import rego.v1

base_input := {
	"schema_version": 1,
	"user_id": 7,
	"role": "user",
	"user_enabled": true,
	"assigned_permissions": [],
	"permission": "metadata_curation",
	"declared_profile_id": "",
	"acting_as_primary": false,
	"target_library_ids": [1],
	"user_library_ids": [],
	"user_libraries_restricted": false,
	"request_time": "2026-07-02T12:00:00Z",
	"device_id": "device-1",
	"client_ip": "192.0.2.10",
}

test_acting_admin_no_declared_profile if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "acting_admin",
	})
	got.allowed
	got.reason == "allowed"
	got.reason_code == ""
}

test_acting_admin_primary_profile if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "acting_admin",
		"declared_profile_id": "prof-1",
		"acting_as_primary": true,
	})
	got.allowed
}

test_acting_admin_rejects_non_primary_profile if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "acting_admin",
		"declared_profile_id": "prof-2",
		"acting_as_primary": false,
	})
	not got.allowed
	got.reason == "primary profile required"
	got.reason_code == "primary_profile_required"
}

test_acting_admin_rejects_non_admin if {
	got := decision with input as object.union(base_input, {
		"permission": "acting_admin",
	})
	not got.allowed
	got.reason == "admin role required"
	got.reason_code == "admin_role_required"
}

test_acting_admin_rejects_disabled_user if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"user_enabled": false,
		"permission": "acting_admin",
	})
	not got.allowed
	got.reason == "user disabled"
	got.reason_code == "user_disabled"
}

test_marker_edit_admin_implicit_grant if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "marker_edit",
	})
	got.allowed
}

test_marker_edit_assigned_grant if {
	got := decision with input as object.union(base_input, {
		"permission": "marker_edit",
		"assigned_permissions": ["marker_edit"],
	})
	got.allowed
}

test_marker_edit_rejects_missing_permission if {
	got := decision with input as object.union(base_input, {
		"permission": "marker_edit",
	})
	not got.allowed
	got.reason == "marker edit permission required"
	got.reason_code == "marker_edit_permission_required"
}

test_metadata_curation_acting_admin_bypasses_libraries if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "metadata_curation",
		"target_library_ids": [99],
		"user_library_ids": [1],
		"user_libraries_restricted": true,
	})
	got.allowed
}

test_metadata_curation_assigned_unrestricted if {
	got := decision with input as object.union(base_input, {
		"permission": "metadata_curation",
		"assigned_permissions": ["metadata_curation"],
		"user_libraries_restricted": false,
		"target_library_ids": [3, 4],
	})
	got.allowed
}

test_metadata_curation_assigned_in_scope if {
	got := decision with input as object.union(base_input, {
		"permission": "metadata_curation",
		"assigned_permissions": ["metadata_curation"],
		"user_libraries_restricted": true,
		"user_library_ids": [1, 2, 3],
		"target_library_ids": [1, 3],
	})
	got.allowed
}

test_metadata_curation_rejects_out_of_scope if {
	got := decision with input as object.union(base_input, {
		"permission": "metadata_curation",
		"assigned_permissions": ["metadata_curation"],
		"user_libraries_restricted": true,
		"user_library_ids": [1],
		"target_library_ids": [1, 2],
	})
	not got.allowed
	got.reason == "item is outside user libraries"
	got.reason_code == "item_outside_user_libraries"
}

test_metadata_curation_rejects_empty_targets if {
	got := decision with input as object.union(base_input, {
		"permission": "metadata_curation",
		"assigned_permissions": ["metadata_curation"],
		"user_libraries_restricted": false,
		"target_library_ids": [],
	})
	not got.allowed
	got.reason == "item is outside user libraries"
	got.reason_code == "item_outside_user_libraries"
}

test_metadata_curation_non_primary_admin_requires_assigned_permission if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "metadata_curation",
		"declared_profile_id": "prof-2",
		"acting_as_primary": false,
		"assigned_permissions": [],
	})
	not got.allowed
	got.reason == "metadata curation permission required"
	got.reason_code == "metadata_curation_permission_required"
}

test_metadata_curation_non_primary_admin_allows_assigned_permission if {
	got := decision with input as object.union(base_input, {
		"role": "admin",
		"permission": "metadata_curation",
		"declared_profile_id": "prof-2",
		"acting_as_primary": false,
		"assigned_permissions": ["metadata_curation"],
	})
	got.allowed
}

test_unknown_permission_rejected if {
	got := decision with input as object.union(base_input, {
		"permission": "download_all_the_things",
	})
	not got.allowed
	got.reason == "unknown permission"
	got.reason_code == "unknown_permission"
}

deny_override(_, _) := {
	"allowed": false,
	"reason": "quiet hours",
}

test_tightening_override_applies if {
	got := decision
		with input as object.union(base_input, {
			"permission": "marker_edit",
			"assigned_permissions": ["marker_edit"],
		})
		with data.silo_custom.permission.override as deny_override
	not got.allowed
	got.reason == "quiet hours"
	got.reason_code == "custom_denial"
}

allow_override(_, _) := {
	"allowed": true,
	"reason": "custom allow",
}

test_widening_override_has_no_effect if {
	denied_input := object.union(base_input, {
		"permission": "marker_edit",
		"assigned_permissions": [],
	})
	base := decision with input as denied_input
	got := decision
		with input as denied_input
		with data.silo_custom.permission.override as allow_override
	got == base
}

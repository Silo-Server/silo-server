package silo.scope

import rego.v1

base_input := {
	"schema_version": 1,
	"user_id": 7,
	"session_id": "sess-1",
	"profile_id": "",
	"account_library_ids": [],
	"account_restricted": false,
	"account_max_playback_quality": "",
	"access_policy_revision": 11,
	"disabled_library_ids": [],
	"profile_present": false,
	"profile_max_content_rating": "",
	"profile_max_advisory_age": 0,
	"profile_max_playback_quality": "",
	"profile_library_restricted": false,
	"profile_allowed_library_ids": [],
	"profile_has_pin": false,
	"profile_verified": true,
	"profile_preferred_metadata_language": "",
	"request_time": "2026-07-02T12:00:00Z",
	"device_id": "device-1",
	"client_ip": "192.0.2.10",
	"is_api_key": false,
}

test_no_profile_unrestricted if {
	got := decision with input as base_input
	got.unrestricted
	not got.libraries_restricted
	got.allowed_library_ids == []
	got.disabled_library_ids == []
	got.max_content_rating == ""
	got.max_advisory_age == 0
	got.max_playback_quality == ""
	got.policy_revision == 11
	got.profile_verified
}

test_account_restricted if {
	got := decision with input as object.union(base_input, {
		"account_restricted": true,
		"account_library_ids": [3, 1, 3],
		"account_max_playback_quality": "4K",
	})
	not got.unrestricted
	got.libraries_restricted
	got.allowed_library_ids == [3, 1, 3]
	got.disabled_library_ids == []
	got.max_playback_quality == "2160p"
}

test_profile_restricted if {
	got := decision with input as object.union(base_input, {
		"profile_id": "prof-1",
		"profile_present": true,
		"profile_library_restricted": true,
		"profile_allowed_library_ids": [4, 2, 2],
		"profile_max_content_rating": "PG-13",
		"profile_max_playback_quality": "720p",
		"profile_preferred_metadata_language": "es",
	})
	not got.unrestricted
	got.allowed_library_ids == [2, 4]
	got.max_content_rating == "PG-13"
	got.max_playback_quality == "1080p"
	got.preferred_metadata_language == "es"
}

test_account_and_profile_intersection if {
	got := decision with input as object.union(base_input, {
		"account_restricted": true,
		"account_library_ids": [1, 2, 3],
		"profile_id": "prof-1",
		"profile_present": true,
		"profile_library_restricted": true,
		"profile_allowed_library_ids": [4, 3, 2, 2],
	})
	not got.unrestricted
	got.allowed_library_ids == [2, 3]
}

test_disabled_subtracts_when_restricted if {
	got := decision with input as object.union(base_input, {
		"account_restricted": true,
		"account_library_ids": [1, 2, 3],
		"disabled_library_ids": [2],
	})
	not got.unrestricted
	got.allowed_library_ids == [1, 3]
	got.disabled_library_ids == []
}

test_disabled_passes_through_when_unrestricted if {
	got := decision with input as object.union(base_input, {
		"disabled_library_ids": [2],
	})
	got.unrestricted
	got.allowed_library_ids == []
	got.disabled_library_ids == [2]
}

test_unverified_profile_passthrough if {
	got := decision with input as object.union(base_input, {
		"profile_id": "prof-1",
		"profile_present": true,
		"profile_has_pin": true,
		"profile_verified": false,
	})
	not got.profile_verified
}

tightening_override(_, _) := {
	"unrestricted": false,
	"allowed_library_ids": [2, 4],
	"disabled_library_ids": [4],
	"max_content_rating": "PG",
	"max_playback_quality": "1080p",
	"profile_verified": false,
}

test_tightening_override_applies if {
	got := decision
		with input as object.union(base_input, {
			"disabled_library_ids": [3],
		})
		with data.silo_custom.scope.override as tightening_override
	not got.unrestricted
	got.allowed_library_ids == [2]
	got.disabled_library_ids == []
	# The ceiling is reported unreduced in both fields: only Go can rank one
	# rating against another, so the caller resolves the stricter of the two.
	got.max_content_rating == ""
	got.max_content_rating_override == "PG"
	got.max_playback_quality == "1080p"
	not got.profile_verified
}

widening_override(_, _) := {
	"unrestricted": true,
	"allowed_library_ids": [2, 3, 4],
	"disabled_library_ids": [],
	"max_content_rating": "",
	"max_playback_quality": "",
	"profile_verified": true,
}

test_widening_override_has_no_effect if {
	restricted_input := object.union(base_input, {
		"account_restricted": true,
		"account_library_ids": [2, 3],
		"account_max_playback_quality": "1080p",
		"profile_id": "prof-1",
		"profile_present": true,
		"profile_max_content_rating": "PG-13",
		"profile_verified": false,
	})
	base := decision with input as restricted_input
	got := decision
		with input as restricted_input
		with data.silo_custom.scope.override as widening_override
	got == base
}

test_profile_advisory_age_limit if {
	got := decision with input as object.union(base_input, {
		"profile_id": "prof-1",
		"profile_present": true,
		"profile_max_advisory_age": 12,
	})
	got.max_advisory_age == 12
}

# A limit on an absent profile never applies, and an input predating the field
# reads as no limit.
test_profile_advisory_age_needs_a_profile if {
	got := decision with input as object.union(base_input, {"profile_max_advisory_age": 12})
	got.max_advisory_age == 0
	legacy := decision with input as object.remove(object.union(base_input, {"profile_present": true}), {"profile_max_advisory_age"})
	legacy.max_advisory_age == 0
}

advisory_override(age) := {"max_advisory_age": age}

advisory_input := object.union(base_input, {
	"profile_id": "prof-1",
	"profile_present": true,
	"profile_max_advisory_age": 12,
})

# An override reduces the advisory-age limit directly: the lower limit wins,
# a higher or absent one changes nothing, and an unusable one fails closed.
test_advisory_age_override_only_tightens if {
	lower := decision with input as advisory_input with data.silo_custom.scope.override as advisory_override(9)
	lower.max_advisory_age == 9
	fractional := decision with input as advisory_input with data.silo_custom.scope.override as advisory_override(9.7)
	fractional.max_advisory_age == 9
	higher := decision with input as advisory_input with data.silo_custom.scope.override as advisory_override(16)
	higher.max_advisory_age == 12
	zero := decision with input as advisory_input with data.silo_custom.scope.override as advisory_override(0)
	zero.max_advisory_age == 12
	null_override := decision with input as advisory_input with data.silo_custom.scope.override as advisory_override(null)
	null_override.max_advisory_age == 12
	absent := decision with input as advisory_input with data.silo_custom.scope.override as tightening_override
	absent.max_advisory_age == 12
	for_unlimited := decision with input as base_input with data.silo_custom.scope.override as advisory_override(10)
	for_unlimited.max_advisory_age == 10
}

test_unusable_advisory_age_override_fails_closed if {
	every value in ["13", -3, 0.5, true] {
		got := decision with input as advisory_input with data.silo_custom.scope.override as advisory_override(value)
		got.max_advisory_age == 1
	}
}

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
	got.max_content_rating == "PG"
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

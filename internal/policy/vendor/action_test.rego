package silo.action

import rego.v1

base_input := {
	"schema_version": 1,
	"action": "download",
	"user_id": 7,
	"download_allowed": true,
	"download_transcode_allowed": true,
	"max_streams": 2,
	"max_transcodes": 1,
	"downloads_enabled": true,
	"transcode_enabled": true,
	"artifacts_available": true,
	"current_active_streams": 0,
	"current_active_transcodes": 0,
	"requested_action": "direct_play",
	"requested_quality": "original",
	"file_quality": "1080p",
	"max_playback_quality": "",
	"content_rating": "PG",
	"max_content_rating": "",
	"request_time": "2026-07-02T12:00:00Z",
	"device_id": "device-1",
	"client_ip": "192.0.2.10",
}

test_download_allowed if {
	got := decision with input as base_input
	got.allowed
	got.reason == "allowed"
	got.reason_code == ""
	got.quality_ceiling == ""
}

test_download_rejects_disabled_downloads if {
	got := decision with input as object.union(base_input, {
		"downloads_enabled": false,
	})
	not got.allowed
	got.reason == "downloads disabled"
	got.reason_code == "downloads_disabled"
}

test_download_rejects_user_flag if {
	got := decision with input as object.union(base_input, {
		"download_allowed": false,
	})
	not got.allowed
	got.reason == "download permission required"
	got.reason_code == "download_permission_required"
}

test_download_rejects_quality_ceiling if {
	got := decision with input as object.union(base_input, {
		"file_quality": "2160p",
		"max_playback_quality": "1080p",
	})
	not got.allowed
	got.reason == "quality ceiling exceeded"
	got.reason_code == "quality_ceiling_exceeded"
}

test_download_rejects_content_rating if {
	got := decision with input as object.union(base_input, {
		"content_rating": "R",
		"max_content_rating": "PG-13",
	})
	not got.allowed
	got.reason == "content rating exceeded"
	got.reason_code == "content_rating_exceeded"
}

test_download_transcode_allowed if {
	got := decision with input as object.union(base_input, {
		"action": "download_transcode",
		"requested_quality": "5mbps",
	})
	got.allowed
}

test_download_transcode_rejects_server_transcode_flag if {
	got := decision with input as object.union(base_input, {
		"action": "download_transcode",
		"transcode_enabled": false,
	})
	not got.allowed
	got.reason == "transcode disabled"
	got.reason_code == "transcode_disabled"
}

test_download_transcode_rejects_user_transcode_flag if {
	got := decision with input as object.union(base_input, {
		"action": "download_transcode",
		"download_transcode_allowed": false,
	})
	not got.allowed
	got.reason == "download transcode permission required"
	got.reason_code == "download_transcode_permission_required"
}

test_download_transcode_rejects_missing_artifacts if {
	got := decision with input as object.union(base_input, {
		"action": "download_transcode",
		"artifacts_available": false,
	})
	not got.allowed
	got.reason == "download artifacts unavailable"
	got.reason_code == "download_artifacts_unavailable"
}

test_playback_admission_allows_below_limits if {
	got := decision with input as object.union(base_input, {
		"action": "playback_admission",
		"requested_action": "transcode",
		"current_active_streams": 1,
		"current_active_transcodes": 0,
	})
	got.allowed
}

test_playback_admission_zero_limits_unlimited if {
	got := decision with input as object.union(base_input, {
		"action": "playback_admission",
		"requested_action": "transcode",
		"max_streams": 0,
		"max_transcodes": 0,
		"current_active_streams": 99,
		"current_active_transcodes": 99,
	})
	got.allowed
}

test_playback_admission_rejects_stream_limit_at_limit if {
	got := decision with input as object.union(base_input, {
		"action": "playback_admission",
		"requested_action": "direct_play",
		"current_active_streams": 2,
	})
	not got.allowed
	got.reason == "max streams exceeded"
	got.reason_code == "max_streams_exceeded"
}

test_playback_admission_rejects_transcode_limit_at_limit if {
	got := decision with input as object.union(base_input, {
		"action": "playback_admission",
		"requested_action": "transcode",
		"current_active_streams": 1,
		"current_active_transcodes": 1,
	})
	not got.allowed
	got.reason == "max transcodes exceeded"
	got.reason_code == "max_transcodes_exceeded"
}

test_playback_admission_direct_ignores_transcode_limit if {
	got := decision with input as object.union(base_input, {
		"action": "playback_admission",
		"requested_action": "direct_play",
		"current_active_streams": 1,
		"current_active_transcodes": 1,
	})
	got.allowed
}

test_unknown_action_rejected if {
	got := decision with input as object.union(base_input, {
		"action": "teleport",
	})
	not got.allowed
	got.reason == "unknown action"
	got.reason_code == "unknown_action"
}

deny_override(_, _) := {
	"allowed": false,
	"reason": "quiet hours",
}

test_tightening_deny_override_applies if {
	got := decision
		with input as base_input
		with data.silo_custom.action.override as deny_override
	not got.allowed
	got.reason == "quiet hours"
	got.reason_code == "custom_denial"
}

allow_override(_, _) := {
	"allowed": true,
	"reason": "custom allow",
}

test_widening_allow_override_has_no_effect if {
	denied_input := object.union(base_input, {
		"download_allowed": false,
	})
	base := decision with input as denied_input
	got := decision
		with input as denied_input
		with data.silo_custom.action.override as allow_override
	got == base
}

tighten_quality_override(_, _) := {
	"allowed": true,
	"quality_ceiling": "1080p",
}

test_quality_ceiling_override_tightens if {
	got := decision
		with input as object.union(base_input, {
			"max_playback_quality": "2160p",
		})
		with data.silo_custom.action.override as tighten_quality_override
	got.allowed
	got.quality_ceiling == "1080p"
}

widen_quality_override(_, _) := {
	"allowed": true,
	"quality_ceiling": "2160p",
}

test_quality_ceiling_override_cannot_widen if {
	got := decision
		with input as object.union(base_input, {
			"max_playback_quality": "1080p",
		})
		with data.silo_custom.action.override as widen_quality_override
	got.allowed
	got.quality_ceiling == ""
}

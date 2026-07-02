package silo.action

import data.silo.lib.quality
import data.silo.lib.ratings
import rego.v1

decision := tightened if {
	override := data.silo_custom.action.override(base_decision, input)
	tightened := tighten(base_decision, override, input)
} else := base_decision

base_decision := download_decision(input) if {
	action(input) == "download"
} else := download_transcode_decision(input) if {
	action(input) == "download_transcode"
} else := playback_admission_decision(input) if {
	action(input) == "playback_admission"
} else := deny("unknown action")

download_decision(i) := allow if {
	download_base_allowed(i)
	quality_allowed(i)
	rating_allowed(i)
} else := deny("downloads disabled") if {
	not downloads_enabled(i)
} else := deny("download permission required") if {
	not download_allowed(i)
} else := deny("quality ceiling exceeded") if {
	not quality_allowed(i)
} else := deny("content rating exceeded")

download_transcode_decision(i) := allow if {
	download_base_allowed(i)
	transcode_enabled(i)
	download_transcode_allowed(i)
	artifacts_available(i)
	quality_allowed(i)
	rating_allowed(i)
} else := deny("downloads disabled") if {
	not downloads_enabled(i)
} else := deny("download permission required") if {
	not download_allowed(i)
} else := deny("transcode disabled") if {
	not transcode_enabled(i)
} else := deny("download transcode permission required") if {
	not download_transcode_allowed(i)
} else := deny("download artifacts unavailable") if {
	not artifacts_available(i)
} else := deny("quality ceiling exceeded") if {
	not quality_allowed(i)
} else := deny("content rating exceeded")

playback_admission_decision(i) := allow if {
	stream_limit_allows(i)
	transcode_limit_allows(i)
} else := deny("max streams exceeded") if {
	not stream_limit_allows(i)
} else := deny("max transcodes exceeded")

download_base_allowed(i) if {
	downloads_enabled(i)
	download_allowed(i)
}

quality_allowed(i) if {
	quality.allowed(file_quality(i), max_playback_quality(i))
}

rating_allowed(i) if {
	ratings.allowed(content_rating(i), max_content_rating(i))
}

stream_limit_allows(i) if {
	max_streams(i) == 0
} else if {
	current_active_streams(i) < max_streams(i)
}

transcode_limit_allows(i) if {
	requested_action(i) != "transcode"
} else if {
	max_transcodes(i) == 0
} else if {
	current_active_transcodes(i) < max_transcodes(i)
}

action(i) := object.get(i, "action", "")

downloads_enabled(i) if {
	object.get(i, "downloads_enabled", false)
}

transcode_enabled(i) if {
	object.get(i, "transcode_enabled", false)
}

download_allowed(i) if {
	object.get(i, "download_allowed", false)
}

download_transcode_allowed(i) if {
	object.get(i, "download_transcode_allowed", false)
}

artifacts_available(i) if {
	object.get(i, "artifacts_available", false)
}

file_quality(i) := object.get(i, "file_quality", "")

max_playback_quality(i) := object.get(i, "max_playback_quality", "")

content_rating(i) := object.get(i, "content_rating", "")

max_content_rating(i) := object.get(i, "max_content_rating", "")

current_active_streams(i) := object.get(i, "current_active_streams", 0)

current_active_transcodes(i) := object.get(i, "current_active_transcodes", 0)

max_streams(i) := object.get(i, "max_streams", 0)

max_transcodes(i) := object.get(i, "max_transcodes", 0)

requested_action(i) := object.get(i, "requested_action", "")

allow := {
	"allowed": true,
	"reason": "allowed",
	"quality_ceiling": "",
}

deny(reason) := {
	"allowed": false,
	"reason": reason,
	"quality_ceiling": "",
}

tighten(base, override, i) := result if {
	allowed := tightened_allowed(base, override)
	reason := tightened_reason(base, override, allowed)
	quality_ceiling := tightened_quality_ceiling(override, i)
	result := {
		"allowed": allowed,
		"reason": reason,
		"quality_ceiling": quality_ceiling,
	}
}

tightened_allowed(base, override) := true if {
	base.allowed
	object.get(override, "allowed", true)
} else := false

tightened_reason(base, override, allowed) := reason if {
	not allowed
	base.allowed
	not object.get(override, "allowed", true)
	reason := nonempty_string_or_default(object.get(override, "reason", ""), base.reason)
} else := base.reason

tightened_quality_ceiling(override, i) := ceiling if {
	override_ceiling := object.get(override, "quality_ceiling", "")
	override_ceiling != ""
	clamped := quality.min(override_ceiling, max_playback_quality(i))
	clamped != quality.normalize(max_playback_quality(i))
	ceiling := clamped
} else := ""

nonempty_string_or_default(value, fallback) := value if {
	is_string(value)
	value != ""
} else := fallback

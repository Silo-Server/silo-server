package silo.scope

import rego.v1
import data.silo.lib.quality
import data.silo.lib.ratings

decision := tightened if {
	override := data.silo_custom.scope.override(base_decision, input)
	tightened := tighten(base_decision, override)
} else := base_decision

base_decision := decision if {
	libraries := library_decision(input)
	decision := {
		"schema_version": input.schema_version,
		"unrestricted": libraries.unrestricted,
		"allowed_library_ids": libraries.allowed_library_ids,
		"disabled_library_ids": libraries.disabled_library_ids,
		"libraries_restricted": libraries.libraries_restricted,
		"max_content_rating": max_content_rating(input),
		"max_playback_quality": max_playback_quality(input),
		"preferred_metadata_language": preferred_metadata_language(input),
		"policy_revision": input.access_policy_revision,
		"profile_verified": input.profile_verified,
	}
}

library_decision(i) := result if {
	effective := effective_libraries(i)
	effective.unrestricted
	result := {
		"unrestricted": true,
		"allowed_library_ids": [],
		"disabled_library_ids": disabled_ids(i),
		"libraries_restricted": false,
	}
} else := result if {
	effective := effective_libraries(i)
	not effective.unrestricted
	result := {
		"unrestricted": false,
		"allowed_library_ids": subtract(effective.allowed_library_ids, disabled_ids(i)),
		"disabled_library_ids": [],
		"libraries_restricted": true,
	}
}

effective_libraries(i) := result if {
	i.account_restricted
	i.profile_present
	i.profile_library_restricted
	result := {
		"unrestricted": false,
		"allowed_library_ids": intersect(i.account_library_ids, i.profile_allowed_library_ids),
	}
} else := result if {
	i.account_restricted
	not profile_limits_libraries(i)
	result := {
		"unrestricted": false,
		"allowed_library_ids": array_or_empty(i.account_library_ids),
	}
} else := result if {
	not i.account_restricted
	i.profile_present
	i.profile_library_restricted
	result := {
		"unrestricted": false,
		"allowed_library_ids": unique_sorted(i.profile_allowed_library_ids),
	}
} else := {
	"unrestricted": true,
	"allowed_library_ids": [],
}

profile_limits_libraries(i) if {
	i.profile_present
	i.profile_library_restricted
}

max_content_rating(i) := rating if {
	i.profile_present
	rating := i.profile_max_content_rating
} else := ""

max_playback_quality(i) := quality.min(i.account_max_playback_quality, profile_quality(i))

profile_quality(i) := quality if {
	i.profile_present
	quality := i.profile_max_playback_quality
} else := ""

preferred_metadata_language(i) := language if {
	i.profile_present
	language := i.profile_preferred_metadata_language
} else := ""

disabled_ids(i) := ids if {
	is_array(i.disabled_library_ids)
	ids := i.disabled_library_ids
} else := []

array_or_empty(xs) := xs if {
	is_array(xs)
} else := []

intersect(left, right) := unique_sorted([id |
	some i
	id := right[i]
	has_value(left, id)
])

unique_sorted(values) := sort({id |
	some i
	id := values[i]
})

subtract(values, excluded) := [id |
	some i
	id := values[i]
	not has_value(excluded, id)
]

has_value(values, value) if {
	some i
	values[i] == value
}

tighten(base, override) := result if {
	unrestricted := merged_unrestricted(base, override)
	disabled := merged_disabled(base, override)
	allowed := merged_allowed(base, override, unrestricted, disabled)
	libraries_restricted := restricted(unrestricted)
	max_rating := ratings.min(base["max_content_rating"], object.get(override, "max_content_rating", ""))
	max_quality := quality.min(base["max_playback_quality"], object.get(override, "max_playback_quality", ""))
	profile_verified := merged_profile_verified(base, override)
	output_disabled := disabled_if_unrestricted(disabled, unrestricted)
	result := {
		"schema_version": base["schema_version"],
		"unrestricted": unrestricted,
		"allowed_library_ids": allowed,
		"disabled_library_ids": output_disabled,
		"libraries_restricted": libraries_restricted,
		"max_content_rating": max_rating,
		"max_playback_quality": max_quality,
		"preferred_metadata_language": base["preferred_metadata_language"],
		"policy_revision": base["policy_revision"],
		"profile_verified": profile_verified,
	}
}

restricted(unrestricted) := false if {
	unrestricted
} else := true

merged_unrestricted(base, override) := true if {
	base.unrestricted
	object.get(override, "unrestricted", base.unrestricted)
} else := false

merged_profile_verified(base, override) := true if {
	base.profile_verified
	object.get(override, "profile_verified", true)
} else := false

merged_disabled(base, override) := unique_sorted(array.concat(
	object.get(base, "disabled_library_ids", []),
	object.get(override, "disabled_library_ids", []),
))

merged_allowed(base, override, unrestricted, disabled) := [] if {
	unrestricted
} else := allowed if {
	not unrestricted
	base.unrestricted
	not object.get(override, "unrestricted", base.unrestricted)
	allowed := subtract(object.get(override, "allowed_library_ids", []), disabled)
} else := allowed if {
	not unrestricted
	not base.unrestricted
	object.get(override, "unrestricted", base.unrestricted)
	allowed := subtract(base.allowed_library_ids, disabled)
} else := allowed if {
	not unrestricted
	not base.unrestricted
	not object.get(override, "unrestricted", base.unrestricted)
	allowed := subtract(intersect(base.allowed_library_ids, object.get(override, "allowed_library_ids", [])), disabled)
}

disabled_if_unrestricted(disabled, unrestricted) := disabled if {
	unrestricted
} else := []

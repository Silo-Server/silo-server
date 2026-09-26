package silo.scope

import rego.v1
import data.silo.lib.quality

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
		"max_content_rating_override": "",
		"max_advisory_age": max_advisory_age(input),
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

# max_advisory_age is the profile's advisory-age limit; 0 means no limit.
max_advisory_age(i) := age if {
	i.profile_present
	age := object.get(i, "profile_max_advisory_age", 0)
	is_number(age)
	age > 0
} else := 0

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

# tighten combines the base decision with a custom override. The advisory-age
# limit is reduced to the lower of the two (see stricter_advisory_age). Every
# other dimension is reduced here too except the content rating: comparing "PG" with "15" or "FSK 16"
# needs the maturity ladder, which lives in Go only (internal/access). The
# override's ceiling is reported alongside the base one and the caller resolves
# the stricter of the two with access.StricterCeiling, so an override that names
# a rating this policy cannot rank can only tighten, never widen.
tighten(base, override) := result if {
	unrestricted := merged_unrestricted(base, override)
	disabled := merged_disabled(base, override)
	allowed := merged_allowed(base, override, unrestricted, disabled)
	libraries_restricted := restricted(unrestricted)
	max_quality := quality.min(base["max_playback_quality"], object.get(override, "max_playback_quality", ""))
	profile_verified := merged_profile_verified(base, override)
	output_disabled := disabled_if_unrestricted(disabled, unrestricted)
	result := {
		"schema_version": base["schema_version"],
		"unrestricted": unrestricted,
		"allowed_library_ids": allowed,
		"disabled_library_ids": output_disabled,
		"libraries_restricted": libraries_restricted,
		"max_content_rating": base["max_content_rating"],
		"max_content_rating_override": object.get(override, "max_content_rating", ""),
		"max_advisory_age": stricter_advisory_age(base["max_advisory_age"], override_advisory_age(override)),
		"max_playback_quality": max_quality,
		"preferred_metadata_language": base["preferred_metadata_language"],
		"policy_revision": base["policy_revision"],
		"profile_verified": profile_verified,
	}
}

# override_advisory_age reads the advisory-age limit an override asks for. An
# absent, null or zero value asks for none. A positive number is floored to a
# whole age. Anything else (a negative number, a string, a boolean) is an
# unusable limit and, like an unusable parental control anywhere else, fails
# closed to the strictest limit there is.
override_advisory_age(o) := 0 if {
	object.get(o, "max_advisory_age", null) == null
} else := 0 if {
	o.max_advisory_age == 0
} else := age if {
	is_number(o.max_advisory_age)
	o.max_advisory_age >= 1
	age := floor(o.max_advisory_age)
} else := 1

# stricter_advisory_age keeps the lower of two advisory-age limits, where 0
# means "no limit" and so always loses to a real one.
stricter_advisory_age(a, b) := b if {
	a == 0
} else := a if {
	b == 0
} else := min([a, b])

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

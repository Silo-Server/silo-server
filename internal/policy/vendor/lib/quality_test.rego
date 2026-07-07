package silo.lib.quality

import rego.v1

test_allowed_denies_4320p_under_2160p if {
	not allowed("4320P", "2160p")
}

test_allowed_permits_480p_under_1080p if {
	allowed("480P", "1080p")
}

test_allowed_permits_720p_without_ceiling if {
	allowed("720P", "")
}

test_allowed_permits_unknown_under_1080p if {
	allowed("unknown", "1080p")
}

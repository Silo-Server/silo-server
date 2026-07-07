package silo.lib.quality

import rego.v1

rank := {
	"": 0,
	"480P": 1,
	"720P": 2,
	"1080P": 3,
	"2160P": 4,
	"4320P": 5,
}

normalize(value) := normalized if {
	u := upper(trim(sprintf("%v", [value]), " "))
	u == ""
	normalized := ""
} else := normalized if {
	u := upper(trim(sprintf("%v", [value]), " "))
	u == "ANY"
	normalized := ""
} else := normalized if {
	u := upper(trim(sprintf("%v", [value]), " "))
	u in {"STANDARD", "480P", "720P", "1080P"}
	normalized := "1080p"
} else := normalized if {
	u := upper(trim(sprintf("%v", [value]), " "))
	u in {"4K", "UHD", "2160P", "4320P"}
	normalized := "2160p"
} else := ""

compare(a, b) := -1 if {
	rank[upper(trim(normalize(a), " "))] < rank[upper(trim(normalize(b), " "))]
} else := 1 if {
	rank[upper(trim(normalize(a), " "))] > rank[upper(trim(normalize(b), " "))]
} else := 0

allowed(file_quality, ceiling) if {
	normalize(ceiling) == ""
} else if {
	file_rank := object.get(rank, raw(file_quality), 0)
	ceiling_rank := object.get(rank, upper(trim(normalize(ceiling), " ")), 0)
	file_rank <= ceiling_rank
}

raw(value) := upper(trim(sprintf("%v", [value]), " "))

min(a, b) := result if {
	normalized_a := normalize(a)
	trim(normalized_a, " ") == ""
	result := normalize(b)
} else := result if {
	normalized_b := normalize(b)
	trim(normalized_b, " ") == ""
	result := normalize(a)
} else := result if {
	compare(a, b) <= 0
	result := normalize(a)
} else := normalize(b)

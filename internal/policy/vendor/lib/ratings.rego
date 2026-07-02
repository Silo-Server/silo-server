package silo.lib.ratings

import rego.v1

rank := {
	"G": 0,
	"TV-Y": 0,
	"TV-G": 0,
	"PG": 1,
	"TV-Y7": 1,
	"TV-PG": 1,
	"PG-13": 2,
	"TV-14": 2,
	"R": 3,
	"NC-17": 3,
	"TV-MA": 3,
}

normalize(value) := upper(trim(sprintf("%v", [value]), " "))

allowed(rating, ceiling) if {
	normalize(ceiling) == ""
} else if {
	rating_value := rank[normalize(rating)]
	ceiling_value := rank[normalize(ceiling)]
	rating_value <= ceiling_value
}

min(a, b) := result if {
	normalize(a) == ""
	result := normalize(b)
} else := result if {
	normalize(b) == ""
	result := normalize(a)
} else := result if {
	not rank[normalize(a)]
	result := normalize(a)
} else := result if {
	not rank[normalize(b)]
	result := normalize(b)
} else := result if {
	rank[normalize(a)] <= rank[normalize(b)]
	result := normalize(a)
} else := normalize(b)

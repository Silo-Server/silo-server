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
	"R": 4,
	"NC-17": 4,
	"TV-MA": 4,
	"AU-G": 0,
	"AU-PG": 1,
	"AU-M": 2,
	"AU-MA15+": 3,
	"AU-R18+": 4,
	"AU-X18+": 5,
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

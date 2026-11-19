package catalog

import (
	"regexp"
)

// placeholderRE matches Postgres positional parameter placeholders ($1, $2, ...)
// for use in arg-count assertions across countSQL tests.
var placeholderRE = regexp.MustCompile(`\$(\d+)`)

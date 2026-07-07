package pathscope

import (
	"path/filepath"
	"strings"
)

var likeEscaper = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

// EscapeLike escapes LIKE wildcards in a literal fragment for use with
// ESCAPE '\' queries.
func EscapeLike(literal string) string {
	return likeEscaper.Replace(literal)
}

func PrefixLike(pathPrefix string) string {
	clean := filepath.Clean(pathPrefix)
	if clean == string(filepath.Separator) {
		return likeEscaper.Replace(clean) + "%"
	}
	return likeEscaper.Replace(clean) + string(filepath.Separator) + "%"
}

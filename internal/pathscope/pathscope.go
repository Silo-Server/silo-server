package pathscope

import (
	"fmt"
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

// CoverageClauses builds one SQL predicate per root matching rows whose
// column value lies at or under that root, using the exact-match + escaped
// prefix-LIKE shape (`column = $n OR column LIKE $n+1 ESCAPE '\'`). The
// prefix pattern ends with a path separator, so a sibling root that merely
// shares a string prefix (/mnt/movies2 vs /mnt/movies) never matches.
// Placeholder numbering starts at firstArg; the returned args bind pairwise
// (root, prefix pattern) in clause order.
func CoverageClauses(column string, roots []string, firstArg int) ([]string, []any) {
	clauses := make([]string, 0, len(roots))
	args := make([]any, 0, len(roots)*2)
	for i, root := range roots {
		pathArg := firstArg + i*2
		clauses = append(clauses, fmt.Sprintf(`(%s = $%d OR %s LIKE $%d ESCAPE '\')`, column, pathArg, column, pathArg+1))
		args = append(args, root, PrefixLike(root))
	}
	return clauses, args
}

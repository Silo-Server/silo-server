package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// scopeLibraryRoot returns the most specific configured library root that
// contains path, or "" when none does.
func scopeLibraryRoot(path string, libraryRoots []string) string {
	root := ""
	for _, candidate := range libraryRoots {
		candidate = filepath.Clean(candidate)
		if pathWithinAnyRoot(path, []string{candidate}) && len(candidate) > len(root) {
			root = candidate
		}
	}
	return root
}

// scanRootIgnoreRules loads the rules above a scoped folder walk, stopping at
// its configured library root. Rules outside that root cannot affect a library.
// A failed ancestor read makes the scope incomplete, so callers must protect it
// from missing-file reconciliation.
func scanRootIgnoreRules(path string, libraryRoots []string) ([]ignoreRules, bool, error) {
	path = filepath.Clean(path)
	root := scopeLibraryRoot(path, libraryRoots)
	if root == "" || root == path {
		return nil, false, nil
	}
	var parents []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		parents = append(parents, dir)
		if dir == root {
			break
		}
	}
	var rules []ignoreRules
	for i := len(parents) - 1; i >= 0; i-- {
		dir := parents[i]
		if ignoreRulesMatch(rules, dir, true) {
			return rules, true, nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, false, fmt.Errorf("read ignore ancestor %s: %w", dir, err)
		}
		var skip bool
		rules, skip = dirIgnoreRules(rules, dir, dir, entries)
		if skip {
			return rules, true, nil
		}
	}
	// A scope is a directory unless the filesystem says otherwise.
	isDir := true
	if info, err := os.Stat(path); err == nil {
		isDir = info.IsDir()
	}
	return rules, ignoreRulesMatch(rules, path, isDir), nil
}

// libraryRootSkipped reports whether root's own ignore files exclude the whole
// root. It checks only the two marker names instead of listing the root, which
// can hold many thousands of entries.
func libraryRootSkipped(root string) bool {
	entries := make([]fs.DirEntry, 0, 2)
	for _, name := range []string{ignoreFileName, ignoreMarkerNoMedia} {
		if info, err := os.Lstat(filepath.Join(root, name)); err == nil {
			entries = append(entries, fs.FileInfoToDirEntry(info))
		}
	}
	_, skip := dirIgnoreRules(nil, root, root, entries)
	return skip
}

// Filesystem ignore conventions honored during scans:
//
//   - .nomedia: a directory containing this marker file is skipped entirely,
//     together with everything under it.
//   - .ignore: Jellyfin semantics. An empty file, or one without a valid
//     pattern, skips its directory like .nomedia. Otherwise each line is a
//     gitignore pattern matched against paths relative to the file's
//     directory, and only matching entries are skipped.
//   - .siloignore: per-directory glob file with Plex .plexignore semantics —
//     patterns are matched against paths relative to the directory holding the
//     file, apply to that directory and every descendant, nested files stack
//     with inherited ones, and a pattern matching a directory name prunes the
//     whole subtree.

const (
	ignoreFileName      = ".ignore"
	ignoreMarkerNoMedia = ".nomedia"
	siloIgnoreFileName  = ".siloignore"
)

// ignoreRules is one parsed ignore file: the logical path of the directory it
// lives in plus its patterns. A .siloignore holds glob patterns
// (filepath.Match semantics, so `*` matches within one path segment and `/`
// is literal); an .ignore holds gitignore patterns.
type ignoreRules struct {
	basePath    string
	patterns    []string
	gitPatterns []gitIgnorePattern
}

// gitIgnorePattern is one parsed .ignore line.
type gitIgnorePattern struct {
	segments []string // slash-separated glob segments; "**" spans any depth
	negate   bool     // "!" re-includes a path an earlier pattern excluded
	dirOnly  bool     // a trailing "/" matches directories only
	anchored bool     // a "/" before the end matches from the file's directory
}

// dirIgnoreRules applies a directory's own ignore files. skip reports that the
// directory is excluded entirely, together with everything under it: it holds
// .nomedia, or an .ignore without a valid pattern. Otherwise rules is the rule
// set its children inherit. Only regular files count as ignore files, and an
// unreadable one is treated as absent.
func dirIgnoreRules(inherited []ignoreRules, dirLogicalPath, dirPhysicalPath string, entries []fs.DirEntry) (rules []ignoreRules, skip bool) {
	var hasIgnore, hasSiloIgnore bool
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		switch entry.Name() {
		case ignoreMarkerNoMedia:
			return inherited, true
		case ignoreFileName:
			hasIgnore = true
		case siloIgnoreFileName:
			hasSiloIgnore = true
		}
	}
	rules = inherited
	if hasIgnore {
		if content, err := os.ReadFile(filepath.Join(dirPhysicalPath, ignoreFileName)); err == nil {
			patterns := parseGitIgnorePatterns(string(content))
			if len(patterns) == 0 {
				return inherited, true
			}
			rules = append(rules, ignoreRules{basePath: dirLogicalPath, gitPatterns: patterns})
		}
	}
	if hasSiloIgnore {
		if content, err := os.ReadFile(filepath.Join(dirPhysicalPath, siloIgnoreFileName)); err == nil {
			if patterns := parseIgnorePatterns(string(content)); len(patterns) > 0 {
				rules = append(rules, ignoreRules{basePath: dirLogicalPath, patterns: patterns})
			}
		}
	}
	return rules, false
}

// parseIgnorePatterns converts .siloignore file content into match patterns.
// Blank lines and `#` comments are dropped, the rest is kept verbatim.
func parseIgnorePatterns(content string) []string {
	var patterns []string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// parseGitIgnorePatterns converts .ignore file content into gitignore
// patterns. Like Jellyfin, it trims each line and drops invalid patterns; the
// caller treats a file left with none as a whole-directory marker.
func parseGitIgnorePatterns(content string) []gitIgnorePattern {
	var patterns []gitIgnorePattern
	for _, line := range parseIgnorePatterns(content) {
		var pattern gitIgnorePattern
		if rest, ok := strings.CutPrefix(line, "!"); ok {
			pattern.negate = true
			line = rest
		}
		if rest, ok := strings.CutSuffix(line, "/"); ok {
			pattern.dirOnly = true
			line = rest
		}
		pattern.anchored = strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		pattern.segments = strings.Split(line, "/")
		valid := true
		for i, segment := range pattern.segments {
			if segment == "**" {
				continue
			}
			translated, ok := gitGlobSegment(segment)
			if _, err := path.Match(translated, ""); !ok || err != nil || segment == "" {
				valid = false
				break
			}
			pattern.segments[i] = translated
		}
		if valid {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

// posixClasses maps the POSIX character classes gitignore accepts inside a
// bracket expression to path.Match ranges.
var posixClasses = map[string]string{
	"alnum":  "0-9A-Za-z",
	"alpha":  "A-Za-z",
	"blank":  " \t",
	"cntrl":  "\x00-\x1f\x7f",
	"digit":  "0-9",
	"graph":  `\!-\~`,
	"lower":  "a-z",
	"print":  ` -\~`,
	"punct":  "\\!-\\/\\:-\\@\\[-\\`\\{-\\~",
	"space":  " \t\n\v\f\r",
	"upper":  "A-Z",
	"xdigit": "0-9A-Fa-f",
}

// gitGlobSegment rewrites one gitignore glob segment into path.Match syntax.
// Only bracket expressions differ: gitignore also negates with "!", accepts
// POSIX classes such as [:digit:], and reads a leading "]" and a leading or
// trailing "-" literally. ok is false for a malformed bracket expression.
func gitGlobSegment(segment string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(segment); i++ {
		switch segment[i] {
		case '\\':
			b.WriteString(segment[i:min(i+2, len(segment))])
			i++
		case '[':
			class, n, ok := gitBracketExpression(segment[i:])
			if !ok {
				return "", false
			}
			b.WriteString(class)
			i += n - 1
		default:
			b.WriteByte(segment[i])
		}
	}
	return b.String(), true
}

// gitBracketExpression translates the bracket expression at the start of s and
// returns it with the number of bytes of s it consumed.
func gitBracketExpression(s string) (string, int, bool) {
	var b strings.Builder
	b.WriteByte('[')
	i := 1
	if i < len(s) && (s[i] == '!' || s[i] == '^') {
		b.WriteByte('^')
		i++
	}
	for first := true; i < len(s); first = false {
		switch c := s[i]; {
		case c == ']' && !first:
			b.WriteByte(']')
			return b.String(), i + 1, true
		case c == ']' || (c == '-' && (first || (i+1 < len(s) && s[i+1] == ']'))):
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		case c == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i += 2
		case strings.HasPrefix(s[i:], "[:"):
			end := strings.Index(s[i+2:], ":]")
			if end < 0 {
				return "", 0, false
			}
			ranges, ok := posixClasses[s[i+2:i+2+end]]
			if !ok {
				return "", 0, false
			}
			b.WriteString(ranges)
			i += end + 4
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", 0, false
}

// match reports whether the pattern matches rel, a slash-separated path
// relative to the .ignore file's directory. An unanchored pattern matches the
// entry's own name at any depth.
func (p gitIgnorePattern) match(rel []string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if !p.anchored {
		return matchGlobSegments(p.segments, rel[len(rel)-1:])
	}
	return matchGlobSegments(p.segments, rel)
}

// matchGlobSegments matches path segments against glob segments, where "**"
// matches zero or more whole segments. A trailing "**" matches only entries
// strictly inside the preceding directory.
func matchGlobSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			if len(rest) == 0 {
				return len(name) > 0
			}
			for i := range len(name) + 1 {
				if matchGlobSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pattern[0], name[0]); !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// ignoreRulesMatch reports whether logicalPath matches the inherited rules.
// Patterns apply to the entry's own path relative to each rule's directory;
// matching directories are pruned by the caller, which is what excludes whole
// subtrees. Any .siloignore match excludes the entry. Among .ignore patterns
// the last match wins, so a deeper file or a later "!" line can re-include it.
func ignoreRulesMatch(rules []ignoreRules, logicalPath string, isDir bool) bool {
	gitIgnored := false
	for _, rule := range rules {
		rel, err := filepath.Rel(rule.basePath, logicalPath)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		for _, pattern := range rule.patterns {
			if ok, _ := filepath.Match(pattern, rel); ok {
				return true
			}
		}
		if len(rule.gitPatterns) == 0 {
			continue
		}
		segments := strings.Split(filepath.ToSlash(rel), "/")
		for _, pattern := range rule.gitPatterns {
			if pattern.match(segments, isDir) {
				gitIgnored = !pattern.negate
			}
		}
	}
	return gitIgnored
}

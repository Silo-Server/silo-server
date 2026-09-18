package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Filesystem ignore conventions honored during scans:
//
//   - .ignore / .nomedia: a directory containing either marker file is skipped
//     entirely, together with everything under it. No pattern semantics.
//   - .siloignore: per-directory glob file with Plex .plexignore semantics —
//     patterns are matched against paths relative to the directory holding the
//     file, apply to that directory and every descendant, nested files stack
//     with inherited ones, and a pattern matching a directory name prunes the
//     whole subtree.

const (
	ignoreMarkerIgnore  = ".ignore"
	ignoreMarkerNoMedia = ".nomedia"
	siloIgnoreFileName  = ".siloignore"
)

// ignoreRules is one parsed .siloignore: the logical path of the directory it
// lives in plus its glob patterns (filepath.Match semantics, so `*` matches
// within one path segment and `/` is literal).
type ignoreRules struct {
	basePath string
	patterns []string
}

// dirHasIgnoreMarker reports whether the entries contain an ignore marker
// file. Only regular files count: a directory or symlink named .ignore or
// .nomedia is not a marker.
func dirHasIgnoreMarker(entries []fs.DirEntry) bool {
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if entry.Name() == ignoreMarkerIgnore || entry.Name() == ignoreMarkerNoMedia {
			return true
		}
	}
	return false
}

// parseIgnorePatterns converts .siloignore file content into match patterns.
// Blank lines and `#` comments are dropped, the rest is kept verbatim.
func parseIgnorePatterns(content string) []string {
	var patterns []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// readSiloIgnoreFile reads and parses the .siloignore in dir, if any. A
// missing file yields nil; a read failure yields nil too and lets the scan
// continue — a broken ignore file must not abort a library walk.
func readSiloIgnoreFile(dir string) []string {
	content, err := os.ReadFile(filepath.Join(dir, siloIgnoreFileName))
	if err != nil {
		return nil
	}
	return parseIgnorePatterns(string(content))
}

// ignoreRulesMatch reports whether logicalPath matches any inherited rule.
// Patterns apply to the entry's own path relative to the rule's directory;
// matching directories are pruned by the caller, which is what excludes whole
// subtrees.
func ignoreRulesMatch(rules []ignoreRules, logicalPath string) bool {
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
	}
	return false
}

// childIgnoreRules returns the rule set children of a directory inherit: the
// inherited rules plus this directory's own .siloignore, if present. Only a
// regular file counts as the pattern file.
func childIgnoreRules(inherited []ignoreRules, dirLogicalPath, dirPhysicalPath string, entries []fs.DirEntry) []ignoreRules {
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() != siloIgnoreFileName {
			continue
		}
		patterns := readSiloIgnoreFile(dirPhysicalPath)
		if len(patterns) == 0 {
			return inherited
		}
		return append(inherited, ignoreRules{basePath: dirLogicalPath, patterns: patterns})
	}
	return inherited
}

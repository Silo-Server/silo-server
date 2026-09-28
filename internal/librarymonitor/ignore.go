package librarymonitor

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ignoredNamePatterns is the fixed ignore list, matched case-insensitively
// against a file or directory name before anything is tracked: download and
// copy temp suffixes plus NAS housekeeping folders (the CephFS source's list
// with common downloader suffixes added). Patterns are lower case.
var ignoredNamePatterns = []string{
	"*.partial",
	// Sonarr and Radarr copy imports under this suffix, then rename.
	"*.partial~",
	"*.part",
	"*.tmp",
	"*.!qb",
	"@eadir",
	"#recycle",
	".recyclebin",
	".trash-*",
	"lost+found",
}

// ignoredDirNames holds the rest of the scanner's skipped directory names
// (the others are in ignoredNamePatterns), so the monitor records no
// directory the scanner never enters.
var ignoredDirNames = map[string]bool{
	"@recycle":     true,
	".trash":       true,
	"$recycle.bin": true,
	".deleted":     true,
	".inbound":     true,
	".downloads":   true,
}

// Ignore-file names honored by the walk. See
// docs/architecture/scanner-ignore-files.md.
const (
	markerNoMedia  = ".nomedia"
	ignoreFileName = ".ignore"
)

// ignoreMarker reports whether name is an ignore file that can exclude its
// directory (see dirSkipped).
func ignoreMarker(name string) bool {
	return name == markerNoMedia || name == ignoreFileName
}

// ignoredName reports whether a file or directory name matches the fixed
// ignore list.
func ignoredName(name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range ignoredNamePatterns {
		if ok, _ := path.Match(pattern, lower); ok {
			return true
		}
	}
	return false
}

// ignoredDir reports whether a directory with this name is never recorded.
func ignoredDir(name string) bool {
	return ignoredName(name) || ignoredDirNames[strings.ToLower(name)]
}

// dirSkipped reports whether dir's own ignore files exclude it and everything
// under it, as the scanner decides: a regular .nomedia file, or a regular
// .ignore file without a pattern (empty, or only blank and comment lines).
// An unreadable .ignore is treated as absent, like the scanner does.
//
// Pattern rules inside .ignore and .siloignore are not applied: the scanner's
// matcher is internal to its package. Watching a pattern-ignored folder only
// costs a watch; a change there resolves to a scan the scanner then filters.
func dirSkipped(dir string, entries []fs.DirEntry) bool {
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		switch entry.Name() {
		case markerNoMedia:
			return true
		case ignoreFileName:
			content, err := os.ReadFile(filepath.Join(dir, ignoreFileName))
			if err == nil && !hasIgnorePattern(string(content)) {
				return true
			}
		}
	}
	return false
}

func hasIgnorePattern(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}
	return false
}

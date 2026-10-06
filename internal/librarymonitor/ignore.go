package librarymonitor

import (
	"io/fs"
	"path"
	"strings"

	"github.com/Silo-Server/silo-server/internal/scanner"
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
	markerNoMedia      = ".nomedia"
	ignoreFileName     = ".ignore"
	siloIgnoreFileName = ".siloignore"
)

// ignoreFile reports whether name is a file that sets ignore rules for its
// directory: .nomedia and a marker .ignore exclude it (see dirSkipped), and
// the patterns in .ignore and .siloignore exclude entries in it. Such files
// are never reported as changes themselves; a change to one re-checks and
// rescans its directory.
func ignoreFile(name string) bool {
	return name == markerNoMedia || name == ignoreFileName || name == siloIgnoreFileName
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

// dirSkipped reports whether dir's own ignore files exclude it and
// everything under it, decided by the scanner (scanner.DirSkipped).
//
// Pattern rules inside .ignore and .siloignore are not applied: watching a
// pattern-ignored folder only costs a watch; a change there resolves to a
// scan the scanner then filters.
func dirSkipped(dir string, entries []fs.DirEntry) bool {
	return scanner.DirSkipped(dir, entries)
}

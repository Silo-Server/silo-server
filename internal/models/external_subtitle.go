package models

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// FileName is the sidecar's file name.
func (s ExternalSubtitle) FileName() string {
	return fspath.Display(filepath.Base(s.Path))
}

// TitleIsFileName reports whether the sidecar has no title of its own: none
// is stored, or the stored one is its file name, as the scanner records.
func (s ExternalSubtitle) TitleIsFileName() bool {
	return IsFileNameTitle(s.Title, s.FileName())
}

// IsFileNameTitle reports whether title says nothing beyond fileName: it is
// empty, or the file name itself. Both are compared without surrounding
// space, so a file name that starts or ends with a space still matches.
func IsFileNameTitle(title, fileName string) bool {
	title = strings.TrimSpace(title)
	return title == "" || title == strings.TrimSpace(fileName)
}

// SidecarLabels labels the external subtitles that a viewer who may not see
// file names knows only by language. The first with a given language, format
// and flags is labeled by its language, the next "en 2", and so on, so a
// choice saved between two of them still tells them apart. The catalog and the
// playback inventory number the same sidecars in the same order, so a label
// saved from one matches the other.
type SidecarLabels map[string]int

// Next returns the label of the next such sidecar.
func (l SidecarLabels) Next(language, format string, forced, hearingImpaired bool) string {
	key := strings.Join([]string{language, format, strconv.FormatBool(forced), strconv.FormatBool(hearingImpaired)}, "|")
	l[key]++
	if n := l[key]; n > 1 {
		return strings.TrimSpace(language + " " + strconv.Itoa(n))
	}
	return language
}

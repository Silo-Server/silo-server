package naming

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// displayPaths lets the path heuristics read an fspath-encoded path as
// display text, so an escaped "å" counts as a letter exactly like a UTF-8 one,
// and maps the paths they derive back to the encoded form.
//
// Display turns each escaped code point into exactly one code point and never
// produces '/' or '.', so a path derived from the display text that is a
// prefix of it has the same rune count as the matching encoded prefix.
type displayPaths struct {
	file    string
	roots   []string
	encoded []string
	display []string
}

// newDisplayPaths returns nil when neither the file path nor a library root
// holds escaped bytes; callers then parse the paths as they are.
func newDisplayPaths(filePath string, libraryRoots []string) *displayPaths {
	encoded := fspath.IsEncoded(filePath)
	for _, root := range libraryRoots {
		encoded = encoded || fspath.IsEncoded(root)
	}
	if !encoded {
		return nil
	}
	d := &displayPaths{file: fspath.Display(filePath), roots: make([]string, len(libraryRoots))}
	d.add(filePath)
	d.add(filepath.Clean(filePath))
	for i, root := range libraryRoots {
		d.roots[i] = fspath.Display(root)
		if root != "" {
			d.add(filepath.Clean(root))
		}
	}
	return d
}

func (d *displayPaths) add(encoded string) {
	d.encoded = append(d.encoded, encoded)
	d.display = append(d.display, fspath.Display(encoded))
}

// toEncoded returns the encoded path for a path derived from the display
// text. A path that is not a prefix of the file or a root is returned as is.
func (d *displayPaths) toEncoded(displayPath string) string {
	if displayPath == "" {
		return displayPath
	}
	for i, display := range d.display {
		if !strings.HasPrefix(display, displayPath) {
			continue
		}
		encoded := d.encoded[i]
		n := utf8.RuneCountInString(displayPath)
		for offset := range encoded {
			if n == 0 {
				return encoded[:offset]
			}
			n--
		}
		return encoded
	}
	return displayPath
}

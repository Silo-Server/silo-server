package trickplay

import (
	"strconv"
	"strings"
)

// keyRoot holds every sheet: trickplay/<media_files.id>/<revision>/.
const keyRoot = "trickplay/"

// Manifest describes a file's published sheets.
type Manifest struct {
	FileID   int
	Revision int64
	// Width and Height are a thumbnail's size in pixels.
	Width, Height int
	// TileColumns by TileRows thumbnails make a sheet; every sheet has the
	// full grid, black after the last thumbnail.
	TileColumns, TileRows int
	// IntervalMS is the time each thumbnail covers: thumbnail i shows
	// [i*IntervalMS, (i+1)*IntervalMS).
	IntervalMS     int
	ThumbnailCount int
	SheetCount     int
	// Bandwidth is Jellyfin's bits-per-second estimate (see
	// Recipe.Bandwidth).
	Bandwidth int
}

// revisionPrefix is the storage prefix of one revision of a file's sheets.
func revisionPrefix(fileID int, revision int64) string {
	return keyRoot + strconv.Itoa(fileID) + "/" + strconv.FormatInt(revision, 10) + "/"
}

// SheetKey is the storage key of sheet index. The revision repeats in the
// file name so artworkkey.Revision reads it: the artwork route then serves
// the sheet as immutable, and signed URLs stay stable for a day.
func SheetKey(fileID int, revision int64, index int) string {
	rev := strconv.FormatInt(revision, 10)
	return revisionPrefix(fileID, revision) + strconv.Itoa(index) + "." + rev + ".jpg"
}

// SheetKey is the storage key of the manifest's sheet index.
func (m Manifest) SheetKey(index int) string {
	return SheetKey(m.FileID, m.Revision, index)
}

// IsKey reports whether key is a trickplay sheet key.
func IsKey(key string) bool {
	return strings.HasPrefix(key, keyRoot)
}

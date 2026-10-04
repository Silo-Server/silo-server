package scanner

import (
	"path/filepath"
	"strings"
)

// videoExtensions is the set of file extensions the movie and series scanner
// branches catalog. Every decision about whether a path is a video file goes
// through this set (SupportsVideoFile, the library walk, ScanFile, extras
// classification, and scantrigger), so they cannot disagree.
//
// Each entry is a container ffprobe identifies and the playback planner can
// direct play, remux, or transcode; playback.MimeFromExtension names a media
// type for every one of them.
var videoExtensions = extensionSet(
	".mkv .webm",                    // Matroska; WebM is its VP8/VP9/AV1 subset
	".mp4 .m4v .mov .3gp .3g2 .f4v", // MP4/QuickTime family
	".avi .divx",                    // AVI; .divx is AVI with a DivX stream
	".ts .m2ts .mts",                // MPEG-TS; .m2ts/.mts use 192-byte BDAV packets
	".mpg .mpeg",                    // MPEG program stream
	".wmv .asf",                     // ASF
	".flv",                          // Flash video
	".ogv .ogm",                     // Ogg video and legacy Ogg Media
)

// extensionSet builds a set from groups of space-separated extensions.
func extensionSet(groups ...string) map[string]bool {
	set := make(map[string]bool)
	for _, group := range groups {
		for _, ext := range strings.Fields(group) {
			set[ext] = true
		}
	}
	return set
}

// Reasons a file that looks like video is not cataloged. They are stored with
// each skipped file and are part of the admin API, so they must not change.
const (
	UnsupportedReasonDVDVOB     = "dvd_vob"
	UnsupportedReasonDiscImage  = "disc_image"
	UnsupportedReasonRealMedia  = "realmedia"
	UnsupportedReasonDiscStream = "disc_stream"
)

// unsupportedVideoExtensions names video formats the scanner recognizes but
// deliberately does not catalog, with the reason a skipped file is reported
// under.
var unsupportedVideoExtensions = map[string]string{
	// A DVD's VIDEO_TS folder splits each title into 1 GB VOB parts next to
	// menu VOBs. Cataloging VOBs one by one would turn every part and menu
	// into its own item.
	".vob": UnsupportedReasonDVDVOB,
	".iso": UnsupportedReasonDiscImage,
	// RealMedia seeks poorly and its codecs need a full transcode for every
	// client.
	".rm":   UnsupportedReasonRealMedia,
	".rmvb": UnsupportedReasonRealMedia,
}

// unsupportedReasonMessages explains each reason to an admin.
var unsupportedReasonMessages = map[string]string{
	UnsupportedReasonDVDVOB:    "DVD VOB files are not cataloged; remux the DVD title to a single file",
	UnsupportedReasonDiscImage: "Disc images are not cataloged; remux the disc title to a single file",
	UnsupportedReasonRealMedia: "RealMedia files are not cataloged",
	// Streams inside a Blu-ray or AVCHD disc folder.
	UnsupportedReasonDiscStream: "Blu-ray and AVCHD disc folders (BDMV/STREAM) are not cataloged; remux the disc title to a single file",
}

// UnsupportedReasonMessage returns the admin-facing explanation of an
// unsupported-file reason, or "" for an unknown one.
func UnsupportedReasonMessage(reason string) string {
	return unsupportedReasonMessages[reason]
}

// UnsupportedFile is a file that looks like video but that the scanner does
// not catalog, such as a DVD VOB or a RealMedia file.
type UnsupportedFile struct {
	Path string
	// Reason is one of the UnsupportedReason constants.
	Reason string
}

// SupportsVideoFile reports whether the given path is a video file the
// scanner catalogs: it uses a recognized video extension and is not a stream
// inside a disc folder structure.
func SupportsVideoFile(filePath string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(filePath))] && !inDiscStreamDir(filePath)
}

// unsupportedVideoFileReason returns the reason a file that looks like video
// is not cataloged, as one of the UnsupportedReason constants. It returns ""
// for supported video files and for files that do not look like video at all.
func unsupportedVideoFileReason(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if videoExtensions[ext] {
		if inDiscStreamDir(filePath) {
			return UnsupportedReasonDiscStream
		}
		return ""
	}
	return unsupportedVideoExtensions[ext]
}

// inDiscStreamDir reports whether filePath sits directly in a BDMV/STREAM
// directory. A Blu-ray or AVCHD disc folder stores the main feature as
// several .m2ts clips alongside menus and extras; only the disc's playlists
// say how they fit together, so cataloging the clips one by one would create
// an item per clip.
func inDiscStreamDir(filePath string) bool {
	dir := filepath.Dir(filePath)
	return strings.EqualFold(filepath.Base(dir), "STREAM") &&
		strings.EqualFold(filepath.Base(filepath.Dir(dir)), "BDMV")
}

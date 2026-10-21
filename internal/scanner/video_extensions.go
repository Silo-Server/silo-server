package scanner

import (
	"path/filepath"
	"slices"
	"strings"
)

// videoContainers lists the containers the movie and series scanner branches
// catalog: each group's file extensions, the containers a probe of such a file
// names (detectContainer's names), and whether the container's header lists
// every stream the file holds.
//
// Each entry is a container ffprobe identifies and the playback planner can
// direct play, remux, or transcode; playback.MimeFromExtension names a media
// type for every one of them.
var videoContainers = []videoContainer{
	{".mkv .webm", []string{containerMKV}, true},                         // Matroska; WebM is its VP8/VP9/AV1 subset
	{".mp4 .m4v .mov .3gp .3g2 .f4v", []string{containerMP4}, true},      // MP4/QuickTime family
	{".avi .divx", []string{containerAVI}, true},                         // AVI; .divx is AVI with a DivX stream
	{".ts .m2ts .mts", []string{containerMPEGTS, containerBDAV}, false},  // MPEG-TS; .m2ts/.mts use 192-byte BDAV packets
	{".mpg .mpeg", []string{containerMPEGPS, containerMPEGVideo}, false}, // MPEG program stream, or a bare MPEG video stream
	{".wmv .asf", []string{containerASF}, true},                          // ASF
	{".flv", []string{containerFLV}, false},                              // Flash video
	{".ogv .ogm", []string{containerOgg}, false},                         // Ogg video and legacy Ogg Media
}

type videoContainer struct {
	extensions string
	containers []string
	// listsStreams is set when the header declares every stream, so a probe
	// that finds no video stream shows the file has none. Containers that
	// announce streams as their packets arrive can hold video past the part
	// ffprobe reads.
	listsStreams bool
}

// videoExtensions is the set of file extensions the movie and series scanner
// branches catalog. Every decision about whether a path is a video file goes
// through this set (SupportsVideoFile, the library walk, ScanFile, extras
// classification, and scantrigger), so they cannot disagree.
var videoExtensions = func() map[string]bool {
	set := make(map[string]bool)
	for _, group := range videoContainers {
		for _, ext := range strings.Fields(group.extensions) {
			set[ext] = true
		}
	}
	return set
}()

// extensionAllowsContainer reports whether filePath's extension allows a
// probe's container: false only for a video extension whose group does not
// list that container.
func extensionAllowsContainer(filePath, container string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	for _, group := range videoContainers {
		if slices.Contains(strings.Fields(group.extensions), ext) {
			return slices.Contains(group.containers, container)
		}
	}
	return true
}

// containerListsStreams reports whether container's header declares every
// stream the file holds.
func containerListsStreams(container string) bool {
	for _, group := range videoContainers {
		if slices.Contains(group.containers, container) {
			return group.listsStreams
		}
	}
	return false
}

// unsupportedVideoExtensions names video formats the scanner recognizes but
// deliberately does not catalog, with the reason a skipped file is logged
// under.
var unsupportedVideoExtensions = map[string]string{
	// A DVD's VIDEO_TS folder splits each title into 1 GB VOB parts next to
	// menu VOBs. Cataloging VOBs one by one would turn every part and menu
	// into its own item.
	".vob": "DVD VOB files are not cataloged; remux the DVD title to a single file",
	".iso": "disc images are not cataloged; remux the disc title to a single file",
	// RealMedia seeks poorly and its codecs need a full transcode for every
	// client.
	".rm":   "RealMedia files are not cataloged",
	".rmvb": "RealMedia files are not cataloged",
}

// discStructureSkipReason is logged for streams inside a Blu-ray or AVCHD
// disc folder.
const discStructureSkipReason = "Blu-ray and AVCHD disc folders (BDMV/STREAM) are not cataloged; remux the disc title to a single file"

// SupportsVideoFile reports whether the given path is a video file the
// scanner catalogs: it uses a recognized video extension and is not a stream
// inside a disc folder structure.
func SupportsVideoFile(filePath string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(filePath))] && !inDiscStreamDir(filePath)
}

// unsupportedVideoFileReason explains why a file that looks like video is not
// cataloged. It returns "" for supported video files and for files that do
// not look like video at all.
func unsupportedVideoFileReason(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if videoExtensions[ext] {
		if inDiscStreamDir(filePath) {
			return discStructureSkipReason
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

// Package mediacontainer lists the media containers Silo catalogs: the file
// extensions the scanner admits as video and audio, and the FFmpeg demuxers
// that read them. The scanner decides what a library file is from these
// lists, and a transcode node that reads its sources through the core admits
// exactly these demuxers on a relayed input (nodesource), so the two cannot
// disagree.
package mediacontainer

import (
	"maps"
	"slices"
	"strings"
)

// videoExtensions are the extensions the movie and series scanner branches
// catalog. Each is a container ffprobe identifies and the playback planner
// can direct play, remux, or transcode.
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

// audioExtensions are the extensions the audiobook and podcast scanner
// branches recognize.
var audioExtensions = extensionSet(".m4b .m4a .mp3 .flac .opus .ogg .wav .aac")

// FFmpeg demuxer names. FFmpeg matches a demuxer by any of its names, so
// "mov" admits the mov,mp4,m4a,3gp,3g2,mj2 demuxer and "matroska" the
// matroska,webm one.
const (
	demuxerMatroska  = "matroska"
	demuxerMOV       = "mov"
	demuxerM4V       = "m4v"
	demuxerAVI       = "avi"
	demuxerMPEGTS    = "mpegts"
	demuxerMPEGPS    = "mpeg"
	demuxerMPEGVideo = "mpegvideo"
	demuxerASF       = "asf"
	demuxerFLV       = "flv"
	demuxerOgg       = "ogg"
	demuxerMP3       = "mp3"
	demuxerFLAC      = "flac"
	demuxerWAV       = "wav"
	demuxerAAC       = "aac"
)

// demuxers names, for each cataloged extension, every FFmpeg demuxer that
// may read such a file: the container's own, and for the extensions whose
// files are known to hold something else (a bare MPEG-4 or MPEG video stream
// in .m4v, .mpg or .mpeg, an MPEG-TS under an MPEG extension) the demuxers of
// those too.
var demuxers = func() map[string][]string {
	byExtension := map[string][]string{}
	for extensions, names := range map[string][]string{
		".mkv .webm":                         {demuxerMatroska},
		".mp4 .mov .3gp .3g2 .f4v .m4b .m4a": {demuxerMOV},
		".m4v":                               {demuxerMOV, demuxerM4V},
		".avi .divx":                         {demuxerAVI},
		".ts .m2ts .mts":                     {demuxerMPEGTS},
		".mpg .mpeg":                         {demuxerMPEGPS, demuxerMPEGTS, demuxerMPEGVideo},
		".wmv .asf":                          {demuxerASF},
		".flv":                               {demuxerFLV},
		".ogv .ogm .opus .ogg":               {demuxerOgg},
		".mp3":                               {demuxerMP3},
		".flac":                              {demuxerFLAC},
		".wav":                               {demuxerWAV},
		".aac":                               {demuxerAAC},
	} {
		for _, ext := range strings.Fields(extensions) {
			byExtension[ext] = names
		}
	}
	return byExtension
}()

func extensionSet(groups ...string) map[string]bool {
	set := make(map[string]bool)
	for _, group := range groups {
		for _, ext := range strings.Fields(group) {
			set[ext] = true
		}
	}
	return set
}

// VideoExtensions returns the cataloged video extensions, lowercase with the
// dot, as a set the caller owns.
func VideoExtensions() map[string]bool { return maps.Clone(videoExtensions) }

// AudioExtensions returns the cataloged audio extensions, lowercase with the
// dot, as a set the caller owns.
func AudioExtensions() map[string]bool { return maps.Clone(audioExtensions) }

// Demuxers returns, sorted and without duplicates, every FFmpeg demuxer that
// may read a cataloged video or audio file.
func Demuxers() []string {
	var names []string
	for ext := range videoExtensions {
		names = append(names, demuxers[ext]...)
	}
	for ext := range audioExtensions {
		names = append(names, demuxers[ext]...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

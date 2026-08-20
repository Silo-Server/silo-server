// Package playback provides play method resolution, streaming, transcoding,
// and session management for Silo.
package playback

import (
	"slices"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// PlayMethod represents how a media file will be streamed.
type PlayMethod string

const (
	PlayDirect    PlayMethod = "direct"
	PlayRemux     PlayMethod = "remux"
	PlayTranscode PlayMethod = "transcode"
)

// ClientCapabilities describes what the client can play natively.
//
// AudioPassthroughCodecs are codecs the connected audio sink can decode bit-
// exact (e.g. an HDMI AVR accepting EAC3/Atmos). They are treated as supported
// audio codecs for resolution purposes so we can stream-copy surround audio
// instead of downmixing+re-encoding to AAC. Distinct from CodecsAudio, which
// describes what the client itself can decode.
type ClientCapabilities struct {
	CodecsVideo            []string `json:"codecs_video"` // e.g., h264, hevc, av1
	CodecsAudio            []string `json:"codecs_audio"` // e.g., aac, opus, flac
	AudioPassthroughCodecs []string `json:"audio_passthrough_codecs,omitempty"`
	Containers             []string `json:"containers"`     // e.g., mp4, webm, mkv
	MaxResolution          string   `json:"max_resolution"` // e.g., 1080p, 2160p
	HDR                    bool     `json:"hdr"`
}

// AdminSettings controls server-side playback constraints.
type AdminSettings struct {
	TranscodeEnabled bool
	Allow4KTranscode bool
}

// PlayDecision is the result of resolving how to play a file.
type PlayDecision struct {
	Method         PlayMethod
	File           *models.MediaFile
	Reason         string // human-readable explanation
	TranscodeAudio bool   // true when remuxing should transcode audio to AAC
}

// Resolve determines the play method for a given file and client capabilities.
// Returns direct if client supports codec+container, remux if codec matches
// but container doesn't, transcode otherwise.
func Resolve(file *models.MediaFile, caps ClientCapabilities, settings AdminSettings) *PlayDecision {
	// Check if client supports the video codec.
	videoOK := containsStr(caps.CodecsVideo, file.CodecVideo)
	// Audio is considered OK if the client can decode the codec itself OR its
	// sink can passthrough it. Passthrough lets us stream-copy surround audio
	// (EAC3/AC3/DTS/TrueHD) to HDMI AVRs instead of re-encoding to stereo AAC.
	audioOK := containsStr(caps.CodecsAudio, file.CodecAudio) ||
		containsStr(caps.AudioPassthroughCodecs, file.CodecAudio)
	// Check if client supports the container.
	containerOK := containsStr(caps.Containers, file.Container)

	// Check resolution constraint.
	if !resolutionFits(file.Resolution, caps.MaxResolution) {
		if !settings.TranscodeEnabled {
			return &PlayDecision{
				Method: PlayDirect,
				File:   file,
				Reason: "file resolution exceeds client max but transcode disabled; attempting direct",
			}
		}
		// Need transcode to lower resolution.
		return &PlayDecision{
			Method: PlayTranscode,
			File:   file,
			Reason: "file resolution exceeds client max resolution",
		}
	}

	// Case 1: Client supports codec + container → direct play.
	if videoOK && audioOK && containerOK {
		return &PlayDecision{
			Method: PlayDirect,
			File:   file,
			Reason: "client supports all codecs and container",
		}
	}

	// A copy-unsafe source (H.264 with conflicting in-band PPS) cannot take a
	// video stream-copy route: the remux would desync strict decoders. Force it
	// past the remux cases into a full video transcode. Direct play of the
	// original file (Case 1) stays available — decoders that reparse in-band
	// parameter sets handle the original container fine.
	copyUnsafe := videoCopyUnsafeFile(file)

	// Case 2: Client supports codecs but not container → remux.
	if videoOK && audioOK && !containerOK && !copyUnsafe {
		return &PlayDecision{
			Method: PlayRemux,
			File:   file,
			Reason: "client supports codecs but not container; remuxing",
		}
	}

	// Case 3: Video OK but audio codec unsupported → remux with audio transcode.
	// This is much cheaper than a full video transcode.
	if videoOK && !audioOK && !copyUnsafe {
		return &PlayDecision{
			Method:         PlayRemux,
			File:           file,
			TranscodeAudio: true,
			Reason:         "client supports video codec but not audio; remuxing with audio transcode to AAC",
		}
	}

	// Case 4: Client can't play video codec → full transcode.
	if !settings.TranscodeEnabled {
		return &PlayDecision{
			Method: PlayDirect,
			File:   file,
			Reason: "transcode needed but disabled; attempting direct play",
		}
	}

	return &PlayDecision{
		Method: PlayTranscode,
		File:   file,
		Reason: "client cannot play video codec; transcoding",
	}
}

// resolutionOrder returns a numeric value for sorting resolutions.
func resolutionOrder(res string) int {
	switch {
	case access.CompareQuality(res, "4320p") == 0:
		return 5
	case access.CompareQuality(res, "2160p") == 0:
		return 4
	case access.CompareQuality(res, "1080p") == 0:
		return 3
	case access.CompareQuality(res, "720p") == 0:
		return 2
	case access.CompareQuality(res, "480p") == 0:
		return 1
	default:
		return 0
	}
}

// resolutionFits checks if the file resolution fits within the client's max.
func resolutionFits(fileRes, maxRes string) bool {
	if maxRes == "" {
		return true // no constraint
	}
	return resolutionOrder(fileRes) <= resolutionOrder(maxRes)
}

// containsStr checks if a slice contains a string.
func containsStr(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

// videoCopyUnsafeFile reports whether the file's video stream cannot be safely
// stream-copied into an avc1/fMP4 segment. It is set once by the multi-PPS
// bitstream scan (H.264 sources that redefine a pic_parameter_set_id in-band
// with conflicting content). Scan failures also disable copy for the current
// decision while remaining eligible for retry on a later request.
func videoCopyUnsafeFile(file *models.MediaFile) bool {
	if file == nil || len(file.VideoTracks) == 0 {
		return false
	}
	track := file.VideoTracks[0]
	return track.VideoCopyUnsafe || (track.MultiplePPS != nil && *track.MultiplePPS)
}

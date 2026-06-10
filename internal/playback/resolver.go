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

type VersionSelectionFilter struct {
	EditionKey           string
	PresentationKind     string
	PresentationGroupKey string
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

	// Case 2: Client supports codecs but not container → remux.
	if videoOK && audioOK && !containerOK {
		return &PlayDecision{
			Method: PlayRemux,
			File:   file,
			Reason: "client supports codecs but not container; remuxing",
		}
	}

	// Case 3: Video OK but audio codec unsupported → remux with audio transcode.
	// This is much cheaper than a full video transcode.
	if videoOK && !audioOK {
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

// SelectVersion chooses the best file version from a list of available files
// based on client capabilities and admin settings.
// Priority: direct-playable > remux > transcode, then highest quality, then smallest file.
func SelectVersion(files []*models.MediaFile, caps ClientCapabilities, settings AdminSettings) (*PlayDecision, error) {
	return SelectVersionFiltered(files, caps, settings, VersionSelectionFilter{})
}

// SelectVersionFiltered chooses the best interchangeable file version within
// one edition/presentation group.
func SelectVersionFiltered(
	files []*models.MediaFile,
	caps ClientCapabilities,
	settings AdminSettings,
	filter VersionSelectionFilter,
) (*PlayDecision, error) {
	if len(files) == 0 {
		return nil, ErrNoVersions
	}

	candidates := files
	if filter.EditionKey != "" || filter.PresentationKind != "" || filter.PresentationGroupKey != "" {
		filtered := make([]*models.MediaFile, 0, len(files))
		for _, f := range files {
			if filter.EditionKey != "" && f.EditionKey != filter.EditionKey {
				continue
			}
			if filter.PresentationKind != "" && f.PresentationKind != filter.PresentationKind {
				continue
			}
			if filter.PresentationGroupKey != "" && f.PresentationGroupKey != filter.PresentationGroupKey {
				continue
			}
			filtered = append(filtered, f)
		}
		if len(filtered) > 0 {
			candidates = filtered
		}
	}

	var directFiles, remuxFiles, transcodeFiles []*PlayDecision

	for _, f := range candidates {
		// Filter by client max resolution.
		if !resolutionFits(f.Resolution, caps.MaxResolution) {
			// If 4K transcoding disabled and this is 4K, skip entirely.
			if is4K(f.Resolution) && !settings.Allow4KTranscode {
				continue
			}
		}

		decision := Resolve(f, caps, settings)

		switch decision.Method {
		case PlayDirect:
			directFiles = append(directFiles, decision)
		case PlayRemux:
			remuxFiles = append(remuxFiles, decision)
		case PlayTranscode:
			transcodeFiles = append(transcodeFiles, decision)
		}
	}

	// Prefer direct > remux > transcode.
	if len(directFiles) > 0 {
		return bestQuality(directFiles), nil
	}
	if len(remuxFiles) > 0 {
		return bestQuality(remuxFiles), nil
	}
	if len(transcodeFiles) > 0 {
		return bestQuality(transcodeFiles), nil
	}

	// Fallback: use first file with direct play.
	return &PlayDecision{
		Method: PlayDirect,
		File:   candidates[0],
		Reason: "no compatible version found; falling back to first file",
	}, nil
}

// bestQuality picks the highest quality file. Among ties, picks smallest file.
func bestQuality(decisions []*PlayDecision) *PlayDecision {
	best := decisions[0]
	for _, d := range decisions[1:] {
		if access.CompareQuality(d.File.Resolution, best.File.Resolution) > 0 {
			best = d
		} else if access.CompareQuality(d.File.Resolution, best.File.Resolution) == 0 && d.File.FileSize < best.File.FileSize {
			best = d
		}
	}
	return best
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

// is4K returns true if the resolution is 2160p or higher.
func is4K(res string) bool {
	return access.CompareQuality(res, "2160p") >= 0
}

// containsStr checks if a slice contains a string.
func containsStr(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

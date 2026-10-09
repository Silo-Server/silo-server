package playback

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
)

// PreparedTracksRecipeVersion identifies the prepared-file stream layout that
// carries every source audio track and every text subtitle track. Artifacts
// without it are legacy single-audio, subtitle-free files.
const PreparedTracksRecipeVersion = "1"

// Prepared audio track codecs.
const (
	PreparedAudioCopy = codecCopyV3
	PreparedAudioAAC  = audioCodecAACV3
)

const (
	audioCodecMP3       = "mp3"
	audioCodecAC3       = "ac3"
	audioCodecEAC3      = "eac3"
	subtitleCodecTextV3 = "text"
)

// PreparedAudioTrack is one audio stream of a prepared download, in output
// order. SourceIndex is the ffmpeg audio ordinal (0:a:N), which is also the
// position in models.MediaFile.AudioTracks.
type PreparedAudioTrack struct {
	SourceIndex int `json:"source_index"`
	// Codec is PreparedAudioCopy or PreparedAudioAAC.
	Codec string `json:"codec"`
	// SourceChannels selects the stereo downmix policy for an AAC encode.
	SourceChannels int    `json:"source_channels,omitempty"`
	Language       string `json:"language,omitempty"`
	Title          string `json:"title,omitempty"`
	Default        bool   `json:"default,omitempty"`
}

// PreparedSubtitleTrack is one embedded text subtitle converted to MP4 timed
// text (mov_text). SourceIndex is the ffmpeg subtitle ordinal (0:s:N), which is
// also the position in models.MediaFile.SubtitleTracks.
type PreparedSubtitleTrack struct {
	SourceIndex     int    `json:"source_index"`
	Language        string `json:"language,omitempty"`
	Title           string `json:"title,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearing_impaired,omitempty"`
}

// PreparedTracks is the frozen stream layout of a prepared download. It is part
// of the transported recipe, so every field affects the artifact's execution
// fingerprint.
type PreparedTracks struct {
	Audio     []PreparedAudioTrack    `json:"audio"`
	Subtitles []PreparedSubtitleTrack `json:"subtitles,omitempty"`
}

// PreparedTracksAvailable reports whether file's probed audio inventory can
// drive the multi-track layout. Without one (a failed or legacy probe) the
// plan would map no audio, so such files keep the legacy layout, whose
// optional 0:a:0? mapping needs no inventory.
func PreparedTracksAvailable(file *models.MediaFile) bool {
	return file != nil && len(file.AudioTracks) > 0
}

// PlanPreparedTracks derives the stream layout of a prepared download from the
// probed source. Every audio track is kept in source order. With a "copy"
// audio target a track is copied when the MP4 muxer accepts its codec and it
// either shares the primary track's codec (which client negotiation verified)
// or is universally decodable, otherwise it is encoded to AAC; an "aac" target
// encodes every track. audioTrackIndex
// marks the default track, falling back to the source default and then the
// first track. Embedded plain-text subtitles become mov_text. ASS/SSA keep
// their styling, typesetting, and overlapping events only as sidecars, and
// bitmap subtitles cannot be stored in MP4, so both are left to sidecar
// delivery (PreparedSubtitleSidecarFormat).
func PlanPreparedTracks(file *models.MediaFile, targetCodecAudio string, audioTrackIndex int) *PreparedTracks {
	plan := &PreparedTracks{Audio: []PreparedAudioTrack{}}
	if file == nil {
		return plan
	}
	copyAudio := strings.EqualFold(strings.TrimSpace(targetCodecAudio), PreparedAudioCopy)
	primaryCodec := normalizeCodecV3(file.CodecAudio)
	if primaryCodec == "" && len(file.AudioTracks) > 0 {
		primaryCodec = normalizeCodecV3(file.AudioTracks[0].Codec)
	}
	defaultIndex := preparedDefaultAudioIndex(file.AudioTracks, audioTrackIndex)
	for i, track := range file.AudioTracks {
		codec := PreparedAudioAAC
		if copyAudio && preparedAudioCopyable(track.Codec, primaryCodec) {
			codec = PreparedAudioCopy
		}
		sourceChannels := 0
		if codec == PreparedAudioAAC {
			sourceChannels = track.Channels
		}
		plan.Audio = append(plan.Audio, PreparedAudioTrack{
			SourceIndex:    i,
			Codec:          codec,
			SourceChannels: sourceChannels,
			Language:       track.Language,
			Title:          track.EmbeddedTitle,
			Default:        i == defaultIndex,
		})
	}
	for i, track := range file.SubtitleTracks {
		if track.External || !PreparedSubtitleEmbeddable(track.Codec) {
			continue
		}
		plan.Subtitles = append(plan.Subtitles, PreparedSubtitleTrack{
			SourceIndex:     i,
			Language:        track.Language,
			Title:           track.EmbeddedTitle,
			Default:         track.Default,
			Forced:          track.Forced,
			HearingImpaired: track.HearingImpaired,
		})
	}
	return plan
}

func (p *PreparedTracks) allAudioCopied() bool {
	for _, track := range p.Audio {
		if track.Codec != PreparedAudioCopy {
			return false
		}
	}
	return true
}

func preparedDefaultAudioIndex(tracks []models.AudioTrack, requested int) int {
	if requested >= 0 && requested < len(tracks) {
		return requested
	}
	for i, track := range tracks {
		if track.Default {
			return i
		}
	}
	return 0
}

// preparedMP4AudioCodecs lists audio codecs FFmpeg's MP4 muxer stores without
// experimental flags. Client negotiation checks decode support, not the
// container, so a passthrough codec such as TrueHD, DTS, or PCM is encoded.
var preparedMP4AudioCodecs = map[string]bool{
	audioCodecAACV3: true,
	audioCodecMP3:   true,
	audioCodecAC3:   true,
	audioCodecEAC3:  true,
	"alac":          true,
}

// preparedAudioCopyable reports whether an audio track may be stream-copied
// into the prepared MP4 under a "copy" audio target.
func preparedAudioCopyable(codec, primaryCodec string) bool {
	normalized := normalizeCodecV3(codec)
	if !preparedMP4AudioCodecs[normalized] {
		return false
	}
	return normalized == primaryCodec || normalized == audioCodecAACV3 || normalized == audioCodecMP3
}

// preparedTextSubtitleCodecs lists embedded plain-text subtitle codecs that
// convert to MP4 timed text without losing content.
var preparedTextSubtitleCodecs = map[string]bool{
	subtitleCodecSubRip:  true,
	subtitleFormatSRT:    true,
	subtitleMuxerWebVTT:  true,
	subtitleCodecMovText: true,
	subtitleCodecTextV3:  true,
}

// PreparedSubtitleEmbeddable reports whether an embedded subtitle codec is
// carried inside a prepared MP4 download as timed text.
func PreparedSubtitleEmbeddable(codec string) bool {
	return preparedTextSubtitleCodecs[normalizeCodecV3(codec)]
}

// PreparedSubtitleSidecarFormat returns the sidecar file format ("ass" or
// "sup") StreamExtractSubtitle produces for an embedded subtitle a prepared
// MP4 cannot carry faithfully, or "" when the track is embedded in the MP4 or
// not delivered. ASS/SSA would lose styling, drawing commands, and overlapping
// events as MP4 timed text; PGS has no MP4 representation.
func PreparedSubtitleSidecarFormat(codec string) string {
	if _, format := streamExtractOutput(codec); format == subtitleFormatASS || format == subtitleFormatSUP {
		return format
	}
	return ""
}

// appendPreparedTrackArgs maps and encodes every stream of a prepared-file
// plan. The prepared output clears source metadata, so each stream's language,
// title (MP4 handler name), and disposition are written explicitly.
func appendPreparedTrackArgs(args []string, opts TranscodeOpts) []string {
	plan := opts.PreparedTracks
	args = append(args, "-map", "0:v:0")
	for _, track := range plan.Audio {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", track.SourceIndex))
	}
	for _, track := range plan.Subtitles {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", track.SourceIndex))
	}
	args = append(args, "-dn")

	for i, track := range plan.Audio {
		stream := strconv.Itoa(i)
		if track.Codec == PreparedAudioCopy {
			args = append(args, "-c:a:"+stream, "copy")
		} else {
			channels, bitrateKbps := ResolveAACOutputV3(0, 0)
			filter := aacTimestampNormalizeFilterV3
			if IsAudioToAACStereoDownmixV3(track.SourceChannels, PreparedAudioAAC, 0) {
				filter = stereoDownmixBoostFilterV3
			}
			args = append(args,
				"-c:a:"+stream, audioCodecAACV3,
				"-b:a:"+stream, strconv.Itoa(bitrateKbps)+"k",
				"-ac:a:"+stream, strconv.Itoa(channels),
				"-filter:a:"+stream, filter,
			)
		}
		args = appendPreparedStreamMetadata(args, "a:"+stream, track.Language, track.Title)
		args = append(args, "-disposition:a:"+stream, preparedDisposition(track.Default, false, false))
	}
	for i, track := range plan.Subtitles {
		stream := strconv.Itoa(i)
		args = append(args, "-c:s:"+stream, subtitleCodecMovText)
		args = appendPreparedStreamMetadata(args, "s:"+stream, track.Language, track.Title)
		args = append(args, "-disposition:s:"+stream, preparedDisposition(track.Default, track.Forced, track.HearingImpaired))
	}
	return args
}

// appendPreparedStreamMetadata writes an MP4-compatible language (ISO 639-2)
// and title. The MP4 muxer stores a track title only as its handler name.
func appendPreparedStreamMetadata(args []string, stream, language, title string) []string {
	if code := lang.ISO6392(language); code != "" {
		args = append(args, "-metadata:s:"+stream, "language="+code)
	}
	if title = strings.TrimSpace(title); title != "" {
		args = append(args, "-metadata:s:"+stream, "handler_name="+title)
	}
	return args
}

func preparedDisposition(isDefault, forced, hearingImpaired bool) string {
	var flags []string
	if isDefault {
		flags = append(flags, "default")
	}
	if forced {
		flags = append(flags, "forced")
	}
	if hearingImpaired {
		flags = append(flags, "hearing_impaired")
	}
	if len(flags) == 0 {
		return "0"
	}
	return strings.Join(flags, "+")
}

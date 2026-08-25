package playback

import (
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// RecipeCard is the small, durable "recipe" needed to reconstruct a transcode
// session after the server forgets its in-memory state (e.g. a restart). It
// captures session identity, ownership, and the full set of encode parameters
// that affect output bytes — everything required to re-spawn an equivalent
// ffmpeg seeked to any requested segment.
//
// It deliberately omits non-serializable runtime fields (the ffmpeg process,
// context, channels, log sink). Those are re-wired on reconstruct from the
// live config and request.
type RecipeCard struct {
	SessionID            string    `json:"session_id"`
	UserID               int       `json:"user_id"`
	ProfileID            string    `json:"profile_id"`
	MediaFileID          int       `json:"media_file_id"`
	TranscodeNodeURL     string    `json:"transcode_node_url,omitempty"`
	TranscodeTransportID string    `json:"transcode_transport_id,omitempty"`
	OriginalStartedAt    time.Time `json:"original_started_at,omitempty"`

	// PlayMethod discriminates which serve path reconstructs this session
	// (direct / remux / transcode). Empty decodes as PlayTranscode for
	// back-compat with cards written before direct/remux were reconstructable.
	PlayMethod PlayMethod `json:"play_method,omitempty"`
	// TranscodeAudio mirrors Session.TranscodeAudio; used by the remux path to
	// re-spawn ffmpeg with the same audio handling on reconstruct, and by the
	// admin activity views to classify the reconstructed session (audio
	// re-encode vs repackage).
	TranscodeAudio bool        `json:"transcode_audio,omitempty"`
	RemuxDVMode    RemuxDVMode `json:"remux_dv_mode,omitempty"`

	// Client metadata mirrored from the session so admin views (client label,
	// Jellyfin pill) survive reconstruction. Carried only by stored cards —
	// deliberately NOT projected into stream-token claims, where client metadata
	// would bloat every stream URL.
	ClientName       string `json:"client_name,omitempty"`
	ClientVersion    string `json:"client_version,omitempty"`
	ClientBuild      string `json:"client_build,omitempty"`
	ClientChannel    string `json:"client_channel,omitempty"`
	ClientUserAgent  string `json:"client_user_agent,omitempty"`
	IsJellyfinCompat bool   `json:"is_jellyfin_compat,omitempty"`

	// Encode parameters — mirror of the byte-affecting TranscodeOpts fields.
	// Direct cards leave them zero; remux cards use the audio targets when the
	// selected stream must be converted.
	InputPath    string `json:"input_path"`
	OutputSubdir string `json:"output_subdir,omitempty"`
	// DVProfile and AudioOnly are source facts the catalog owns and a remote
	// executor cannot look up for itself: the remux needs the Dolby Vision
	// profile to strip a dangling Profile 7 RPU, and the audio-only flag to keep
	// the content type the plan promised. They ride the card so a proxy serving
	// this session from a grant produces the same bytes the API would have.
	DVProfile                  int                    `json:"dv_profile,omitempty"`
	AudioOnly                  bool                   `json:"audio_only,omitempty"`
	SourceVideoCodec           string                 `json:"source_video_codec,omitempty"`
	SourceVideoProfile         string                 `json:"source_video_profile,omitempty"`
	SourceVideoBitDepth        int                    `json:"source_video_bit_depth,omitempty"`
	SoftwareVideoDecode        bool                   `json:"software_video_decode,omitempty"`
	ToneMapPolicy              tonemap.Policy         `json:"tone_map_policy,omitempty"`
	ToneMapMode                tonemap.Mode           `json:"tone_map_mode,omitempty"`
	ToneMapSourceKind          tonemap.SourceKind     `json:"tone_map_source_kind,omitempty"`
	ToneMapFilter              string                 `json:"tone_map_filter,omitempty"`
	ToneMapRecipeVersion       string                 `json:"tone_map_recipe_version,omitempty"`
	ToneMapPreflightRequired   bool                   `json:"tone_map_preflight_required,omitempty"`
	ToneMapSourceRevision      tonemap.SourceRevision `json:"tone_map_source_revision,omitzero"`
	ToneMapDVConfigPresent     bool                   `json:"tone_map_dv_config_present,omitempty"`
	ToneMapDVBLCompatIDPresent bool                   `json:"tone_map_dv_bl_compat_id_present,omitempty"`
	ToneMapDVBLPresent         bool                   `json:"tone_map_dv_bl_present,omitempty"`
	ToneMapDVRPUPresent        bool                   `json:"tone_map_dv_rpu_present,omitempty"`
	VideoBitstreamFilter       string                 `json:"video_bitstream_filter,omitempty"`
	VideoSampleEntry           string                 `json:"video_sample_entry,omitempty"`
	SeekSeconds                float64                `json:"seek_seconds"`
	StreamOriginSeconds        float64                `json:"stream_origin_seconds,omitempty"`
	CopySeekAnchorResolved     bool                   `json:"copy_seek_anchor_resolved,omitempty"`
	TargetResolution           string                 `json:"target_resolution,omitempty"`
	TargetCodecVideo           string                 `json:"target_codec_video,omitempty"`
	TargetCodecAudio           string                 `json:"target_codec_audio,omitempty"`
	TargetAudioChannels        int                    `json:"target_audio_channels,omitempty"`
	TargetAudioBitrateKbps     int                    `json:"target_audio_bitrate_kbps,omitempty"`
	SegmentDuration            int                    `json:"segment_duration"`
	StartSegmentNumber         int                    `json:"start_segment_number"`
	HWAccel                    string                 `json:"hw_accel,omitempty"`
	HWDevice                   string                 `json:"hw_device,omitempty"`
	SubtitleTrackIndex         int                    `json:"subtitle_track_index"`
	SubtitleBurnIn             bool                   `json:"subtitle_burn_in,omitempty"`
	SubtitleCodec              string                 `json:"subtitle_codec,omitempty"`
	AudioTrackIndex            int                    `json:"audio_track_index"`
	TargetBitrateKbps          int                    `json:"target_bitrate_kbps,omitempty"`
	TotalDuration              float64                `json:"total_duration"`
	FastStart                  bool                   `json:"fast_start,omitempty"`
}

// NewRecipeCard builds a RecipeCard from the durable identity fields plus the
// TranscodeOpts used to start the session. The non-serializable opts fields
// (FFmpegLogSink) are dropped; FFmpegPath/HWAccel/HWDevice are intentionally
// re-resolved from live config on reconstruct rather than pinned here, so an
// operator's config change applies to reconstructed sessions too.
func NewRecipeCard(userID int, profileID string, mediaFileID int, transcodeNodeURL string, opts TranscodeOpts) RecipeCard {
	opts = resolveSoftwareVideoDecode(opts)
	return RecipeCard{
		SessionID:                  opts.SessionID,
		UserID:                     userID,
		ProfileID:                  profileID,
		MediaFileID:                mediaFileID,
		TranscodeNodeURL:           transcodeNodeURL,
		TranscodeTransportID:       opts.TranscodeTransportID,
		PlayMethod:                 PlayTranscode,
		TranscodeAudio:             TranscodesAudio(opts.TargetCodecAudio),
		InputPath:                  opts.InputPath,
		OutputSubdir:               opts.OutputSubdir,
		SourceVideoCodec:           opts.SourceVideoCodec,
		SourceVideoProfile:         opts.SourceVideoProfile,
		SourceVideoBitDepth:        opts.SourceVideoBitDepth,
		SoftwareVideoDecode:        opts.SoftwareVideoDecode,
		ToneMapPolicy:              opts.ToneMapPolicy,
		ToneMapMode:                opts.ToneMapMode,
		ToneMapSourceKind:          opts.ToneMapSourceKind,
		ToneMapFilter:              opts.ToneMapFilter,
		ToneMapRecipeVersion:       opts.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   opts.ToneMapPreflightRequired,
		ToneMapSourceRevision:      opts.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     opts.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: opts.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         opts.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        opts.ToneMapDVRPUPresent,
		VideoBitstreamFilter:       opts.VideoBitstreamFilter,
		VideoSampleEntry:           opts.VideoSampleEntry,
		SeekSeconds:                opts.SeekSeconds,
		StreamOriginSeconds:        opts.StreamOriginSeconds,
		CopySeekAnchorResolved:     opts.CopySeekAnchorResolved,
		TargetResolution:           opts.TargetResolution,
		TargetCodecVideo:           opts.TargetCodecVideo,
		TargetCodecAudio:           opts.TargetCodecAudio,
		TargetAudioChannels:        opts.TargetAudioChannels,
		TargetAudioBitrateKbps:     opts.TargetAudioBitrateKbps,
		SegmentDuration:            opts.SegmentDuration,
		StartSegmentNumber:         opts.StartSegmentNumber,
		HWAccel:                    opts.HWAccel,
		HWDevice:                   opts.HWDevice,
		SubtitleTrackIndex:         opts.SubtitleTrackIndex,
		SubtitleBurnIn:             opts.SubtitleBurnIn,
		SubtitleCodec:              opts.SubtitleCodec,
		AudioTrackIndex:            opts.AudioTrackIndex,
		TargetBitrateKbps:          opts.TargetBitrateKbps,
		TotalDuration:              opts.TotalDuration,
		FastStart:                  opts.FastStart,
	}
}

// NewDirectRecipeCard builds a card for a direct-play session. Only identity is
// needed to rebuild the Session: the file is served by HTTP byte range and the
// client re-supplies its position, so there are no encode parameters and no
// runtime to reconstruct beyond the Session itself.
func NewDirectRecipeCard(sessionID string, userID int, profileID string, mediaFileID int) RecipeCard {
	return RecipeCard{
		SessionID:   sessionID,
		UserID:      userID,
		ProfileID:   profileID,
		MediaFileID: mediaFileID,
		PlayMethod:  PlayDirect,
	}
}

// NewRemuxRecipeCard builds a card for a remux session: identity plus the audio
// selection. The remux ffmpeg is a single pipe re-spawned at the client-supplied
// ?seek= on the next request, so no segment/encode parameters are pinned.
func NewRemuxRecipeCard(sessionID string, userID int, profileID string, mediaFileID int, transcodeAudio bool, audioTrackIndex int, dvMode ...RemuxDVMode) RecipeCard {
	mode := RemuxDVMode("")
	if len(dvMode) > 0 {
		mode = dvMode[0]
	}
	return RecipeCard{
		SessionID:       sessionID,
		UserID:          userID,
		ProfileID:       profileID,
		MediaFileID:     mediaFileID,
		PlayMethod:      PlayRemux,
		TranscodeAudio:  transcodeAudio,
		RemuxDVMode:     mode,
		AudioTrackIndex: audioTrackIndex,
	}
}

// VideoStreamCopy reports whether this recipe delivers the source video
// bitstream without re-encoding it: a progressive remux, or an HLS transport
// whose video target was pinned to an explicit copy. Those are exactly the
// routes an H.264 multi-PPS verdict disqualifies — a real transcode re-encodes
// the bitstream and is unaffected by conflicting in-band parameter sets.
func (c RecipeCard) VideoStreamCopy() bool {
	if c.PlayMethod == PlayRemux {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.TargetCodecVideo), "copy")
}

// TranscodeOpts rebuilds the encode parameters for a reconstruct. outputDir,
// ffmpegPath and logSink are supplied by the caller from live config because
// they are environment-specific and not pinned in the card. ToneMapFilter may
// also be empty after token reconstruction and is resolved from live capability
// data before the transcode starts.
func (c RecipeCard) TranscodeOpts(outputDir, ffmpegPath string, logSink FFmpegLogSink) TranscodeOpts {
	return TranscodeOpts{
		InputPath:                  c.InputPath,
		OutputSubdir:               c.OutputSubdir,
		OutputDir:                  outputDir,
		SessionID:                  c.SessionID,
		TranscodeTransportID:       c.TranscodeTransportID,
		SourceVideoCodec:           c.SourceVideoCodec,
		SourceVideoProfile:         c.SourceVideoProfile,
		SourceVideoBitDepth:        c.SourceVideoBitDepth,
		SoftwareVideoDecode:        c.SoftwareVideoDecode,
		ToneMapPolicy:              c.ToneMapPolicy,
		ToneMapMode:                c.ToneMapMode,
		ToneMapSourceKind:          c.ToneMapSourceKind,
		ToneMapFilter:              c.ToneMapFilter,
		ToneMapRecipeVersion:       c.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   c.ToneMapPreflightRequired,
		ToneMapSourceRevision:      c.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     c.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: c.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         c.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        c.ToneMapDVRPUPresent,
		VideoBitstreamFilter:       c.VideoBitstreamFilter,
		VideoSampleEntry:           c.VideoSampleEntry,
		SeekSeconds:                c.SeekSeconds,
		StreamOriginSeconds:        c.StreamOriginSeconds,
		CopySeekAnchorResolved:     c.CopySeekAnchorResolved,
		TargetResolution:           c.TargetResolution,
		TargetCodecVideo:           c.TargetCodecVideo,
		TargetCodecAudio:           c.TargetCodecAudio,
		TargetAudioChannels:        c.TargetAudioChannels,
		TargetAudioBitrateKbps:     c.TargetAudioBitrateKbps,
		SegmentDuration:            c.SegmentDuration,
		StartSegmentNumber:         c.StartSegmentNumber,
		FFmpegPath:                 ffmpegPath,
		HWAccel:                    c.HWAccel,
		HWDevice:                   c.HWDevice,
		SubtitleTrackIndex:         c.SubtitleTrackIndex,
		SubtitleBurnIn:             c.SubtitleBurnIn,
		SubtitleCodec:              c.SubtitleCodec,
		AudioTrackIndex:            c.AudioTrackIndex,
		TargetBitrateKbps:          c.TargetBitrateKbps,
		TotalDuration:              c.TotalDuration,
		FastStart:                  c.FastStart,
		NodeType:                   "integrated",
		ExecutionMode:              "integrated",
		FFmpegLogSink:              logSink,
	}
}

// MaxTokenTTL is the absolute lifetime of a stream token, and therefore the
// longest a session can remain reconstructable. Under token-carried
// reconstruction there is no durable server-side card index, so segment-dir
// cleanup spares a dir that is not live in memory only until this age elapses:
// past it, no surviving token could still reconstruct the session, so the dir is
// safe to reap. It must comfortably outlast any realistic restart outage.
const MaxTokenTTL = 24 * time.Hour

// ToClaims projects the reconstruction recipe into stream-token claims so the
// card can travel with the client instead of a shared per-session store. The
// environment-specific knobs (HWAccel/HWDevice) are intentionally NOT carried —
// they are re-resolved from live config on reconstruct, so an operator's config
// change applies to reconstructed sessions too.
func (c RecipeCard) ToClaims() streamtoken.Claims {
	playMethod := string(c.PlayMethod)
	if c.PlayMethod == PlayTranscode && c.ToneMapMode != "" {
		// Older binaries do not understand the frozen tone-map claims. Give
		// them a method they reject instead of silently reconstructing SDR
		// output without the required recipe.
		playMethod = streamtoken.PlayMethodToneMapTranscode
	}
	return streamtoken.Claims{
		SessionID:            c.SessionID,
		MediaPath:            c.InputPath,
		OutputSubdir:         c.OutputSubdir,
		DVProfile:            c.DVProfile,
		AudioOnly:            c.AudioOnly,
		PlayMethod:           playMethod,
		TranscodeAudio:       c.TranscodeAudio,
		RemuxDVMode:          string(c.RemuxDVMode),
		TranscodeNode:        c.TranscodeNodeURL,
		TranscodeTransportID: c.TranscodeTransportID,
		TargetCodec:          c.TargetCodecVideo,
		TargetRes:            c.TargetResolution,
		AudioTrackIndex:      c.AudioTrackIndex,
		UserID:               c.UserID,
		ProfileID:            c.ProfileID,
		MediaFileID:          c.MediaFileID,
		OriginalStartedAtUnixNano: func() int64 {
			if c.OriginalStartedAt.IsZero() {
				return 0
			}
			return c.OriginalStartedAt.UnixNano()
		}(),
		SourceVideoCodec:           c.SourceVideoCodec,
		SourceVideoProfile:         c.SourceVideoProfile,
		SourceVideoBitDepth:        c.SourceVideoBitDepth,
		SoftwareVideoDecode:        c.SoftwareVideoDecode,
		ToneMapPolicy:              string(c.ToneMapPolicy),
		ToneMapMode:                string(c.ToneMapMode),
		ToneMapSourceKind:          string(c.ToneMapSourceKind),
		ToneMapRecipeVersion:       c.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   c.ToneMapPreflightRequired,
		ToneMapSourceRevision:      c.ToneMapSourceRevision.Encode(),
		ToneMapDVConfigPresent:     c.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: c.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         c.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        c.ToneMapDVRPUPresent,
		VideoBitstreamFilter:       c.VideoBitstreamFilter,
		VideoSampleEntry:           c.VideoSampleEntry,
		SeekSeconds:                c.SeekSeconds,
		StreamOriginSeconds:        c.StreamOriginSeconds,
		CopySeekAnchorResolved:     c.CopySeekAnchorResolved,
		SegmentDuration:            c.SegmentDuration,
		StartSegmentNumber:         c.StartSegmentNumber,
		SubtitleTrackIndex:         c.SubtitleTrackIndex,
		SubtitleBurnIn:             c.SubtitleBurnIn,
		SubtitleCodec:              c.SubtitleCodec,
		TargetBitrateKbps:          c.TargetBitrateKbps,
		TotalDuration:              c.TotalDuration,
		FastStart:                  c.FastStart,
		TargetCodecAudio:           c.TargetCodecAudio,
		TargetAudioChannels:        c.TargetAudioChannels,
		TargetAudioBitrateKbps:     c.TargetAudioBitrateKbps,
	}
}

// RecipeCardFromClaims rebuilds the reconstruction recipe from verified
// stream-token claims. HWAccel, HWDevice, and ToneMapFilter are deliberately
// absent (re-resolved from live config by the reconstruct path). An empty
// PlayMethod decodes to PlayTranscode for back-compat with any token minted
// before the discriminator.
func RecipeCardFromClaims(c *streamtoken.Claims) RecipeCard {
	if c == nil {
		return RecipeCard{}
	}
	method := PlayMethod(c.PlayMethod)
	if c.PlayMethod == streamtoken.PlayMethodToneMapTranscode {
		method = PlayTranscode
	}
	if method == "" {
		method = PlayTranscode
	}
	sourceRevision, err := tonemap.DecodeSourceRevision(c.ToneMapSourceRevision)
	if err != nil {
		// A malformed frozen revision must fail source validation rather than
		// silently becoming an unfrozen legacy recipe.
		sourceRevision = tonemap.SourceRevision{MediaFileID: -1}
	}
	card := RecipeCard{
		SessionID:                  c.SessionID,
		UserID:                     c.UserID,
		ProfileID:                  c.ProfileID,
		MediaFileID:                c.MediaFileID,
		TranscodeNodeURL:           c.TranscodeNode,
		TranscodeTransportID:       c.TranscodeTransportID,
		PlayMethod:                 method,
		TranscodeAudio:             c.TranscodeAudio,
		RemuxDVMode:                RemuxDVMode(c.RemuxDVMode),
		InputPath:                  c.MediaPath,
		OutputSubdir:               c.OutputSubdir,
		DVProfile:                  c.DVProfile,
		AudioOnly:                  c.AudioOnly,
		SourceVideoCodec:           c.SourceVideoCodec,
		SourceVideoProfile:         c.SourceVideoProfile,
		SourceVideoBitDepth:        c.SourceVideoBitDepth,
		SoftwareVideoDecode:        c.SoftwareVideoDecode,
		ToneMapPolicy:              tonemap.Policy(c.ToneMapPolicy),
		ToneMapMode:                tonemap.Mode(c.ToneMapMode),
		ToneMapSourceKind:          tonemap.SourceKind(c.ToneMapSourceKind),
		ToneMapRecipeVersion:       c.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   c.ToneMapPreflightRequired,
		ToneMapSourceRevision:      sourceRevision,
		ToneMapDVConfigPresent:     c.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: c.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         c.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        c.ToneMapDVRPUPresent,
		VideoBitstreamFilter:       c.VideoBitstreamFilter,
		VideoSampleEntry:           c.VideoSampleEntry,
		SeekSeconds:                c.SeekSeconds,
		StreamOriginSeconds:        c.StreamOriginSeconds,
		CopySeekAnchorResolved:     c.CopySeekAnchorResolved,
		TargetResolution:           c.TargetRes,
		TargetCodecVideo:           c.TargetCodec,
		TargetCodecAudio:           c.TargetCodecAudio,
		TargetAudioChannels:        c.TargetAudioChannels,
		TargetAudioBitrateKbps:     c.TargetAudioBitrateKbps,
		SegmentDuration:            c.SegmentDuration,
		StartSegmentNumber:         c.StartSegmentNumber,
		SubtitleTrackIndex:         c.SubtitleTrackIndex,
		SubtitleBurnIn:             c.SubtitleBurnIn,
		SubtitleCodec:              c.SubtitleCodec,
		AudioTrackIndex:            c.AudioTrackIndex,
		TargetBitrateKbps:          c.TargetBitrateKbps,
		TotalDuration:              c.TotalDuration,
		FastStart:                  c.FastStart,
	}
	if c.OriginalStartedAtUnixNano != 0 {
		card.OriginalStartedAt = time.Unix(0, c.OriginalStartedAtUnixNano).UTC()
	}
	return card
}

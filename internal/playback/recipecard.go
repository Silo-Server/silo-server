package playback

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
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
	SessionID            string `json:"session_id"`
	UserID               int    `json:"user_id"`
	ProfileID            string `json:"profile_id"`
	MediaFileID          int    `json:"media_file_id"`
	TranscodeNodeURL     string `json:"transcode_node_url,omitempty"`
	TranscodeTransportID string `json:"transcode_transport_id,omitempty"`

	// PlayMethod discriminates which serve path reconstructs this session
	// (direct / remux / transcode). Empty decodes as PlayTranscode for
	// back-compat with cards written before direct/remux were reconstructable.
	PlayMethod PlayMethod `json:"play_method,omitempty"`
	// TranscodeAudio mirrors Session.TranscodeAudio; used by the remux path to
	// re-spawn ffmpeg with the same audio handling on reconstruct.
	TranscodeAudio bool        `json:"transcode_audio,omitempty"`
	RemuxDVMode    RemuxDVMode `json:"remux_dv_mode,omitempty"`

	// Encode parameters — mirror of the byte-affecting TranscodeOpts fields.
	// Unused (zero) for direct/remux cards, which carry no segment-based encode.
	InputPath            string  `json:"input_path"`
	OutputSubdir         string  `json:"output_subdir,omitempty"`
	SourceVideoCodec     string  `json:"source_video_codec,omitempty"`
	VideoBitstreamFilter string  `json:"video_bitstream_filter,omitempty"`
	SeekSeconds          float64 `json:"seek_seconds"`
	TargetResolution     string  `json:"target_resolution,omitempty"`
	TargetCodecVideo     string  `json:"target_codec_video,omitempty"`
	TargetCodecAudio     string  `json:"target_codec_audio,omitempty"`
	SegmentDuration      int     `json:"segment_duration"`
	StartSegmentNumber   int     `json:"start_segment_number"`
	HWAccel              string  `json:"hw_accel,omitempty"`
	HWDevice             string  `json:"hw_device,omitempty"`
	SubtitleTrackIndex   int     `json:"subtitle_track_index"`
	SubtitleBurnIn       bool    `json:"subtitle_burn_in,omitempty"`
	SubtitleCodec        string  `json:"subtitle_codec,omitempty"`
	AudioTrackIndex      int     `json:"audio_track_index"`
	TargetBitrateKbps    int     `json:"target_bitrate_kbps,omitempty"`
	TotalDuration        float64 `json:"total_duration"`
	FastStart            bool    `json:"fast_start,omitempty"`
}

// NewRecipeCard builds a RecipeCard from the durable identity fields plus the
// TranscodeOpts used to start the session. The non-serializable opts fields
// (FFmpegLogSink) are dropped; FFmpegPath/HWAccel/HWDevice are intentionally
// re-resolved from live config on reconstruct rather than pinned here, so an
// operator's config change applies to reconstructed sessions too.
func NewRecipeCard(userID int, profileID string, mediaFileID int, transcodeNodeURL string, opts TranscodeOpts) RecipeCard {
	return RecipeCard{
		SessionID:            opts.SessionID,
		UserID:               userID,
		ProfileID:            profileID,
		MediaFileID:          mediaFileID,
		TranscodeNodeURL:     transcodeNodeURL,
		TranscodeTransportID: opts.TranscodeTransportID,
		PlayMethod:           PlayTranscode,
		InputPath:            opts.InputPath,
		OutputSubdir:         opts.OutputSubdir,
		SourceVideoCodec:     opts.SourceVideoCodec,
		VideoBitstreamFilter: opts.VideoBitstreamFilter,
		SeekSeconds:          opts.SeekSeconds,
		TargetResolution:     opts.TargetResolution,
		TargetCodecVideo:     opts.TargetCodecVideo,
		TargetCodecAudio:     opts.TargetCodecAudio,
		SegmentDuration:      opts.SegmentDuration,
		StartSegmentNumber:   opts.StartSegmentNumber,
		HWAccel:              opts.HWAccel,
		HWDevice:             opts.HWDevice,
		SubtitleTrackIndex:   opts.SubtitleTrackIndex,
		SubtitleBurnIn:       opts.SubtitleBurnIn,
		SubtitleCodec:        opts.SubtitleCodec,
		AudioTrackIndex:      opts.AudioTrackIndex,
		TargetBitrateKbps:    opts.TargetBitrateKbps,
		TotalDuration:        opts.TotalDuration,
		FastStart:            opts.FastStart,
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

// TranscodeOpts rebuilds the encode parameters for a reconstruct. outputDir,
// ffmpegPath and logSink are supplied by the caller from live config because
// they are environment-specific and not pinned in the card.
func (c RecipeCard) TranscodeOpts(outputDir, ffmpegPath string, logSink FFmpegLogSink) TranscodeOpts {
	return TranscodeOpts{
		InputPath:            c.InputPath,
		OutputSubdir:         c.OutputSubdir,
		OutputDir:            outputDir,
		SessionID:            c.SessionID,
		TranscodeTransportID: c.TranscodeTransportID,
		SourceVideoCodec:     c.SourceVideoCodec,
		VideoBitstreamFilter: c.VideoBitstreamFilter,
		SeekSeconds:          c.SeekSeconds,
		TargetResolution:     c.TargetResolution,
		TargetCodecVideo:     c.TargetCodecVideo,
		TargetCodecAudio:     c.TargetCodecAudio,
		SegmentDuration:      c.SegmentDuration,
		StartSegmentNumber:   c.StartSegmentNumber,
		FFmpegPath:           ffmpegPath,
		HWAccel:              c.HWAccel,
		HWDevice:             c.HWDevice,
		SubtitleTrackIndex:   c.SubtitleTrackIndex,
		SubtitleBurnIn:       c.SubtitleBurnIn,
		SubtitleCodec:        c.SubtitleCodec,
		AudioTrackIndex:      c.AudioTrackIndex,
		TargetBitrateKbps:    c.TargetBitrateKbps,
		TotalDuration:        c.TotalDuration,
		FastStart:            c.FastStart,
		NodeType:             "integrated",
		ExecutionMode:        "integrated",
		FFmpegLogSink:        logSink,
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
	return streamtoken.Claims{
		SessionID:            c.SessionID,
		MediaPath:            c.InputPath,
		OutputSubdir:         c.OutputSubdir,
		PlayMethod:           string(c.PlayMethod),
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
		SourceVideoCodec:     c.SourceVideoCodec,
		VideoBitstreamFilter: c.VideoBitstreamFilter,
		SeekSeconds:          c.SeekSeconds,
		SegmentDuration:      c.SegmentDuration,
		StartSegmentNumber:   c.StartSegmentNumber,
		SubtitleTrackIndex:   c.SubtitleTrackIndex,
		SubtitleBurnIn:       c.SubtitleBurnIn,
		SubtitleCodec:        c.SubtitleCodec,
		TargetBitrateKbps:    c.TargetBitrateKbps,
		TotalDuration:        c.TotalDuration,
		FastStart:            c.FastStart,
		TargetCodecAudio:     c.TargetCodecAudio,
	}
}

// RecipeCardFromClaims rebuilds the reconstruction recipe from verified
// stream-token claims. HWAccel/HWDevice are deliberately absent (re-resolved
// from live config by the reconstruct path). An empty PlayMethod decodes to
// PlayTranscode for back-compat with any token minted before the discriminator.
func RecipeCardFromClaims(c *streamtoken.Claims) RecipeCard {
	if c == nil {
		return RecipeCard{}
	}
	method := PlayMethod(c.PlayMethod)
	if method == "" {
		method = PlayTranscode
	}
	return RecipeCard{
		SessionID:            c.SessionID,
		UserID:               c.UserID,
		ProfileID:            c.ProfileID,
		MediaFileID:          c.MediaFileID,
		TranscodeNodeURL:     c.TranscodeNode,
		TranscodeTransportID: c.TranscodeTransportID,
		PlayMethod:           method,
		TranscodeAudio:       c.TranscodeAudio,
		RemuxDVMode:          RemuxDVMode(c.RemuxDVMode),
		InputPath:            c.MediaPath,
		OutputSubdir:         c.OutputSubdir,
		SourceVideoCodec:     c.SourceVideoCodec,
		VideoBitstreamFilter: c.VideoBitstreamFilter,
		SeekSeconds:          c.SeekSeconds,
		TargetResolution:     c.TargetRes,
		TargetCodecVideo:     c.TargetCodec,
		TargetCodecAudio:     c.TargetCodecAudio,
		SegmentDuration:      c.SegmentDuration,
		StartSegmentNumber:   c.StartSegmentNumber,
		SubtitleTrackIndex:   c.SubtitleTrackIndex,
		SubtitleBurnIn:       c.SubtitleBurnIn,
		SubtitleCodec:        c.SubtitleCodec,
		AudioTrackIndex:      c.AudioTrackIndex,
		TargetBitrateKbps:    c.TargetBitrateKbps,
		TotalDuration:        c.TotalDuration,
		FastStart:            c.FastStart,
	}
}

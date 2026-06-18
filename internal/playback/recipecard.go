package playback

import (
	"context"
	"time"
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
	SessionID        string `json:"session_id"`
	UserID           int    `json:"user_id"`
	ProfileID        string `json:"profile_id"`
	MediaFileID      int    `json:"media_file_id"`
	TranscodeNodeURL string `json:"transcode_node_url,omitempty"`

	// PlayMethod discriminates which serve path reconstructs this session
	// (direct / remux / transcode). Empty decodes as PlayTranscode for
	// back-compat with cards written before direct/remux were reconstructable.
	PlayMethod PlayMethod `json:"play_method,omitempty"`
	// TranscodeAudio mirrors Session.TranscodeAudio; used by the remux path to
	// re-spawn ffmpeg with the same audio handling on reconstruct.
	TranscodeAudio bool `json:"transcode_audio,omitempty"`

	// Encode parameters — mirror of the byte-affecting TranscodeOpts fields.
	// Unused (zero) for direct/remux cards, which carry no segment-based encode.
	InputPath          string  `json:"input_path"`
	SourceVideoCodec   string  `json:"source_video_codec,omitempty"`
	SeekSeconds        float64 `json:"seek_seconds"`
	TargetResolution   string  `json:"target_resolution,omitempty"`
	TargetCodecVideo   string  `json:"target_codec_video,omitempty"`
	TargetCodecAudio   string  `json:"target_codec_audio,omitempty"`
	SegmentDuration    int     `json:"segment_duration"`
	StartSegmentNumber int     `json:"start_segment_number"`
	HWAccel            string  `json:"hw_accel,omitempty"`
	HWDevice           string  `json:"hw_device,omitempty"`
	SubtitleTrackIndex int     `json:"subtitle_track_index"`
	SubtitleBurnIn     bool    `json:"subtitle_burn_in,omitempty"`
	AudioTrackIndex    int     `json:"audio_track_index"`
	TargetBitrateKbps  int     `json:"target_bitrate_kbps,omitempty"`
	TotalDuration      float64 `json:"total_duration"`
	FastStart          bool    `json:"fast_start,omitempty"`
}

// NewRecipeCard builds a RecipeCard from the durable identity fields plus the
// TranscodeOpts used to start the session. The non-serializable opts fields
// (FFmpegLogSink) are dropped; FFmpegPath/HWAccel/HWDevice are intentionally
// re-resolved from live config on reconstruct rather than pinned here, so an
// operator's config change applies to reconstructed sessions too.
func NewRecipeCard(userID int, profileID string, mediaFileID int, transcodeNodeURL string, opts TranscodeOpts) RecipeCard {
	return RecipeCard{
		SessionID:          opts.SessionID,
		UserID:             userID,
		ProfileID:          profileID,
		MediaFileID:        mediaFileID,
		TranscodeNodeURL:   transcodeNodeURL,
		PlayMethod:         PlayTranscode,
		InputPath:          opts.InputPath,
		SourceVideoCodec:   opts.SourceVideoCodec,
		SeekSeconds:        opts.SeekSeconds,
		TargetResolution:   opts.TargetResolution,
		TargetCodecVideo:   opts.TargetCodecVideo,
		TargetCodecAudio:   opts.TargetCodecAudio,
		SegmentDuration:    opts.SegmentDuration,
		StartSegmentNumber: opts.StartSegmentNumber,
		HWAccel:            opts.HWAccel,
		HWDevice:           opts.HWDevice,
		SubtitleTrackIndex: opts.SubtitleTrackIndex,
		SubtitleBurnIn:     opts.SubtitleBurnIn,
		AudioTrackIndex:    opts.AudioTrackIndex,
		TargetBitrateKbps:  opts.TargetBitrateKbps,
		TotalDuration:      opts.TotalDuration,
		FastStart:          opts.FastStart,
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
func NewRemuxRecipeCard(sessionID string, userID int, profileID string, mediaFileID int, transcodeAudio bool, audioTrackIndex int) RecipeCard {
	return RecipeCard{
		SessionID:       sessionID,
		UserID:          userID,
		ProfileID:       profileID,
		MediaFileID:     mediaFileID,
		PlayMethod:      PlayRemux,
		TranscodeAudio:  transcodeAudio,
		AudioTrackIndex: audioTrackIndex,
	}
}

// TranscodeOpts rebuilds the encode parameters for a reconstruct. outputDir,
// ffmpegPath and logSink are supplied by the caller from live config because
// they are environment-specific and not pinned in the card.
func (c RecipeCard) TranscodeOpts(outputDir, ffmpegPath string, logSink FFmpegLogSink) TranscodeOpts {
	return TranscodeOpts{
		InputPath:          c.InputPath,
		OutputDir:          outputDir,
		SessionID:          c.SessionID,
		SourceVideoCodec:   c.SourceVideoCodec,
		SeekSeconds:        c.SeekSeconds,
		TargetResolution:   c.TargetResolution,
		TargetCodecVideo:   c.TargetCodecVideo,
		TargetCodecAudio:   c.TargetCodecAudio,
		SegmentDuration:    c.SegmentDuration,
		StartSegmentNumber: c.StartSegmentNumber,
		FFmpegPath:         ffmpegPath,
		HWAccel:            c.HWAccel,
		HWDevice:           c.HWDevice,
		SubtitleTrackIndex: c.SubtitleTrackIndex,
		SubtitleBurnIn:     c.SubtitleBurnIn,
		AudioTrackIndex:    c.AudioTrackIndex,
		TargetBitrateKbps:  c.TargetBitrateKbps,
		TotalDuration:      c.TotalDuration,
		FastStart:          c.FastStart,
		NodeType:           "integrated",
		ExecutionMode:      "integrated",
		FFmpegLogSink:      logSink,
	}
}

// RecipeStore persists RecipeCards so a transcode session can be reconstructed
// after the in-memory state is lost. The Postgres-backed PostgresRecipeStore is
// the production implementation; the interface remains so tests can inject a
// fake. Implementations must be safe for concurrent use and must no-op
// gracefully when persistence is unavailable so callers never need to
// special-case a disabled store.
type RecipeStore interface {
	// Enabled reports whether persistence is actually wired. When false, every
	// other method is a no-op and reconstruct is disabled (today's behavior).
	Enabled() bool
	// Save writes (or overwrites) the card and arms its TTL.
	Save(ctx context.Context, card RecipeCard) error
	// Get returns the card for sessionID. found is false when absent.
	Get(ctx context.Context, sessionID string) (card RecipeCard, found bool, err error)
	// Delete removes the card (clean session stop).
	Delete(ctx context.Context, sessionID string) error
	// Refresh re-arms the TTL for a still-live session.
	Refresh(ctx context.Context, sessionID string) error
	// ActiveSessionIDs lists every session that still has a card. Used by the
	// segment-directory cleanup to spare dirs whose session is resumable.
	ActiveSessionIDs(ctx context.Context) (map[string]struct{}, error)
	// DeleteExpired physically removes lapsed rows and returns the count. Reads
	// already filter on expiry, so this only bounds table growth; it runs on the
	// janitor cadence and at boot cleanup.
	DeleteExpired(ctx context.Context) (int64, error)
}

const (
	// recipeTTL is the idle/abandonment window for a recipe card. It is re-armed
	// on activity (see PlaybackHandler refresh), so it caps how long an
	// idle/paused/abandoned session stays reconstructable — not session length.
	// It must comfortably outlast a paused-session grace window plus a restart.
	recipeTTL = 30 * time.Minute
)

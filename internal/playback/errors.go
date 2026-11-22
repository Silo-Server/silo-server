package playback

import "errors"

// ErrSegmentIncomplete means a segment name exists with bytes but they are not
// (yet) a whole segment: a writer is still filling it, or truncated it and has
// not finished rewriting it. It is transient and retryable, and it must never
// reach a client as a 200 body.
var ErrSegmentIncomplete = errors.New("segment is not complete yet")

// Sentinel errors for playback operations.
var (
	ErrSessionNotFound          = errors.New("playback session not found")
	ErrTooManyStreams           = errors.New("too many concurrent streams")
	ErrTooManyTranscodes        = errors.New("too many concurrent transcodes")
	ErrTranscodingDisabled      = errors.New("transcoding is disabled for this user")
	ErrAudioTranscodingDisabled = errors.New("audio transcoding is disabled for this user")
	// ErrPlaybackNotAllowed is the generic policy admission denial: a denial
	// without a recognized concurrency-limit code (e.g. an admin custom
	// override, or a failed policy evaluation) must not masquerade as one.
	ErrPlaybackNotAllowed        = errors.New("playback not allowed by policy")
	ErrFileNotFound              = errors.New("media file not found")
	ErrTranscodeFailed           = errors.New("transcode process failed")
	ErrSegmentNotFound           = errors.New("segment not found")
	ErrSubtitleRenditionNotFound = errors.New("subtitle rendition not found")
	ErrSegmentStalled            = errors.New("segment production stalled")
	ErrSubtitleWarmCanceled      = errors.New("subtitle warm canceled by playback")
	ErrManifestNotReady          = errors.New("manifest not ready")
	// ErrManifestQueryTooLong rejects a playlist request whose query is longer
	// than maxManifestQueryBytes.
	ErrManifestQueryTooLong = errors.New("playlist request query too long")
	// ErrAdmissionDeciderTimeout reports that the optional policy hook exceeded its deadline.
	ErrAdmissionDeciderTimeout = errors.New("admission decider timed out")
	// ErrToneMapSourceValidationUnavailable reports that the executor could not
	// freshly probe the source metadata needed to validate a tone-map recipe.
	// The failure is transient: callers may retry after the source or probe
	// executable becomes available again.
	ErrToneMapSourceValidationUnavailable = errors.New("tone-map source validation unavailable")
	// ErrToneMapExecutorUnavailable reports a transient failure while probing the
	// executor needed by an otherwise valid frozen tone-map recipe. Unsupported
	// executors remain ordinary validation errors and are not retry-classified.
	ErrToneMapExecutorUnavailable = errors.New("tone-map executor unavailable")
	// ErrSessionSuperseded means the session a restart was about to re-spawn is
	// no longer the live mapped session (a concurrent teardown or reconstruct
	// replaced it while the restart waited for the per-session lifecycle lock).
	// The caller must not re-spawn ffmpeg for the stale handle.
	ErrSessionSuperseded = errors.New("transcode session superseded")
	// ErrLimitProviderUnavailable wraps a failure to load a user's admission
	// limits from the limit provider (e.g. a transient Postgres error during a
	// post-restart reconstruct wave). It is distinct from the genuine over-cap
	// sentinels (ErrTooManyStreams / ErrTooManyTranscodes): a provider failure
	// means limits could not be evaluated at all, so callers may choose to fail
	// open rather than treat the session as over its (unknown) cap.
	ErrLimitProviderUnavailable = errors.New("session limit provider unavailable")
	// ErrVirtualSourceDemuxFailed reports that a transcode session's source
	// produced repeated input demux I/O failures and the candidate was stamped
	// known-bad. A restart must not rebuild the same bad transport; the caller
	// rotates to another candidate on the next failure recovery.
	ErrVirtualSourceDemuxFailed = errors.New("virtual source produced repeated demux failures")
	// ErrSourceDecodeRejected reports that a transcode generation was revoked
	// after its decoder rejected the source with no usable video produced.
	// Segment and manifest waits return it instead of burning their full
	// timeout, and restarts refuse with it: rebuilding the same undecodable
	// bytes can never succeed, so the caller rotates to another candidate.
	ErrSourceDecodeRejected = errors.New("transcode source rejected by video decoder")
	// ErrTranscodeNoVideoFrames reports a video encode generation that muxed
	// playable-looking segments without decoding a single frame. Segment
	// existence alone cannot tell that apart from healthy output, so a start
	// used to declare such a plan ready and leave the client waiting for a
	// first frame that never came. The verdict is per generation: unlike
	// ErrSourceDecodeRejected it does not indict the source bytes, only this
	// execution path's decode, so the caller advances to another path.
	ErrTranscodeNoVideoFrames = errors.New("transcode muxed segments with no video frames")
	// ErrStaleSegmentGeneration reports a segment or range request carrying a
	// generation token that names neither the live generation nor a retained
	// one. The bytes the URL was minted against no longer exist, so serving the
	// current generation's same-numbered bytes would silently mix generations;
	// the caller rejects the read and the client reloads the manifest.
	ErrStaleSegmentGeneration = errors.New("segment generation is stale")
	// ErrSegmentRecoverySuperseded reports that a segment wait or restart was
	// issued for a target a newer seek has replaced. The viewer moved on, so
	// the old target must not restart FFmpeg; the request answers the
	// stale-generation verdict and the client follows the newest playlist.
	ErrSegmentRecoverySuperseded = errors.New("segment recovery request superseded")
)

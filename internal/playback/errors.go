package playback

import "errors"

// Sentinel errors for playback operations.
var (
	ErrNoVersions        = errors.New("no file versions available")
	ErrSessionNotFound   = errors.New("playback session not found")
	ErrTooManyStreams    = errors.New("too many concurrent streams")
	ErrTooManyTranscodes = errors.New("too many concurrent transcodes")
	// ErrPlaybackNotAllowed is the generic policy admission denial: a denial
	// without a recognized concurrency-limit code (e.g. an admin custom
	// override, or a failed policy evaluation) must not masquerade as one.
	ErrPlaybackNotAllowed = errors.New("playback not allowed by policy")
	ErrFileNotFound       = errors.New("media file not found")
	ErrTranscodeFailed    = errors.New("transcode process failed")
	ErrSegmentNotFound    = errors.New("segment not found")
	ErrManifestNotReady   = errors.New("manifest not ready")
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
)

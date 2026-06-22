package playback

import "errors"

// Sentinel errors for playback operations.
var (
	ErrNoVersions        = errors.New("no file versions available")
	ErrSessionNotFound   = errors.New("playback session not found")
	ErrTooManyStreams    = errors.New("too many concurrent streams")
	ErrTooManyTranscodes = errors.New("too many concurrent transcodes")
	ErrFileNotFound      = errors.New("media file not found")
	ErrTranscodeFailed   = errors.New("transcode process failed")
	ErrSegmentNotFound   = errors.New("segment not found")
	ErrManifestNotReady  = errors.New("manifest not ready")
	// ErrLimitProviderUnavailable wraps a failure to load a user's admission
	// limits from the limit provider (e.g. a transient Postgres error during a
	// post-restart reconstruct wave). It is distinct from the genuine over-cap
	// sentinels (ErrTooManyStreams / ErrTooManyTranscodes): a provider failure
	// means limits could not be evaluated at all, so callers may choose to fail
	// open rather than treat the session as over its (unknown) cap.
	ErrLimitProviderUnavailable = errors.New("session limit provider unavailable")
)

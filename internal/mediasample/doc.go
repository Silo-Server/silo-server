// Package mediasample owns every ffmpeg run that decodes a media file for
// analysis: audio fingerprints, silence, and frame statistics today, images
// as later consumers need them. It never runs playback transcodes or remuxes,
// and it does not build GPU-encode pipelines; those stay in playback and
// tonemap.
//
// A caller describes the run as a typed, JSON-serializable Request (what to
// sample, what to produce, which decode attempts to make) and a Runner turns
// it into ffmpeg arguments, runs it, and parses the result. Requests never
// carry raw filter strings, so the same Request can later be sent to a remote
// node that builds its own arguments.
//
// The package also holds the shared pieces those runs need: a per-binary
// capability inventory (LoadCapabilities), and a resizable concurrency
// Limiter with a slot reserved for work a viewer is waiting on.
//
// See docs/architecture/media-sampling.md.
package mediasample

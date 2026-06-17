// Package ebookconvert converts Kindle-family ebooks (MOBI/AZW/AZW3) to EPUB
// in-process, by running libmobi's mobitool compiled to wasm32-wasi on the
// wazero runtime. No cgo, no external binary; untrusted input is parsed inside
// the WASM sandbox. See docs/.../2026-06-17-kindle-epub-conversion-design.md.
package ebookconvert

import "errors"

var (
	// ErrDRMProtected means the source is encrypted/DRM'd and cannot be
	// converted without a device key. Callers should surface the
	// "open externally" path; the source is never usable in-app.
	ErrDRMProtected = errors.New("ebookconvert: source is DRM-protected")

	// ErrConversionFailed means mobitool failed or produced output that is
	// not a valid EPUB (corrupt source, unexpected format, internal error).
	ErrConversionFailed = errors.New("ebookconvert: conversion failed")

	// ErrSourceTooLarge means the source exceeds the configured size cap and
	// was rejected before invoking the converter.
	ErrSourceTooLarge = errors.New("ebookconvert: source exceeds max size")

	// ErrUnavailable means the converter could not be initialized (the
	// embedded module failed to compile/instantiate). The capability must
	// not be advertised in this state.
	ErrUnavailable = errors.New("ebookconvert: converter unavailable")
)

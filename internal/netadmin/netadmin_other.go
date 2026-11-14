//go:build !(linux && cgo)

package netadmin

// Current returns the unsupported platform state. This build has no helper,
// so Helper stays HelperUnsupportedPlatform.
func Current() State { return State{Helper: HelperUnsupportedPlatform} }

// SetCongestion asks the helper to set TCP_CONGESTION to bbr on fd.
// This build has no helper, so it returns ErrUnavailable.
func SetCongestion(fd int) error { return ErrUnavailable }

// ForceSendBuffer asks the helper to set SO_SNDBUFFORCE to bytes on fd.
// This build has no helper, so it returns ErrUnavailable.
func ForceSendBuffer(fd, bytes int) error { return ErrUnavailable }

// Release idempotently closes the helper channel and reaps a running helper.
// Later socket operations return ErrUnavailable. This build has no helper.
func Release() {}

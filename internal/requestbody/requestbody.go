// Package requestbody provides byte-counted reads for bounded HTTP control payloads.
package requestbody

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrUnreadable identifies transport-level request body read or close failures.
var ErrUnreadable = errors.New("request body is unreadable")

// Read consumes and closes r.Body while enforcing maxBytes on bytes actually
// received. It does not trust Content-Length, so the same budget applies to
// chunked and HTTP/2 request bodies.
func Read(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	limited := http.MaxBytesReader(w, r.Body, maxBytes)
	body, readErr := io.ReadAll(limited)
	closeErr := limited.Close()
	if readErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, closeErr)
	}
	return body, nil
}

// IsTooLarge reports whether err came from exceeding a Read byte budget.
func IsTooLarge(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)
	return ok
}

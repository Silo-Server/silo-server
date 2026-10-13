package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// decodeLimitedJSONBody decodes a JSON request body into dst, reading at most
// limit bytes. It writes 413 when decoding needs more than limit bytes and 400
// for malformed JSON, and returns false in both cases. allowEmpty treats an
// empty body as a zero-value request for endpoints whose fields are all
// optional.
func decodeLimitedJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any, allowEmpty bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || (allowEmpty && errors.Is(err, io.EOF)) {
		return true
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Request body is too large")
		return false
	}
	writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
	return false
}

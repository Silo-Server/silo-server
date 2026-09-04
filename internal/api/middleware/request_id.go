package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// RequestID assigns every request a server-generated ID and stores it under
// chi's RequestIDKey, so chimw.GetReqID keeps working for the request logger,
// activity log, playback telemetry and policy decisions.
//
// Unlike chi's middleware.RequestID it never adopts a client-supplied
// X-Request-Id: the ID is a server-side correlation handle that reaches logs
// and the v2 Problem Details `instance`, and a caller must not be able to
// choose it. It also sets no response header; the v2 listener adds
// X-Request-ID from this same context value on its own routes.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chimw.RequestIDKey, NewRequestID())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// NewRequestID mints a random 96-bit request ID as 24 hex characters. It is
// unpredictable, needs no per-process coordination and never embeds the host
// name.
func NewRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a broken host; keep the request going with a
		// distinguishable, still unique-enough value rather than a blank.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

package httpstream

import (
	"context"
	"errors"
	"net/http"
)

// Serve runs next and then record, once, for a middleware that records each
// request it passes on. A handler that has committed a response and cannot
// finish it breaks it with panic(http.ErrAbortHandler), and that panic
// unwinds past any code after next.ServeHTTP, so a recorder that only runs on
// return never sees the request.
//
// aborted reports that the handler broke the response while its client was
// still waiting. A client that went away is recorded as it would be had the
// handler returned, since its leaving is what ended the response. net/http
// also cancels the request context when a buffered write to the client fails,
// a write deadline included, so those count as the client going away. An
// HTTP/1.1 body sent through ReadFrom (the sendfile path) writes to the
// connection directly, and net/http does not cancel the context when that
// write fails, so a client that stalls there is recorded as aborted. record
// runs before the abort carries on, and the abort is re-raised even if record
// panics, so net/http still breaks the connection rather than ending the body
// cleanly. Any other panic passes through unrecorded, to the recovery
// middleware that answers it.
func Serve(next http.Handler, w http.ResponseWriter, r *http.Request, record func(aborted bool)) {
	returned := false
	defer func() {
		if returned {
			// The handler finished; a panic from record itself is not an abort.
			return
		}
		if rec := recover(); rec != nil {
			if rec != http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
				panic(rec)
			}
			defer panic(rec)
			record(!errors.Is(r.Context().Err(), context.Canceled))
		}
	}()
	next.ServeHTTP(w, r)
	returned = true
	record(false)
}

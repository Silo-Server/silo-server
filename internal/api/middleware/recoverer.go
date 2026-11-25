package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/logredact"
)

// Recoverer answers a handler's panic with a 500 that the recording
// middleware around it sees, so the request metrics and the activity log
// record the crash like any other server error (#3735). Unlike chi's
// Recoverer it writes the 500 for an upgrade request too: chi skips the write
// when the request asks for Connection: Upgrade, and the recorders would then
// keep their default 200. A handler that already wrote its status keeps it.
// http.ErrAbortHandler passes through unchanged, so net/http still breaks the
// connection of an aborted response.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
				panic(rec)
			}
			// The path can carry a bearer credential ({token} or {secret});
			// log it redacted, as the request and activity logs do. The panic
			// value can too, as an error's text (desc = api_key=...), and the
			// log handler redacts by key only, so it is masked here.
			slog.ErrorContext(r.Context(), "panic serving request", "component", "api",
				"request_id", chimw.GetReqID(r.Context()), "method", r.Method,
				"path", activitylog.RedactSecretPathParams(r, r.URL.Path),
				"panic", logredact.SanitizeText(fmt.Sprint(rec)), "stack", string(debug.Stack()))
			w.WriteHeader(http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

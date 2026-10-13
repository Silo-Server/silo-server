package auditmutation

import (
	"context"
	"net/http"
	"time"
)

type LogContext struct {
	Request            *http.Request
	NodeID             string
	Started            time.Time
	Publish            func(Entry)
	Committed          []Entry
	UserID             *int
	ImpersonatorUserID *int
	SessionID          string
}

type logContextKey struct{}

// SetLogContext stores a LogContext pointer in the request context.
func SetLogContext(ctx context.Context, lc *LogContext) context.Context {
	return context.WithValue(ctx, logContextKey{}, lc)
}

// GetLogContext retrieves the LogContext from the request context.
func GetLogContext(ctx context.Context) *LogContext {
	lc, _ := ctx.Value(logContextKey{}).(*LogContext)
	return lc
}

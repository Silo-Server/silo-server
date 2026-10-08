// Package ctxerr identifies errors that only report that a caller went away,
// so request paths do not count or log them as server failures.
package ctxerr

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IsCanceled reports whether err is a cancellation: context.Canceled, or the
// gRPC Canceled status a plugin call returns when its caller's context ends.
// Either may be wrapped. A deadline is not a cancellation.
func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}

// Abandoned reports whether err only says that the caller of ctx went away:
// ctx itself was canceled and err is a cancellation. A real failure that
// lands after the caller left is not abandoned.
func Abandoned(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && IsCanceled(err)
}

// LogLevel is level, or Debug when err only says that the caller of ctx went
// away.
func LogLevel(ctx context.Context, err error, level slog.Level) slog.Level {
	if Abandoned(ctx, err) {
		return slog.LevelDebug
	}
	return level
}

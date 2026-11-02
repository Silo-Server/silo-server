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
// ctx itself was canceled and every error err carries is a cancellation. A
// real failure that lands after the caller left is not abandoned, even when
// it is joined with the cancellation.
func Abandoned(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && onlyCanceled(err)
}

// onlyCanceled reports whether err is a cancellation and carries nothing
// else: each error joined into it must be one too.
func onlyCanceled(err error) bool {
	switch wrapped := err.(type) { //nolint:errorlint // walks the error tree itself
	case interface{ Unwrap() []error }:
		found := false
		for _, inner := range wrapped.Unwrap() {
			if inner == nil {
				continue
			}
			if !onlyCanceled(inner) {
				return false
			}
			found = true
		}
		return found
	case interface{ Unwrap() error }:
		if inner := wrapped.Unwrap(); inner != nil {
			return onlyCanceled(inner)
		}
	}
	return IsCanceled(err)
}

// LogLevel is level, or Debug when err only says that the caller of ctx went
// away.
func LogLevel(ctx context.Context, err error, level slog.Level) slog.Level {
	if Abandoned(ctx, err) {
		return slog.LevelDebug
	}
	return level
}

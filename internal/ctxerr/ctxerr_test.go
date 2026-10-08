package ctxerr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsCanceled(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, true},
		{"wrapped canceled", fmt.Errorf("loading paths for list: %w", context.Canceled), true},
		{"joined canceled", errors.Join(errors.New("other"), context.Canceled), true},
		{"grpc canceled", status.Error(codes.Canceled, "context canceled"), true},
		{"wrapped grpc canceled", fmt.Errorf("marker provider: %w", status.Error(codes.Canceled, "context canceled")), true},
		{"deadline", context.DeadlineExceeded, false},
		{"grpc deadline", status.Error(codes.DeadlineExceeded, "context deadline exceeded"), false},
		{"grpc unavailable", status.Error(codes.Unavailable, "connection refused"), false},
		{"other", errors.New("query failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCanceled(tc.err); got != tc.want {
				t.Fatalf("IsCanceled(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestLogLevelNeedsTheCallerGone(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelExpired := context.WithTimeout(t.Context(), 0)
	defer cancelExpired()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want slog.Level
	}{
		{"caller gone", canceled, fmt.Errorf("query: %w", context.Canceled), slog.LevelDebug},
		{"plugin call after the caller left", canceled, status.Error(codes.Canceled, "context canceled"), slog.LevelDebug},
		{"real failure after the caller left", canceled, errors.New("connection reset"), slog.LevelWarn},
		{"cancellation while the caller waits", t.Context(), context.Canceled, slog.LevelWarn},
		{"deadline", expired, context.DeadlineExceeded, slog.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LogLevel(tc.ctx, tc.err, slog.LevelWarn); got != tc.want {
				t.Fatalf("LogLevel = %v, want %v", got, tc.want)
			}
		})
	}
}

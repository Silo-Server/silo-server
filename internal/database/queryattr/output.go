package queryattr

import (
	"context"
	"log/slog"
)

// A failed observability sink cannot interrupt database completion or shutdown.
func write(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	defer func() { _ = recover() }()
	//nolint:sloglint // This shared sink receives the fixed event and summary messages from its callers.
	slog.Default().LogAttrs(ctx, level, msg, attrs...)
}

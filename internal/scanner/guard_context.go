package scanner

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/claimcontext"
)

// WithOwnershipGuard installs a synchronous ownership check for scan work.
func WithOwnershipGuard(ctx context.Context, guard func() error) context.Context {
	return claimcontext.WithOwnershipGuard(ctx, guard)
}

// WithGuardedCancel derives a cancelable context without losing an installed
// ownership guard. Scanner code must use it instead of context.WithCancel when
// deriving a context that can perform ingest mutations.
func WithGuardedCancel(ctx context.Context) (context.Context, context.CancelFunc) {
	return claimcontext.WithGuardedCancel(ctx)
}

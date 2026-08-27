package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestOwnershipGuardSurvivesScannerContextDerivation(t *testing.T) {
	var expired atomic.Bool
	ctx := WithOwnershipGuard(t.Context(), func() error {
		if expired.Load() {
			return errors.New("claim expired")
		}
		return nil
	})
	ingestCtx, cancelIngest := WithGuardedCancel(ctx)
	defer cancelIngest()
	watchCtx, cancelWatch := WithGuardedCancel(ingestCtx)
	defer cancelWatch()

	if err := watchCtx.Err(); err != nil {
		t.Fatalf("watch context before expiry = %v, want nil", err)
	}
	expired.Store(true)
	if err := watchCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("watch context after expiry = %v, want %v", err, context.Canceled)
	}
}

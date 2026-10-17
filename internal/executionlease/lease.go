// Package executionlease retains admitted local work across shared probes.
package executionlease

import (
	"context"
	"errors"
	"sync"
)

type retainerKey struct{}
type Retainer func() (func(), error)

// WithRetainer carries the local work's reference factory. Shared flights keep
// the value through context.WithoutCancel while retaining their own deadline.
func WithRetainer(ctx context.Context, retain Retainer) context.Context {
	return context.WithValue(ctx, retainerKey{}, retain)
}

// Retain must be called before scheduling work that can outlive its caller.
// Legacy callers without a local admission policy retain their prior behavior.
func Retain(ctx context.Context) (func(), error) {
	retain, ok := ctx.Value(retainerKey{}).(Retainer)
	if !ok {
		return func() {}, nil
	}
	if retain == nil {
		return nil, errors.New("execution lease unavailable")
	}
	release, err := retain()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, errors.New("execution lease unavailable")
	}
	return sync.OnceFunc(release), nil
}

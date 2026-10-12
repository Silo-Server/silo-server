package catalog

import "context"

type speculativeReadKey struct{}

// WithSpeculativeRead marks ctx as a speculative read, such as a client
// warming its cache for a card the pointer rests on. Detail construction
// then skips probe repair, which runs ffprobe and persists the result; the
// ordinary read made when the title is actually opened performs it.
func WithSpeculativeRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, speculativeReadKey{}, true)
}

func isSpeculativeRead(ctx context.Context) bool {
	speculative, _ := ctx.Value(speculativeReadKey{}).(bool)
	return speculative
}

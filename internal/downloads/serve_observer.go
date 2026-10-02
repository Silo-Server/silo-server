package downloads

import "context"

type serveAuthorizedContextKey struct{}

// WithServeAuthorized registers a request-scoped callback invoked after a file
// target is authorized and before response bytes are served. It does not alter
// authorization or serving behavior.
func WithServeAuthorized(ctx context.Context, callback func(FileTarget)) context.Context {
	if ctx == nil || callback == nil {
		return ctx
	}
	return context.WithValue(ctx, serveAuthorizedContextKey{}, callback)
}

func notifyServeAuthorized(ctx context.Context, target FileTarget) {
	if ctx == nil {
		return
	}
	callback, _ := ctx.Value(serveAuthorizedContextKey{}).(func(FileTarget))
	if callback != nil {
		callback(target)
	}
}

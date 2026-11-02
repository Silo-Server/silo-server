package downloads

import (
	"context"
	"os"
)

type (
	serveAuthorizedContextKey struct{}
	serveEntityTagContextKey  struct{}
)

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

// WithServeEntityTag makes local file serving send tag(file, info) as the
// response ETag, computed from the descriptor it serves. Without it no ETag
// is sent.
func WithServeEntityTag(ctx context.Context, tag func(*os.File, os.FileInfo) string) context.Context {
	if ctx == nil || tag == nil {
		return ctx
	}
	return context.WithValue(ctx, serveEntityTagContextKey{}, tag)
}

func serveEntityTag(ctx context.Context, file *os.File, info os.FileInfo) string {
	if ctx == nil {
		return ""
	}
	tag, _ := ctx.Value(serveEntityTagContextKey{}).(func(*os.File, os.FileInfo) string)
	if tag == nil {
		return ""
	}
	return tag(file, info)
}

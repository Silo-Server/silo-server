package catalog

import (
	"context"
)

type echoImageResolver struct{}

func (echoImageResolver) ResolveImageURL(_ context.Context, path, _ string) string {
	return "https://cdn.example/" + path
}

func (r echoImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

type countingResolver struct {
	echoImageResolver
	calls int
}

func (r *countingResolver) ResolveImageURL(ctx context.Context, path, variant string) string {
	r.calls++
	return r.echoImageResolver.ResolveImageURL(ctx, path, variant)
}

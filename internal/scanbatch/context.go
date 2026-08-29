// Package scanbatch carries immutable scan-run provenance through ingestion.
package scanbatch

import (
	"context"
	"strings"
)

type runIDKey struct{}

// WithRunID returns a child context carrying the scan run that first observed
// newly inserted media files. Empty IDs deliberately clear the value.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey{}, strings.TrimSpace(runID))
}

// RunID returns the scan-run provenance carried by ctx, or an empty string for
// legacy/manual ingestion paths that are not owned by the scan queue.
func RunID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	runID, _ := ctx.Value(runIDKey{}).(string)
	return strings.TrimSpace(runID)
}

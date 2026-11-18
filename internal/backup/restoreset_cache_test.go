package backup

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
)

// countingDestination counts reads of restore-set.tar.
type countingDestination struct {
	Destination
	opens atomic.Int32
}

func (d *countingDestination) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if strings.HasSuffix(key, "/"+RestoreSetName) {
		d.opens.Add(1)
	}
	return d.Destination.Open(ctx, key)
}

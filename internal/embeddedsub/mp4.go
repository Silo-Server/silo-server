package embeddedsub

import (
	"context"
	"io"
	"time"
)

func isMP4(head []byte) bool { return false }

func readMP4Index(r io.ReaderAt, size int64, head []byte) (*Index, error) {
	return nil, ErrNotSupportedContainer
}

func readMP4Packets(ctx context.Context, r *fileReader, t *Track, from, to time.Duration, concurrency int) (*Result, error) {
	return nil, ErrNotSupportedContainer
}

func writeMP4(w io.Writer, t *Track, packets []Packet) error {
	return ErrNotSupportedContainer
}

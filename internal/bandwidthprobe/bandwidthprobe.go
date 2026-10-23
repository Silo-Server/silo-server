// Package bandwidthprobe writes the payload clients download to measure their
// throughput to the server.
package bandwidthprobe

import (
	crand "crypto/rand"
	"io"
	"math/rand/v2"
)

// MaxSize is the largest probe a client may request. It is large enough to
// time a fast link for a couple of seconds and small enough that a probe stays
// cheap next to the media the same viewer could download.
const MaxSize = 100_000_000

// Write sends size random bytes to w. The bytes are random rather than
// constant so a compressing proxy cannot shrink the payload and inflate the
// measured bandwidth. It stops at the first write error, which is how a client
// that has seen enough ends the probe.
func Write(w io.Writer, size int) error {
	var seed [32]byte
	_, _ = crand.Read(seed[:])
	source := rand.NewChaCha8(seed)
	block := make([]byte, min(size, 32768))
	for size > 0 {
		chunk := block[:min(size, len(block))]
		_, _ = source.Read(chunk)
		n, err := w.Write(chunk)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		size -= n
	}
	return nil
}

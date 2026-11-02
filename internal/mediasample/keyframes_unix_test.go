//go:build unix

package mediasample

import (
	"path/filepath"
	"syscall"
	"testing"
)

// TestMatroskaKeyframesSkipsAFIFO names a FIFO without a writer, which
// opening would block on.
func TestMatroskaKeyframesSkipsAFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if got := matroskaKeyframes(t.Context(), fifo); got != nil {
		t.Fatalf("keyframes %v, want none", got)
	}
}

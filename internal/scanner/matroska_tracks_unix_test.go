//go:build unix

package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Opening a FIFO with no writer blocks, the way a read on stalled network
// storage does. The read is abandoned instead of holding the caller.
func TestReadMatroskaSubtitleTrackIDsAbandonsStalledRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stalled.mkv")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Unblock the abandoned open when the test ends.
	t.Cleanup(func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})

	saved := matroskaTracksReadTimeout
	matroskaTracksReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { matroskaTracksReadTimeout = saved })
	if _, err := readMatroskaSubtitleTrackIDs(context.Background(), path, 1, 1, nil); !errors.Is(err, errMatroskaTracksReadTimeout) {
		t.Fatalf("err = %v, want a read timeout", err)
	}

	matroskaTracksReadTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readMatroskaSubtitleTrackIDs(ctx, path, 1, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
}

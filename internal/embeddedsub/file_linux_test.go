package embeddedsub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPacketReadCancellationWhileOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked-open")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ReadPackets(ctx, path, &Index{Tracks: []Track{{Number: 1, Completeness: CompletenessUnknown}}}, 1, WholeFrom, WholeTo, ReadOptions{})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read cancellation: %v", err)
		}
	case <-time.After(time.Second):
		// Release the kernel open before failing, so the regression leaves no
		// blocked goroutine or descriptor behind.
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			defer func() { _ = f.Close() }()
		}
		<-done
		t.Fatal("packet read ignored cancellation while opening the source")
	}
}

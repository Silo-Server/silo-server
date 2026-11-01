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
	var pending chan struct{}
	defer func() {
		// Hold a writer open until the kernel open has returned, including
		// when cancellation wins before that worker reaches the syscall.
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			defer func() { _ = f.Close() }()
			if pending != nil {
				<-pending
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := ReadPackets(ctx, path, &Index{Tracks: []Track{{Number: 1, Completeness: CompletenessUnknown}}}, 1, WholeFrom, WholeTo, ReadOptions{})
		done <- err
	}()
	started := time.NewTicker(time.Millisecond)
	defer started.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for pending == nil {
		sourceOpeningMu.Lock()
		pending = sourceOpening[path]
		sourceOpeningMu.Unlock()
		if pending != nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("packet read returned before cancellation: %v", err)
		case <-started.C:
		case <-deadline.C:
			t.Fatal("packet read never started opening the source")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("packet read ignored cancellation while opening the source")
	}
}

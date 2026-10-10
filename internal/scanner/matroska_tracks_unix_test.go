//go:build unix

package scanner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
)

// Opening a FIFO with no writer blocks, the way a read on stalled network
// storage does. The read is abandoned instead of holding the caller.
func TestReadMatroskaTracksAbandonsStalledRead(t *testing.T) {
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
	if _, _, err := readMatroskaTracks(context.Background(), path); !errors.Is(err, errMatroskaTracksReadTimeout) {
		t.Fatalf("err = %v, want a read timeout", err)
	}

	matroskaTracksReadTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := readMatroskaTracks(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
}

// A read abandoned on stalled storage keeps its slot until the kernel
// returns; with every slot held, further reads are refused instead of piling
// up more stuck goroutines and open files.
func TestReadMatroskaTracksCapsStalledReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stalled.mkv")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	savedSlots, savedTimeout := matroskaTracksReadSlots, matroskaTracksReadTimeout
	matroskaTracksReadSlots = make(chan struct{}, 1)
	matroskaTracksReadTimeout = 20 * time.Millisecond
	t.Cleanup(func() { matroskaTracksReadSlots, matroskaTracksReadTimeout = savedSlots, savedTimeout })

	if _, _, err := readMatroskaTracks(context.Background(), path); !errors.Is(err, errMatroskaTracksReadTimeout) {
		t.Fatalf("first read: err = %v, want a timeout", err)
	}
	if _, _, err := readMatroskaTracks(context.Background(), "testdata/subtitles.mkv"); !errors.Is(err, errMatroskaTracksReadBusy) {
		t.Fatalf("read while the stalled one holds the slot: err = %v, want busy", err)
	}

	// Storage recovers: the stuck open returns and frees its slot.
	w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	matroskaTracksReadTimeout = time.Minute
	deadline := time.Now().Add(5 * time.Second)
	for len(matroskaTracksReadSlots) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("stalled read never released its slot")
		}
		time.Sleep(time.Millisecond)
	}
	if tracks, _, err := readMatroskaTracks(context.Background(), "testdata/subtitles.mkv"); err != nil || len(tracks) != 4 {
		t.Fatalf("read after recovery: %d tracks, err = %v", len(tracks), err)
	}
}

// A read that storage fails is reported as the storage error, not as a file
// that is not Matroska, so the backfill retries it instead of recording it.
// Reading a directory fails the way a read on broken storage does.
func TestReadMatroskaTracksReportsStorageReadErrors(t *testing.T) {
	_, info, err := readMatroskaTracks(context.Background(), t.TempDir())
	if info == nil {
		t.Fatalf("info = nil (err %v), want the opened directory's", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Fatalf("err = %v, want the read's *fs.PathError", err)
	}

	path := filepath.Join(t.TempDir(), "not-matroska.mkv")
	if err := os.WriteFile(path, []byte("RIFF\x00\x00\x00\x00AVI LIST"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readMatroskaTracks(context.Background(), path); !errors.Is(err, mediaprobe.ErrNotMatroska) {
		t.Fatalf("err = %v, want ErrNotMatroska for readable non-Matroska bytes", err)
	}
}

package uploads

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerCompletesChunkedUpload(t *testing.T) {
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      16,
		MaxChunkSize: 4,
		Now:          fixedNow(),
	})

	session, err := manager.Create(CreateRequest{
		Filename:  "plugin.bin",
		SizeBytes: 10,
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if session.TotalChunks != 3 {
		t.Fatalf("TotalChunks = %d, want 3", session.TotalChunks)
	}

	chunks := [][]byte{
		[]byte("abcd"),
		[]byte("efgh"),
		[]byte("ij"),
	}
	for index, chunk := range chunks {
		info, err := manager.PutChunk(context.Background(), session.ID, index, bytes.NewReader(chunk), int64(len(chunk)))
		if err != nil {
			t.Fatalf("PutChunk(%d) error = %v", index, err)
		}
		if info.ReceivedChunks != index+1 {
			t.Fatalf("ReceivedChunks after chunk %d = %d, want %d", index, info.ReceivedChunks, index+1)
		}
	}

	upload, err := manager.Complete(session.ID)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	defer upload.Cleanup()

	data, err := os.ReadFile(upload.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "abcdefghij" {
		t.Fatalf("assembled upload = %q, want %q", data, "abcdefghij")
	}
}

func TestManagerRejectsWrongChunkSizeWithoutCorruptingExistingData(t *testing.T) {
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      16,
		MaxChunkSize: 4,
		Now:          fixedNow(),
	})

	session, err := manager.Create(CreateRequest{
		Filename:  "plugin.bin",
		SizeBytes: 8,
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("PutChunk(0) error = %v", err)
	}
	if _, err := manager.PutChunk(context.Background(), session.ID, 1, bytes.NewReader([]byte("efghi")), -1); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("PutChunk oversized error = %v, want ErrInvalidChunk", err)
	}
	if _, err := manager.PutChunk(context.Background(), session.ID, 1, bytes.NewReader([]byte("efgh")), 4); err != nil {
		t.Fatalf("PutChunk retry error = %v", err)
	}

	upload, err := manager.Complete(session.ID)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	defer upload.Cleanup()

	data, err := os.ReadFile(upload.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "abcdefgh" {
		t.Fatalf("assembled upload = %q, want %q", data, "abcdefgh")
	}
}

func TestManagerRequiresAllChunksBeforeComplete(t *testing.T) {
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      16,
		MaxChunkSize: 4,
		Now:          fixedNow(),
	})

	session, err := manager.Create(CreateRequest{
		Filename:  "plugin.bin",
		SizeBytes: 8,
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("PutChunk(0) error = %v", err)
	}
	if _, err := manager.Complete(session.ID); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Complete() error = %v, want ErrIncomplete", err)
	}
}

func TestManagerAcceptsDuplicateReceivedChunk(t *testing.T) {
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      8,
		MaxChunkSize: 4,
		Now:          fixedNow(),
	})

	session, err := manager.Create(CreateRequest{
		Filename:  "plugin.bin",
		SizeBytes: 4,
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	info, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("abcd")), 4)
	if err != nil {
		t.Fatalf("PutChunk(0) error = %v", err)
	}
	info, err = manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("zzzz")), 4)
	if err != nil {
		t.Fatalf("PutChunk duplicate error = %v", err)
	}
	if info.ReceivedChunks != 1 || info.ReceivedBytes != 4 {
		t.Fatalf("duplicate changed progress: chunks=%d bytes=%d", info.ReceivedChunks, info.ReceivedBytes)
	}

	upload, err := manager.Complete(session.ID)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	defer upload.Cleanup()

	data, err := os.ReadFile(upload.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "abcd" {
		t.Fatalf("duplicate chunk changed data to %q", data)
	}
}

func TestManagerChunkActivityRefreshesExpiry(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      16,
		MaxChunkSize: 4,
		TTL:          10 * time.Minute,
		Now:          func() time.Time { return now },
	})

	session, err := manager.Create(CreateRequest{Filename: "plugin.bin", SizeBytes: 8, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 9 minutes per chunk: an absolute deadline would expire before the
	// second chunk; an idle timeout refreshed by chunk arrivals must not.
	now = now.Add(9 * time.Minute)
	if _, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("PutChunk(0) error = %v", err)
	}
	now = now.Add(9 * time.Minute)
	if _, err := manager.PutChunk(context.Background(), session.ID, 1, bytes.NewReader([]byte("efgh")), 4); err != nil {
		t.Fatalf("PutChunk(1) after refresh error = %v", err)
	}
	if _, err := manager.Complete(session.ID); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
}

func TestManagerReclaimsOrphanedDirsFromPreviousProcess(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "deadbeefdeadbeefdeadbeefdeadbeef")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "upload.bin"), []byte("leftover"), 0o600); err != nil {
		t.Fatalf("write stale spool: %v", err)
	}

	// A "restarted" manager knows nothing about the stale dir; reclaim must
	// remove it while leaving live sessions untouched.
	manager := NewManager(ManagerOptions{RootDir: root, MaxSize: 16, MaxChunkSize: 4, Now: fixedNow()})
	session, err := manager.Create(CreateRequest{Filename: "plugin.bin", SizeBytes: 4, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if removed := manager.ReclaimOrphanedDirs(); removed != 1 {
		t.Fatalf("ReclaimOrphanedDirs() = %d, want 1", removed)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale dir still exists: %v", err)
	}
	if _, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("live session broken after reclaim: %v", err)
	}
}

func TestManagerRejectsConcurrentWritesToSameChunk(t *testing.T) {
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      16,
		MaxChunkSize: 4,
		Now:          fixedNow(),
	})
	session, err := manager.Create(CreateRequest{Filename: "plugin.bin", SizeBytes: 4, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// First writer blocks mid-body (outside the manager lock); a second
	// writer for the same chunk must be turned away, and writers for other
	// sessions must not be blocked behind the stalled copy.
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := manager.PutChunk(context.Background(), session.ID, 0, &gatedReader{
			started: firstStarted,
			release: release,
			data:    []byte("abcd"),
		}, 4)
		done <- err
	}()
	<-firstStarted

	if _, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader([]byte("zzzz")), 4); !errors.Is(err, ErrChunkBusy) {
		t.Fatalf("concurrent same-chunk PutChunk error = %v, want ErrChunkBusy", err)
	}

	other, err := manager.Create(CreateRequest{Filename: "other.bin", SizeBytes: 4, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}
	if _, err := manager.PutChunk(context.Background(), other.ID, 0, bytes.NewReader([]byte("wxyz")), 4); err != nil {
		t.Fatalf("other session blocked behind stalled writer: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("stalled writer error = %v", err)
	}
	upload, err := manager.Complete(session.ID)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	defer upload.Cleanup()
	data, err := os.ReadFile(upload.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "abcd" {
		t.Fatalf("assembled upload = %q, want %q", data, "abcd")
	}
}

// gatedReader signals when Read is first called and then blocks until
// released, letting tests hold a chunk body copy open mid-flight.
type gatedReader struct {
	started chan struct{}
	release chan struct{}
	data    []byte
	signal  bool
	offset  int
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if !g.signal {
		g.signal = true
		close(g.started)
		<-g.release
	}
	if g.offset >= len(g.data) {
		return 0, io.EOF
	}
	n := copy(p, g.data[g.offset:])
	g.offset += n
	return n, nil
}

func TestManagerCountsDetachedWritersUntilTheyFinish(t *testing.T) {
	manager := NewManager(ManagerOptions{
		RootDir:      t.TempDir(),
		MaxSize:      16,
		MaxChunkSize: 4,
		Now:          fixedNow(),
	})
	session, err := manager.Create(CreateRequest{Filename: "plugin.bin", SizeBytes: 4, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Stall a chunk write, then cancel the session out from under it — the
	// cancel-and-recreate pattern a client uses to replace an upload. The
	// stalled writer still holds a connection and the spool file, so it must
	// stay visible to admission caps until it finishes.
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := manager.PutChunk(context.Background(), session.ID, 0, &gatedReader{
			started: firstStarted,
			release: release,
			data:    []byte("abcd"),
		}, 4)
		done <- err
	}()
	<-firstStarted

	if err := manager.Cancel(session.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got := manager.DetachedWriterSessions(); got != 1 {
		t.Fatalf("DetachedWriterSessions() during write = %d, want 1", got)
	}

	close(release)
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("stalled writer error = %v, want ErrNotFound", err)
	}
	if got := manager.DetachedWriterSessions(); got != 0 {
		t.Fatalf("DetachedWriterSessions() after writer drained = %d, want 0", got)
	}
}

func fixedNow() func() time.Time {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		return now
	}
}

package uploads

import (
	"bytes"
	"context"
	"errors"
	"os"
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

func fixedNow() func() time.Time {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		return now
	}
}

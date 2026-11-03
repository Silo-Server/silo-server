//go:build unix

package uploads

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

const orphanID = "deadbeefdeadbeefdeadbeefdeadbeef"

func TestManagerRefusesUploadRootsOthersControl(t *testing.T) {
	uid := os.Geteuid()
	dirWithMode := func(mode os.FileMode) func(*testing.T, string) string {
		return func(t *testing.T, root string) string {
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			// Chmod, not Mkdir's mode: the umask would clear the write bits under test.
			if err := os.Chmod(root, mode); err != nil {
				t.Fatal(err)
			}
			return root
		}
	}
	tests := []struct {
		name string
		// prepare builds the root and returns the directory a sweep through it
		// would reach.
		prepare func(*testing.T, string) string
		uid     int
		refused bool
	}{
		{name: "missing root is created", uid: uid},
		{name: "existing root this user owns", prepare: dirWithMode(0o755), uid: uid},
		{name: "owned by another user", prepare: dirWithMode(0o700), uid: uid + 1, refused: true},
		{name: "group writable", prepare: dirWithMode(0o770), uid: uid, refused: true},
		{name: "world writable with sticky bit", prepare: dirWithMode(0o777 | os.ModeSticky), uid: uid, refused: true},
		{name: "symlink to a directory the service owns", prepare: func(t *testing.T, root string) string {
			victim := filepath.Join(t.TempDir(), "service-data")
			if err := os.Mkdir(victim, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, root); err != nil {
				t.Fatal(err)
			}
			return victim
		}, uid: uid, refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "silo-uploads")
			var swept string
			if tt.prepare != nil {
				swept = tt.prepare(t, root)
			}
			// A directory named like a session, left where a sweep would reach it.
			var orphan string
			if swept != "" {
				orphan = filepath.Join(swept, orphanID)
				if err := os.Mkdir(orphan, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			manager := NewManager(ManagerOptions{RootDir: root, MaxSize: 16, MaxChunkSize: 4, Now: fixedNow()})
			manager.uid = tt.uid

			removed := manager.ReclaimOrphanedDirs()
			_, createErr := manager.Create(CreateRequest{SizeBytes: 4, ChunkSize: 4})

			wantRemoved := 0
			if orphan != "" {
				_, err := os.Stat(orphan)
				if tt.refused && err != nil {
					t.Fatalf("sweep through a refused root removed %s: %v", orphan, err)
				}
				if !tt.refused {
					wantRemoved = 1
				}
			}
			if removed != wantRemoved {
				t.Fatalf("ReclaimOrphanedDirs() = %d, want %d", removed, wantRemoved)
			}
			if tt.refused && createErr == nil {
				t.Fatal("Create() accepted an upload root another account could control")
			}
			if !tt.refused && createErr != nil {
				t.Fatalf("Create() error = %v", createErr)
			}
			if tt.prepare == nil {
				info, err := os.Stat(root)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != 0o700 {
					t.Fatalf("created root mode = %#o, want 0700", got)
				}
			}
		})
	}
}

// TestManagerKeepsChunkIOOnTheCreatedFile swaps what a session's name points at
// after Create, as an account controlling the spool could, and checks no
// chunk write lands in, and no completed upload reads from, anything but the
// file Create made.
func TestManagerKeepsChunkIOOnTheCreatedFile(t *testing.T) {
	const victimContent = "SERVICE-OWNED"
	swapSessionDir := func(t *testing.T, sessionDir, victimDir string) {
		if err := os.Rename(sessionDir, sessionDir+".aside"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victimDir, sessionDir); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		// swap replaces the session at sessionDir and returns the file a
		// redirected write or read would reach.
		swap func(t *testing.T, sessionDir, victimDir string) string
		// atComplete swaps after every chunk landed instead of before the
		// first chunk.
		atComplete bool
	}{
		{name: "session dir swapped for a symlink before a chunk", swap: func(t *testing.T, sessionDir, victimDir string) string {
			swapSessionDir(t, sessionDir, victimDir)
			return filepath.Join(victimDir, "upload.bin")
		}},
		{name: "session file swapped for a symlink before a chunk", swap: func(t *testing.T, sessionDir, victimDir string) string {
			file := filepath.Join(sessionDir, "upload.bin")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(victimDir, "upload.bin")
			if err := os.Symlink(victim, file); err != nil {
				t.Fatal(err)
			}
			return victim
		}},
		{name: "session file replaced inside the root before a chunk", swap: func(t *testing.T, sessionDir, _ string) string {
			file := filepath.Join(sessionDir, "upload.bin")
			if err := os.Rename(file, file+".aside"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(victimContent), 0o600); err != nil {
				t.Fatal(err)
			}
			return file
		}},
		{name: "session dir swapped for a symlink before complete", atComplete: true, swap: func(t *testing.T, sessionDir, victimDir string) string {
			swapSessionDir(t, sessionDir, victimDir)
			return filepath.Join(victimDir, "upload.bin")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			victimDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(victimDir, "upload.bin"), []byte(victimContent), 0o600); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(ManagerOptions{RootDir: root, MaxSize: 16, MaxChunkSize: 16, Now: fixedNow()})
			session, err := manager.Create(CreateRequest{SizeBytes: int64(len(victimContent)), ChunkSize: 16})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			sessionDir := filepath.Join(root, session.ID)
			chunk := []byte("UPLOADED-BYTES")[:len(victimContent)]

			var victim string
			var ioErr error
			if !tt.atComplete {
				victim = tt.swap(t, sessionDir, victimDir)
				_, ioErr = manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader(chunk), int64(len(chunk)))
			} else {
				if _, err := manager.PutChunk(context.Background(), session.ID, 0, bytes.NewReader(chunk), int64(len(chunk))); err != nil {
					t.Fatalf("PutChunk() error = %v", err)
				}
				victim = tt.swap(t, sessionDir, victimDir)
				var upload *CompletedUpload
				if upload, ioErr = manager.Complete(session.ID); ioErr == nil {
					defer upload.Cleanup()
				}
			}
			got, err := os.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != victimContent {
				t.Fatalf("victim file now holds %q, want %q", got, victimContent)
			}
			if ioErr == nil {
				t.Fatal("chunk write or complete went through a swapped session")
			}
		})
	}
}

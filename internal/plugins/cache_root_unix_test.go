//go:build unix

package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsurePrivateCacheRootRefusesRootsOthersControl(t *testing.T) {
	uid := os.Geteuid()
	dirWithMode := func(mode os.FileMode) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			// Chmod, not Mkdir's mode: the umask would clear the write bits under test.
			if err := os.Chmod(root, mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		uid     int
		wantErr string
	}{
		{name: "missing root is created", uid: uid},
		{name: "existing root this user owns", prepare: dirWithMode(0o755), uid: uid},
		{name: "owned by another user", prepare: dirWithMode(0o700), uid: uid + 1, wantErr: "is owned by uid"},
		{name: "group writable", prepare: dirWithMode(0o770), uid: uid, wantErr: "writable by its group or other users"},
		{name: "world writable with sticky bit", prepare: dirWithMode(0o777 | os.ModeSticky), uid: uid, wantErr: "writable by its group or other users"},
		{name: "symlink to a private dir", prepare: func(t *testing.T, root string) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, root); err != nil {
				t.Fatal(err)
			}
		}, uid: uid, wantErr: "is a symlink"},
		{name: "regular file", prepare: func(t *testing.T, root string) {
			if err := os.WriteFile(root, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, uid: uid, wantErr: "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "silo-plugins")
			if tt.prepare != nil {
				tt.prepare(t, root)
			}
			err := ensurePrivateCacheRoot(root, tt.uid)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ensurePrivateCacheRoot() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensurePrivateCacheRoot() error = %v", err)
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

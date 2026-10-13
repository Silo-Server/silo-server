package librarymonitor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClassifyFSType(t *testing.T) {
	tests := []struct {
		name    string
		fsType  int64
		want    fsSupport
		display string
	}{
		{"ext4", 0xef53, fsMonitored, ""},
		{"xfs", 0x58465342, fsMonitored, ""},
		{"btrfs", 0x9123683e, fsMonitored, ""},
		{"zfs", 0x2fc12fc1, fsMonitored, ""},
		{"tmpfs", 0x01021994, fsMonitored, ""},
		{"nfs", 0x6969, fsUnsupported, "NFS"},
		{"smb", 0x517b, fsUnsupported, "SMB"},
		{"cifs", 0xff534d42, fsUnsupported, "CIFS"},
		{"smb2", 0xfe534d42, fsUnsupported, "SMB2"},
		{"cephfs", 0x00c36400, fsUnsupported, "CephFS"},
		{"9p", 0x01021997, fsUnsupported, "9p"},
		{"fuse and virtiofs", 0x65735546, fsMonitoredCaveat, "FUSE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFSType(tt.fsType)
			if got.Support != tt.want || got.Name != tt.display {
				t.Fatalf("classifyFSType(%#x) = %+v, want support %d name %q", tt.fsType, got, tt.want, tt.display)
			}
		})
	}
}

func TestIgnoredName(t *testing.T) {
	for name, want := range map[string]bool{
		"movie.mkv":           false,
		"movie.mkv.partial":   true,
		"movie.mkv.partial~":  true,
		"movie.mkv.part":      true,
		"movie.PART":          true,
		"x.tmp":               true,
		"movie.mkv.!qB":       true,
		"@eaDir":              true,
		"#recycle":            true,
		".RecycleBin":         true,
		".Trash-1000":         true,
		"lost+found":          true,
		"Season 1":            false,
		"movie.partial.mkv":   false,
		"trash-not-hidden.nf": false,
	} {
		if got := ignoredName(name); got != want {
			t.Errorf("ignoredName(%q) = %v, want %v", name, got, want)
		}
	}
	if !ignoredDir(".downloads") || ignoredName(".downloads") {
		t.Errorf("scanner-skipped directory names apply to directories only")
	}
}

func TestWalkTreeSkipsWhatTheScannerSkips(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root,
		"Movie A",
		"Movie A/Extras",
		"NoMedia/Inside",
		"EmptyIgnore/Inside",
		"InvalidIgnore/Inside",
		"PatternIgnore/Inside",
		"@eaDir/thumbs",
		"Downloading.partial",
		".downloads",
		"Target/Deep",
	)
	writeFile(t, filepath.Join(root, "NoMedia", ".nomedia"), "")
	writeFile(t, filepath.Join(root, "EmptyIgnore", ".ignore"), "# only a comment\n\n")
	// Only invalid patterns: the scanner drops them and skips the folder.
	writeFile(t, filepath.Join(root, "InvalidIgnore", ".ignore"), "[\n")
	writeFile(t, filepath.Join(root, "PatternIgnore", ".ignore"), "*.nfo\n")
	// A symlinked directory is followed, and a loop back to the root is cut.
	if err := os.Symlink(filepath.Join(root, "Target"), filepath.Join(root, "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "Target", "Loop")); err != nil {
		t.Fatal(err)
	}

	var entered []string
	err := walkTree(context.Background(), root, walkVisitor{
		enter: func(dir string, link bool) (bool, error) {
			if link {
				entered = append(entered, rel(t, root, dir)+" (link)")
			} else {
				entered = append(entered, rel(t, root, dir))
			}
			return true, nil
		},
		listed: func(dir string, skipped bool) bool {
			if skipped {
				entered = append(entered, "skip "+rel(t, root, dir))
			}
			return skipped
		},
	})
	if err != nil {
		t.Fatalf("walkTree: %v", err)
	}
	want := []string{
		".",
		"EmptyIgnore", "skip EmptyIgnore",
		"InvalidIgnore", "skip InvalidIgnore",
		"Linked (link)", "Linked/Deep",
		"Movie A", "Movie A/Extras",
		"NoMedia", "skip NoMedia",
		"PatternIgnore", "PatternIgnore/Inside",
	}
	if !reflect.DeepEqual(entered, want) {
		t.Fatalf("entered = %q\nwant      %q", entered, want)
	}
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rel(t *testing.T, root, path string) string {
	t.Helper()
	r, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(r)
}

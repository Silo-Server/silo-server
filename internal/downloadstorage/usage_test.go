package downloadstorage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureCountsFilesByKind(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "art1.mp4", 1000)
	writeFile(t, dir, "12_transcode_abcdef_art2.mp4", 500)
	writeFile(t, dir, "art3.mp4.part", 300)
	writeFile(t, dir, "art1.mp4.receipt.json", 40)
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "nested"), "ignored.mp4", 9999)

	now := time.Unix(1_700_000_000, 0)
	u := Measure(dir, "", now)
	if u.Error != "" {
		t.Fatalf("Measure error: %s", u.Error)
	}
	if u.Files != 2 || u.Bytes != 1500 {
		t.Fatalf("complete = %d files / %d bytes, want 2 / 1500", u.Files, u.Bytes)
	}
	if u.PartialFiles != 1 || u.PartialBytes != 300 {
		t.Fatalf("partial = %d files / %d bytes, want 1 / 300", u.PartialFiles, u.PartialBytes)
	}
	if u.OtherBytes != 40 || u.TotalBytes() != 1840 {
		t.Fatalf("other = %d, total = %d, want 40 / 1840", u.OtherBytes, u.TotalBytes())
	}
	if !u.MeasuredAt.Equal(now) || u.Dir != dir {
		t.Fatalf("measured at %v in %q", u.MeasuredAt, u.Dir)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if u.FSTotalBytes <= 0 || u.FSUsedBytes < 0 || u.FSUsedBytes > u.FSTotalBytes {
			t.Fatalf("filesystem used/total = %d/%d", u.FSUsedBytes, u.FSTotalBytes)
		}
	}
}

func TestMeasureMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	parent := t.TempDir()
	u := Measure(filepath.Join(parent, "not-created-yet"), parent, time.Now())
	if u.Error != "" {
		t.Fatalf("missing directory reported an error: %s", u.Error)
	}
	if u.Files != 0 || u.TotalBytes() != 0 {
		t.Fatalf("missing directory reported contents: %+v", u)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if u.FSTotalBytes <= 0 {
			t.Fatal("missing directory should report the filesystem it will be created on")
		}
		if !u.SharesScratch {
			t.Fatal("a directory under the scratch dir's filesystem must report SharesScratch")
		}
	}
}

func TestMeasureUnreadableDirectoryReportsError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	writeFile(t, dir, "a-file", 1)
	u := Measure(file, "", time.Now())
	if u.Error == "" {
		t.Fatal("measuring a regular file as a directory must report an error")
	}
	// A node serves the error on its unauthenticated health check.
	if strings.Contains(u.Error, dir) {
		t.Fatalf("error %q names the directory", u.Error)
	}
}

func TestFileKind(t *testing.T) {
	for name, want := range map[string]string{
		"x.mp4":                    KindComplete,
		"1_remux_ab_x.mp4":         KindComplete,
		"x.mp4.part":               KindPartial,
		"x.mp4.receipt.json":       KindOther,
		"x.mp4.receipt.json.12345": KindOther,
		"notes.txt":                KindOther,
	} {
		if got := fileKind(name); got != want {
			t.Errorf("fileKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEphemeralFS(t *testing.T) {
	for fsType, want := range map[string]bool{"tmpfs": true, "overlay": true, "ext4": false, "": false, "nfs": false} {
		if got := ephemeralFS(fsType); got != want {
			t.Errorf("ephemeralFS(%q) = %v, want %v", fsType, got, want)
		}
	}
}

func TestRedactPathDropsDirectory(t *testing.T) {
	u := Usage{Dir: "/srv/x", Files: 3}
	if r := u.RedactPath(); r.Dir != "" || r.Files != 3 {
		t.Fatalf("RedactPath = %+v", r)
	}
}

package fspath

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"unicode/utf8"
)

func TestEncodeLeavesValidUTF8Unchanged(t *testing.T) {
	for _, path := range []string{
		"",
		"/media/movies/Heat (1995)/Heat (1995).mkv",
		"/media/tv/Bård Ylvisåker/Season 01/S01E01 – ♫.mkv",
		"/media/anime/進撃の巨人/第1話.mkv",
		"/media/odd/� replacement and emoji 🎬.mkv",
	} {
		if got := Encode(path); got != path {
			t.Errorf("Encode(%q) = %q, want it unchanged", path, got)
		}
		if got := Decode(path); got != path {
			t.Errorf("Decode(%q) = %q, want it unchanged", path, got)
		}
		if IsEncoded(path) {
			t.Errorf("IsEncoded(%q) = true", path)
		}
	}
}

func TestEncodeRoundTrips(t *testing.T) {
	for _, raw := range []string{
		"/mnt/media/Takk for alt B\xe5rd Ylvis\xe5ker.mkv",
		"\xff\xfe\x80\x81",
		"truncated UTF-8 \xe2\x82",
		"mixed \xe5 and å and \xc3",
		// A reserved code point really present in a name is escaped too.
		"reserved \U0010FEE5 code point",
		"overlong \xc0\xaf slash",
	} {
		encoded := Encode(raw)
		if !utf8.ValidString(encoded) {
			t.Errorf("Encode(%q) = %q, not valid UTF-8", raw, encoded)
		}
		if strings.ContainsRune(encoded, utf8.RuneError) && !strings.ContainsRune(raw, utf8.RuneError) {
			t.Errorf("Encode(%q) = %q lost bytes to U+FFFD", raw, encoded)
		}
		if got := Decode(encoded); got != raw {
			t.Errorf("Decode(Encode(%q)) = %q", raw, got)
		}
		if !IsEncoded(encoded) {
			t.Errorf("IsEncoded(Encode(%q)) = false", raw)
		}
		// Encoded paths survive JSON, which replaces invalid bytes in raw ones.
		data, err := json.Marshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var back string
		if err := json.Unmarshal(data, &back); err != nil || Decode(back) != raw {
			t.Errorf("JSON round trip of %q gave %q (%v)", raw, back, err)
		}
	}
}

func TestEncodePreservesPathStructure(t *testing.T) {
	dir := "/mnt/media/Takk for alt (2023)"
	raw := dir + "/Season 01/B\xe5rd.mkv"
	encoded := Encode(raw)
	if !strings.HasPrefix(encoded, dir+"/") {
		t.Fatalf("Encode(%q) = %q, lost the valid prefix", raw, encoded)
	}
	if got, want := filepath.Dir(encoded), dir+"/Season 01"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	if got := filepath.Base(encoded); Decode(got) != "B\xe5rd.mkv" {
		t.Fatalf("Base = %q does not decode to the raw name", got)
	}
	if strings.Count(encoded, "/") != strings.Count(raw, "/") {
		t.Fatalf("Encode changed the number of separators: %q", encoded)
	}
}

func TestDisplayReadsEscapedBytesAsWindows1252(t *testing.T) {
	for raw, want := range map[string]string{
		"Takk for alt B\xe5rd Ylvis\xe5ker": "Takk for alt Bård Ylvisåker",
		"Caf\xe9 \x96 \x93Quoted\x94 \x80":  "Café – “Quoted” €",
		"undefined \x81 byte":               "undefined � byte",
		"already UTF-8 Bård":                "already UTF-8 Bård",
	} {
		if got := Display(Encode(raw)); got != want {
			t.Errorf("Display(Encode(%q)) = %q, want %q", raw, got, want)
		}
	}
}

func TestOSWrappersOpenEncodedPaths(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "S\xe6son 1")
	rawFile := filepath.Join(rawDir, "Episode B\xe5rd.mkv")
	if err := os.Mkdir(rawDir, 0o755); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	if err := os.WriteFile(rawFile, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rawFile, filepath.Join(dir, "link.mkv")); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadDir(Encode(rawDir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != Encode("Episode B\xe5rd.mkv") {
		t.Fatalf("ReadDir entries = %v, want the encoded name", entries)
	}
	info, err := entries[0].Info()
	if err != nil || info.Name() != entries[0].Name() {
		t.Fatalf("entry Info name = %v (%v), want %q", info, err, entries[0].Name())
	}

	encoded := filepath.Join(Encode(rawDir), entries[0].Name())
	if encoded != Encode(rawFile) {
		t.Fatalf("joined path %q, want %q", encoded, Encode(rawFile))
	}
	if info, err := Stat(encoded); err != nil || info.Name() != entries[0].Name() {
		t.Fatalf("Stat = %v, %v", info, err)
	}
	if _, err := Lstat(encoded); err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	data, err := ReadFile(encoded)
	if err != nil || string(data) != "video" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	f, err := Open(encoded)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	opened, err := f.Stat()
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	lstat, _ := Lstat(encoded)
	if !SameFile(lstat, opened) || !SameFile(info, lstat) {
		t.Fatal("SameFile is false for the same file seen through Lstat, Open, and ReadDir")
	}
	if other, err := Stat(dir); err != nil || SameFile(lstat, other) {
		t.Fatalf("SameFile is true for a different file (%v)", err)
	}
	if target, err := Readlink(filepath.Join(dir, "link.mkv")); err != nil || target != encoded {
		t.Fatalf("Readlink = %q, %v; want %q", target, err, encoded)
	}
	resolved, err := EvalSymlinks(filepath.Join(dir, "link.mkv"))
	if err != nil || resolved != encoded {
		t.Fatalf("EvalSymlinks = %q, %v; want %q", resolved, err, encoded)
	}

	out, err := CommandContext(context.Background(), "cat", encoded).Output()
	if err != nil || !bytes.Equal(out, []byte("video")) {
		t.Fatalf("cat via CommandContext = %q, %v", out, err)
	}
}

func TestDecodeAllKeepsUnchangedSlice(t *testing.T) {
	args := []string{"-i", "/media/plain.mkv", "-f", "null"}
	if got := DecodeAll(args); &got[0] != &args[0] {
		t.Fatal("DecodeAll copied arguments that needed no decoding")
	}
	encoded := Encode("/media/B\xe5rd.mkv")
	got := DecodeAll([]string{"-i", encoded, "subtitles=filename='" + encoded + "'"})
	if got[1] != "/media/B\xe5rd.mkv" || got[2] != "subtitles=filename='/media/B\xe5rd.mkv'" {
		t.Fatalf("DecodeAll = %q", got)
	}
}

// A wedged mount must not pin the callers: each returns when its own context
// ends, and callers for the same path share one blocked stat instead of
// stranding a goroutine per request.
func TestStatContextReturnsWhenContextEndsWhileStatBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		orig := osStat
		osStat = func(name string) (os.FileInfo, error) {
			calls.Add(1)
			<-release
			return nil, os.ErrNotExist
		}
		defer func() { osStat = orig }()
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()

		const path = "/media/wedged/movie.mkv"
		first, cancelFirst := context.WithCancel(context.Background())
		defer cancelFirst()
		firstDone := make(chan error, 1)
		go func() {
			_, err := StatContext(first, path)
			firstDone <- err
		}()
		secondDone := make(chan error, 1)
		go func() {
			_, err := StatContext(context.Background(), path)
			secondDone <- err
		}()
		synctest.Wait()
		if n := calls.Load(); n != 1 {
			t.Fatalf("stat ran %d times for one path, want the callers to share one", n)
		}

		cancelFirst()
		synctest.Wait()
		select {
		case err := <-firstDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("StatContext error = %v, want context.Canceled", err)
			}
		default:
			t.Fatal("StatContext still waiting on the blocked stat after its context ended")
		}
		select {
		case err := <-secondDone:
			t.Fatalf("caller with a live context returned %v before the stat did", err)
		default:
		}

		close(release)
		released = true
		if err := <-secondDone; !os.IsNotExist(err) {
			t.Fatalf("shared StatContext result = %v, want the stat's not-exist error", err)
		}
	})
}

// Cancel just after the entry check observes a live context, so completion
// and cancellation are both ready when StatContext starts waiting.
type cancelAfterErrCheckContext struct {
	context.Context //nolint:containedctx // Wrap a real context to cancel immediately after the entry check.
	cancel          context.CancelFunc
}

func (c *cancelAfterErrCheckContext) Err() error {
	err := c.Context.Err()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	return err
}

func TestStatContextPrefersCancellationToCompletedStat(t *testing.T) {
	const path = "/media/completed/movie.mkv"
	done := make(chan struct{})
	close(done)
	statCallsMu.Lock()
	statCalls[path] = &statCall{done: done, err: os.ErrNotExist}
	statCallsMu.Unlock()
	t.Cleanup(func() {
		statCallsMu.Lock()
		delete(statCalls, path)
		statCallsMu.Unlock()
	})

	// Exercise both select branches without relying on scheduler timing.
	for range 64 {
		ctx, cancel := context.WithCancel(t.Context())
		_, err := StatContext(&cancelAfterErrCheckContext{Context: ctx, cancel: cancel}, path)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StatContext error = %v, want context.Canceled instead of the completed stat", err)
		}
	}
}

package nodesource

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
)

// TestRestrictArgsHoldsOnlyRelayedInputs puts the relay's protocols and the
// admitted demuxers just before each relayed "-i", released or unknown relay
// URLs included, and leaves any argv without one as it was.
func TestRestrictArgsHoldsOnlyRelayedInputs(t *testing.T) {
	url := openRemote(t, newTestRelay(t, newFakeCore(t, 1000), ModeCore)).Input()
	restrictions := inputRestrictions

	for name, args := range map[string][]string{
		"local path":        {"-v", "error", "-ss", "3", "-i", "/media/movie.mkv", "-f", "null", "-"},
		"url not an input":  {"-i", "/media/movie.mkv", "-metadata", "title=" + url, "-"},
		"pipe and lavfi":    {"-f", "lavfi", "-i", "testsrc", "-i", "pipe:0", "-f", "null", "-"},
		"positional ignore": {"-v", "error", url},
	} {
		got := RestrictArgs(args)
		if !slices.Equal(got, args) || &got[0] != &args[0] {
			t.Errorf("%s: RestrictArgs changed a local argv: %q", name, got)
		}
	}

	args := []string{"-hide_banner", "-ss", "3", "-i", url, "-i", "/media/subs.mkv", "-c", "copy", "-"}
	want := slices.Concat([]string{"-hide_banner", "-ss", "3"}, restrictions, []string{"-i", url, "-i", "/media/subs.mkv", "-c", "copy", "-"})
	if got := RestrictArgs(args); !slices.Equal(got, want) {
		t.Fatalf("RestrictArgs\n got %q\nwant %q", got, want)
	}
	// A relay URL whose handle is unknown or released fails closed.
	unknown := "http://127.0.0.1:1/src/00000000000000000000000000000000"
	for _, input := range []string{unknown, releasedURL(t)} {
		args := []string{"-i", input, "-f", "null", "-"}
		want := slices.Concat(restrictions, args)
		if got := RestrictArgs(args); !slices.Equal(got, want) {
			t.Fatalf("unregistered relay URL\n got %q\nwant %q", got, want)
		}
		if got := RestrictProbeArgs([]string{"-v", "error", input}); !slices.Equal(got, slices.Concat([]string{"-v", "error"}, restrictions, []string{input})) {
			t.Fatalf("unregistered relay URL probe: %q", got)
		}
	}
	two := []string{"-i", url, "-i", url, "-"}
	want = slices.Concat(restrictions, []string{"-i", url}, restrictions, []string{"-i", url, "-"})
	if got := RestrictArgs(two); !slices.Equal(got, want) {
		t.Fatalf("two relayed inputs\n got %q\nwant %q", got, want)
	}
}

// TestRestrictProbeArgsPlacesOptionsBeforeThePositionalInput restricts an
// ffprobe input given positionally, just before it, as well as after "-i".
func TestRestrictProbeArgsPlacesOptionsBeforeThePositionalInput(t *testing.T) {
	url := openRemote(t, newTestRelay(t, newFakeCore(t, 1000), ModeCore)).Input()
	restrictions := inputRestrictions

	local := []string{"-v", "error", "-show_streams", "-of", "json", "/media/movie.mkv"}
	if got := RestrictProbeArgs(local); !slices.Equal(got, local) {
		t.Fatalf("RestrictProbeArgs changed a local argv: %q", got)
	}
	positional := []string{"-v", "error", "-show_streams", "-of", "json", url}
	want := slices.Concat([]string{"-v", "error", "-show_streams", "-of", "json"}, restrictions, []string{url})
	if got := RestrictProbeArgs(positional); !slices.Equal(got, want) {
		t.Fatalf("positional\n got %q\nwant %q", got, want)
	}
	flagged := []string{"-v", "error", "-i", url, "-show_streams"}
	want = slices.Concat([]string{"-v", "error"}, restrictions, []string{"-i", url, "-show_streams"})
	if got := RestrictProbeArgs(flagged); !slices.Equal(got, want) {
		t.Fatalf("-i input\n got %q\nwant %q", got, want)
	}
}

// TestInputKeyAndLookup key a relayed source by its identity and anything
// else by itself, without touching the filesystem.
func TestInputKeyAndLookup(t *testing.T) {
	h := openRemote(t, newTestRelay(t, newFakeCore(t, 1000), ModeCore))
	if got := InputKey(h.Input()); got != h.Identity() {
		t.Fatalf("InputKey(relayed) = %q, want %q", got, h.Identity())
	}
	if got := InputKey("/does/not/exist.mkv"); got != "/does/not/exist.mkv" {
		t.Fatalf("InputKey(local) = %q", got)
	}
	if found, ok := Lookup(h.Input()); !ok || found != h {
		t.Fatalf("Lookup(relayed) = %v, %t", found, ok)
	}
	url := h.Input()
	_ = h.Close()
	if _, ok := Lookup(url); ok || InputKey(url) != url {
		t.Fatal("a closed handle is still registered")
	}
	// A released relay URL is no local path: it reads as released, never as
	// a missing file.
	if _, err := Stat(url); !errors.Is(err, ErrClosed) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(released) = %v, want ErrClosed", err)
	}
	if _, err := OpenReaderAt(t.Context(), url); !errors.Is(err, ErrClosed) {
		t.Fatalf("OpenReaderAt(released) = %v, want ErrClosed", err)
	}
	if _, err := Identity(url); !errors.Is(err, ErrClosed) {
		t.Fatalf("Identity(released) = %v, want ErrClosed", err)
	}
}

// releasedURL is the loopback URL of a handle that has been closed.
func releasedURL(t *testing.T) string {
	t.Helper()
	h := openRemote(t, newTestRelay(t, newFakeCore(t, 1000), ModeCore))
	url := h.Input()
	_ = h.Close()
	if IsRemote(url) {
		t.Fatal("a closed handle is still registered")
	}
	return url
}

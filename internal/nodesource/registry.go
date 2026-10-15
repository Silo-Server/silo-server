package nodesource

import (
	"context"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// registry finds the open core-access handle behind a loopback URL, so code
// that receives FFmpeg's input string (playback's Go-side consumers) can read
// a source without knowing how the node reaches it.
var registry = struct {
	sync.RWMutex
	byURL map[string]*Handle
}{byURL: make(map[string]*Handle)}

func registerURL(h *Handle) {
	registry.Lock()
	registry.byURL[h.url] = h
	registry.Unlock()
}

func unregisterURL(h *Handle) {
	registry.Lock()
	if registry.byURL[h.url] == h {
		delete(registry.byURL, h.url)
	}
	registry.Unlock()
}

// lookup returns the open handle registered at input, if input is one.
func lookup(input string) *Handle {
	registry.RLock()
	defer registry.RUnlock()
	return registry.byURL[input]
}

// released is the error for a relay URL whose handle is gone: its job
// released the source, so it can no longer be read. It is no local path
// either, and must not read as a missing file.
func released(input string) error {
	if !isLoopbackSourceURL(input) {
		return nil
	}
	return &fs.PathError{Op: opRead, Path: "source (released)", Err: ErrClosed}
}

// Stat describes input: a local path through os.Stat, a registered loopback
// URL from its handle's revision.
func Stat(input string) (os.FileInfo, error) {
	h := lookup(input)
	if h == nil {
		if err := released(input); err != nil {
			return nil, err
		}
		return os.Stat(input)
	}
	if err := h.Err(); err != nil {
		return nil, h.readError(err)
	}
	return sourceInfo{h: h}, nil
}

// OpenReaderAt opens input for random access: a local path as the file, a
// registered loopback URL through its handle (Handle.ReaderAt).
func OpenReaderAt(ctx context.Context, input string) (Reader, error) {
	h := lookup(input)
	if h == nil {
		if err := released(input); err != nil {
			return nil, err
		}
		return os.Open(input)
	}
	return h.ReaderAt(ctx)
}

// Identity names input's revision for caches and logs: a registered loopback
// URL by its handle's Identity, a local path by the path, size and
// modification time.
func Identity(input string) (string, error) {
	if h := lookup(input); h != nil {
		return h.Identity(), nil
	}
	if err := released(input); err != nil {
		return "", err
	}
	info, err := os.Stat(input)
	if err != nil {
		return "", err
	}
	return input + "|" + strconv.FormatInt(info.Size(), 10) + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10), nil
}

// IsRemote reports whether input is a registered loopback URL.
func IsRemote(input string) bool { return lookup(input) != nil }

// Lookup returns the open handle registered at input, when input is a
// loopback URL, for consumers that need more than Stat, OpenReaderAt and
// Identity give: its Context, to bound work done on the source's behalf.
func Lookup(input string) (*Handle, bool) {
	h := lookup(input)
	return h, h != nil
}

// InputKey names input in an in-process cache keyed today by the input
// string: a registered loopback URL, a new capability for every job, by its
// handle's Identity, so jobs on the same source share an entry; anything else
// by itself, without touching the filesystem.
func InputKey(input string) string {
	if h := lookup(input); h != nil {
		return h.Identity()
	}
	return input
}

// sourceInfo is a core-access source's os.FileInfo.
type sourceInfo struct{ h *Handle }

func (i sourceInfo) Name() string       { return "source " + i.h.etag }
func (i sourceInfo) Size() int64        { return i.h.size }
func (i sourceInfo) Mode() fs.FileMode  { return 0o444 }
func (i sourceInfo) ModTime() time.Time { return i.h.modTime }
func (i sourceInfo) IsDir() bool        { return false }
func (i sourceInfo) Sys() any           { return nil }

// loopbackSourceURL matches a relay URL: a temporary bearer capability for a
// source on the node host, which must never reach a log.
var loopbackSourceURL = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+/src/[0-9a-f]{32}`)

// IsRelayURL reports whether input is a relay URL, whether or not its handle
// is still open: a capability, never a local path.
func IsRelayURL(input string) bool { return isLoopbackSourceURL(input) }

// isLoopbackSourceURL reports whether input is a relay URL, registered or not.
func isLoopbackSourceURL(input string) bool {
	loc := loopbackSourceURL.FindStringIndex(input)
	return loc != nil && loc[0] == 0 && loc[1] == len(input)
}

// RedactLoopback replaces every relay URL in s with the identity of its
// source, or a placeholder once the handle is gone. Apply it to anything
// logged that can carry FFmpeg's input: argv, FFmpeg's own output, input
// fields.
func RedactLoopback(s string) string {
	if !strings.Contains(s, "/src/") {
		return s
	}
	return loopbackSourceURL.ReplaceAllStringFunc(s, func(url string) string {
		if h := lookup(url); h != nil {
			return "source " + h.Identity()
		}
		return "source (released)"
	})
}

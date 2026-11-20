package fspath

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// A transcode node that reads its sources through the core (core source
// access) gives FFmpeg and its Go readers a loopback URL instead of a library
// path: a relayed input. The functions here answer for such a URL the way
// they answer for a path, so the code that stats, reads and launches FFmpeg on
// a job's input works on either without knowing which it has. The relay
// itself lives in internal/nodesource, which registers itself here
// (RegisterRelayedSources); fspath does not depend on it.

// ErrRelayed is an *os.File open of a relayed input, which has no file:
// read it through OpenReader.
var ErrRelayed = errors.New("fspath: a relayed source has no file; use OpenReader")

// Reader is random access to a media source with its size and modification
// time. *os.File implements it.
type Reader interface {
	io.ReaderAt
	io.Closer
	Stat() (os.FileInfo, error)
}

// RelayedSources answers for relayed inputs. internal/nodesource implements
// it.
type RelayedSources interface {
	// Owns reports whether name is a relay URL, whether or not its source is
	// still open: a relay URL is never a local path.
	Owns(name string) bool
	// Stat describes the relayed source at name: a regular file of its
	// granted size and modification time.
	Stat(ctx context.Context, name string) (os.FileInfo, error)
	// OpenReader opens the relayed source at name for random reads, bounded
	// by ctx.
	OpenReader(ctx context.Context, name string) (Reader, error)
	// GuardArgs returns the arguments of an FFmpeg (probe false) or ffprobe
	// (probe true) launch with every relayed input restricted to the relay's
	// protocols and the admitted demuxers.
	GuardArgs(probe bool, args []string) []string
	// Redact replaces every relay URL in s with the name of its source.
	Redact(s string) string
}

var relayed atomic.Pointer[RelayedSources]

// RegisterRelayedSources installs the relay that answers for relayed inputs
// in this process. The last registration wins.
func RegisterRelayedSources(sources RelayedSources) {
	relayed.Store(&sources)
}

func relayedSources() RelayedSources {
	if p := relayed.Load(); p != nil {
		return *p
	}
	return nil
}

// IsRelayed reports whether name is a relayed input rather than a path.
func IsRelayed(name string) bool {
	r := relayedSources()
	return r != nil && r.Owns(name)
}

// RedactRelayed replaces every relay URL in s with the name of its source,
// so it can be logged or returned: a relay URL is a bearer capability on the
// node host. Without a registered relay s is returned as is.
func RedactRelayed(s string) string {
	if r := relayedSources(); r != nil {
		return r.Redact(s)
	}
	return s
}

// OpenReader opens name for random reads: a path as its file, a relayed
// input through the relay. ctx bounds a relayed reader's reads; a file is not
// tied to it. The caller closes the reader.
func OpenReader(ctx context.Context, name string) (Reader, error) {
	if r := relayedSources(); r != nil && r.Owns(name) {
		return r.OpenReader(ctx, name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return Open(name)
}

// relayedOpenError is the error an *os.File open of a relayed input returns.
// It does not name the URL.
func relayedOpenError(op string) error {
	return &fs.PathError{Op: op, Path: "relayed source", Err: ErrRelayed}
}

// isProbe reports whether binary is ffprobe, whose input is positional.
func isProbe(binary string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(binary)), "ffprobe")
}

// guardArgs restricts the relayed inputs of a launch of binary.
func guardArgs(binary string, args []string) []string {
	if r := relayedSources(); r != nil {
		return r.GuardArgs(isProbe(binary), args)
	}
	return args
}

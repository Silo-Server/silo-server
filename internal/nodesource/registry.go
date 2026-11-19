package nodesource

import (
	"context"
	"io/fs"
	"os"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// The relay answers for its URLs in internal/fspath, so the code that stats,
// reads and launches FFmpeg on a job's input (tone-map checks, keyframe and
// subtitle indexes, staging, every FFmpeg and ffprobe launch) works on a
// relay URL as on a library path.
func init() {
	fspath.RegisterRelayedSources(relayedSources{})
}

// relayedSources is the relay's fspath.RelayedSources.
type relayedSources struct{}

func (relayedSources) Owns(name string) bool { return IsRelayURL(name) }

// Stat describes the open source at a relay URL from its revision. A URL
// whose source is gone is no local path either, and must not read as a
// missing file.
func (relayedSources) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e := lookupRelay(name)
	if e == nil {
		return nil, released()
	}
	if err := e.Err(); err != nil {
		return nil, e.readError(err)
	}
	return sourceInfo{e: e}, nil
}

func (relayedSources) OpenReader(ctx context.Context, name string) (fspath.Reader, error) {
	e := lookupRelay(name)
	if e == nil {
		return nil, released()
	}
	return e.newReader(ctx)
}

func (relayedSources) GuardArgs(probe bool, args []string) []string { return restrict(args, probe) }

func (relayedSources) Redact(s string) string { return RedactRelayURLs(s) }

// released is the error for a relay URL whose source is no longer open.
func released() error {
	return &fs.PathError{Op: opRead, Path: "source (released)", Err: ErrClosed}
}

package fspath

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// The functions below are the os, path/filepath, and os/exec calls Silo makes
// on media paths. Each takes encoded paths, decodes them for the system call,
// and encodes any names it returns.

// Open is os.Open for an encoded path.
func Open(name string) (*os.File, error) { return os.Open(Decode(name)) }

// OpenFile is os.OpenFile for an encoded path.
func OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(Decode(name), flag, perm)
}

// Stat is os.Stat for an encoded path. The returned FileInfo reports the
// encoded base name.
func Stat(name string) (os.FileInfo, error) {
	info, err := osStat(Decode(name))
	return encodedInfo(info), err
}

// osStat is os.Stat; tests swap it to simulate a stat that never returns.
var osStat = os.Stat

// statCall is one in-flight stat that StatContext callers for a path share.
type statCall struct {
	done chan struct{}
	info os.FileInfo
	err  error
}

var (
	statCallsMu sync.Mutex
	statCalls   = map[string]*statCall{}
)

// StatContext is Stat that returns ctx.Err() once ctx ends, even while the
// stat system call is still blocked, as it can be on a stalled network or
// FUSE mount. The call keeps running until the kernel returns; concurrent
// callers for the same path share it, so a wedged path holds one goroutine
// however many requests give up on it.
func StatContext(ctx context.Context, name string) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	statCallsMu.Lock()
	call, ok := statCalls[name]
	if !ok {
		call = &statCall{done: make(chan struct{})}
		statCalls[name] = call
		go func() {
			call.info, call.err = Stat(name)
			statCallsMu.Lock()
			delete(statCalls, name)
			statCallsMu.Unlock()
			close(call.done)
		}()
	}
	statCallsMu.Unlock()
	select {
	case <-call.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return call.info, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Lstat is os.Lstat for an encoded path. The returned FileInfo reports the
// encoded base name.
func Lstat(name string) (os.FileInfo, error) {
	info, err := os.Lstat(Decode(name))
	return encodedInfo(info), err
}

// ReadFile is os.ReadFile for an encoded path.
func ReadFile(name string) ([]byte, error) { return os.ReadFile(Decode(name)) }

// ReadDir is os.ReadDir for an encoded directory. Entry names are encoded, so
// filepath.Join(name, entry.Name()) is an encoded path.
func ReadDir(name string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(Decode(name))
	for i, entry := range entries {
		if raw := entry.Name(); Encode(raw) != raw {
			entries[i] = encodedDirEntry{entry}
		}
	}
	return entries, err
}

// EvalSymlinks is filepath.EvalSymlinks for an encoded path and returns the
// encoded result.
func EvalSymlinks(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(Decode(path))
	if err != nil {
		return "", err
	}
	return Encode(resolved), nil
}

// Readlink is os.Readlink for an encoded path and returns the encoded link
// text.
func Readlink(name string) (string, error) {
	target, err := os.Readlink(Decode(name))
	if err != nil {
		return "", err
	}
	return Encode(target), nil
}

// CommandContext is exec.CommandContext with every argument decoded, so an
// encoded media path reaches ffmpeg or ffprobe as the real file name, also
// when it is embedded in a larger argument such as a filter graph. Arguments
// without reserved code points pass through unchanged.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, DecodeAll(args)...) //nolint:gosec // same contract as exec.CommandContext; callers pass configured tool binaries and separate arguments, no shell
}

// DecodeAll decodes each string in args. It returns args itself when no
// string needed decoding.
func DecodeAll(args []string) []string {
	var out []string
	for i, arg := range args {
		decoded := Decode(arg)
		if out == nil {
			if decoded == arg {
				continue
			}
			out = append(make([]string, 0, len(args)), args[:i]...)
		}
		out = append(out, decoded)
	}
	if out == nil {
		return args
	}
	return out
}

// SameFile is os.SameFile for FileInfo values that may come from this
// package. os.SameFile only compares its own FileInfo type, so it reports
// false for the wrapper Stat, Lstat, and ReadDir return for an encoded name.
func SameFile(a, b os.FileInfo) bool { return os.SameFile(rawInfo(a), rawInfo(b)) }

func rawInfo(info os.FileInfo) os.FileInfo {
	if encoded, ok := info.(encodedFileInfo); ok {
		return encoded.FileInfo
	}
	return info
}

func encodedInfo(info os.FileInfo) os.FileInfo {
	if info == nil {
		return nil
	}
	if raw := info.Name(); Encode(raw) != raw {
		return encodedFileInfo{info}
	}
	return info
}

type encodedFileInfo struct{ fs.FileInfo }

func (i encodedFileInfo) Name() string { return Encode(i.FileInfo.Name()) }

type encodedDirEntry struct{ fs.DirEntry }

func (e encodedDirEntry) Name() string { return Encode(e.DirEntry.Name()) }

func (e encodedDirEntry) Info() (fs.FileInfo, error) {
	info, err := e.DirEntry.Info()
	return encodedInfo(info), err
}

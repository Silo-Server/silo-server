package keptsub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Kept streams are copied to the node's local disk, named by their SHA-256,
// so reading one again, or cutting a window from it, costs no storage
// request. A copy never goes stale: its name is its content. The copies are
// held to a byte budget, evicting the least recently used.

const (
	defaultMirrorMaxBytes = 1 << 30
	mirrorTempPrefix      = ".kept-"
	// staleMirrorTempAge is when a temp file a crash left is removed.
	staleMirrorTempAge = time.Hour
)

var errNoMirrorDir = errors.New("keptsub: no local directory for kept tracks")

// mirrorFetch is one copy being fetched, shared by its concurrent callers.
type mirrorFetch struct {
	done chan struct{}
	path string
	err  error
}

func (s *Store) mirrorDir() string {
	if s.opts.MirrorDir == nil {
		return ""
	}
	return s.opts.MirrorDir()
}

func mirrorName(t *Track) string { return t.SHA256 + streamExt(t.StreamFormat) }

// mirror returns the local copy of t's stream, fetching it from storage
// when the node has none. The fetched bytes must hash to t's SHA-256.
func (s *Store) mirror(ctx context.Context, t *Track) (string, error) {
	dir := s.mirrorDir()
	if dir == "" {
		return "", errNoMirrorDir
	}
	path := filepath.Join(dir, mirrorName(t))
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now) // recency for eviction; best effort
		return path, nil
	}
	s.mu.Lock()
	f := s.mirrors[path]
	if f == nil {
		f = &mirrorFetch{done: make(chan struct{})}
		s.mirrors[path] = f
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.mirrors, path)
				s.mu.Unlock()
				close(f.done)
			}()
			fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			f.path, f.err = s.fetchMirror(fetchCtx, dir, path, t)
		}()
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.path, f.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *Store) fetchMirror(ctx context.Context, dir, path string, t *Track) (string, error) {
	r, _, err := s.blobs.Get(ctx, t.BlobKey)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	return s.writeMirror(dir, path, t, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// mirrorData copies data, t's stream just kept, locally. A failure only
// costs a fetch later.
func (s *Store) mirrorData(t *Track, data []byte) {
	dir := s.mirrorDir()
	if dir == "" {
		return
	}
	if _, err := s.writeMirror(dir, filepath.Join(dir, mirrorName(t)), t, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}); err != nil {
		slog.Warn("local copy of a kept subtitle track failed", "component", "keptsub", "key", t.BlobKey, "error", err)
	}
}

// writeMirror writes a copy through a temp file, checks its hash, and
// renames it into place.
func (s *Store) writeMirror(dir, path string, t *Track, write func(io.Writer) error) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, mirrorTempPrefix+"*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	err = write(io.MultiWriter(tmp, h))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != t.SHA256 {
		return "", fmt.Errorf("keptsub: %s holds bytes with SHA-256 %s, want %s", t.BlobKey, got, t.SHA256)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	s.evictMirrors(dir)
	return path, nil
}

// evictMirrors removes the least recently used copies over the budget, and
// temp files a crash left.
func (s *Store) evictMirrors(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type copyFile struct {
		path  string
		size  int64
		mtime time.Time
	}
	var copies []copyFile
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), mirrorTempPrefix) {
			if time.Since(info.ModTime()) > staleMirrorTempAge {
				_ = os.Remove(path)
			}
			continue
		}
		copies = append(copies, copyFile{path: path, size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
	}
	slices.SortFunc(copies, func(a, b copyFile) int { return a.mtime.Compare(b.mtime) })
	for _, c := range copies {
		if total <= s.opts.MirrorMaxBytes {
			return
		}
		if err := os.Remove(c.path); err == nil || os.IsNotExist(err) {
			total -= c.size
		}
	}
}

package keptsub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Kept streams are copied to the node's local disk, named by their SHA-256,
// so reading one again, or cutting a window from it, costs no storage
// request. A copy never goes stale: its name is its content. Its file is
// never touched after it is written, so the index cache, keyed by path,
// size and modification time, keeps finding it; recency for eviction is kept
// in memory, and the copies are held to a byte budget, least recently used
// first, with copies this process hasn't used ordered by when they were
// written.

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

// used records that the copy at path was just used.
func (s *Store) used(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mirrorUsed) >= startTimesKept {
		clear(s.mirrorUsed)
	}
	s.mirrorUsed[path] = time.Now()
}

// mirror returns the local copy of t's stream, fetching it from storage
// when the node has none. The fetched bytes must hash to t's SHA-256.
func (s *Store) mirror(ctx context.Context, t *Track) (local string, release func(), err error) {
	dir := s.mirrorDir()
	if dir == "" {
		return "", nil, errNoMirrorDir
	}
	path := filepath.Join(dir, mirrorName(t))
	unpin := s.pinMirror(path)
	defer func() {
		if err != nil {
			unpin()
		}
	}()
	if _, err := os.Stat(path); err == nil {
		s.used(path)
		return path, unpin, nil
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
			f.path, f.err = s.fetchMirror(fetchCtx, dir, t)
		}()
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.path, unpin, f.err
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

func (s *Store) fetchMirror(ctx context.Context, dir string, t *Track) (string, error) {
	r, _, err := s.blobs.Get(ctx, t.BlobKey)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	path, sum, _, release, err := s.writeMirror(dir, streamExt(t.StreamFormat), func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
	if err != nil {
		return "", err
	}
	defer release()
	if sum != t.SHA256 {
		_ = os.Remove(path)
		return "", fmt.Errorf("keptsub: %s holds bytes with SHA-256 %s, want %s", t.BlobKey, sum, t.SHA256)
	}
	return path, nil
}

// writeMirror writes a copy through a temp file, hashing it, and renames it
// to its hash with ext. The caller releases its eviction pin after consuming it.
func (s *Store) writeMirror(dir, ext string, write func(io.Writer) error) (path, sum string, size int64, release func(), err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", 0, nil, err
	}
	tmp, err := os.CreateTemp(dir, mirrorTempPrefix+"*")
	if err != nil {
		return "", "", 0, nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	counter := &byteCounter{}
	err = write(io.MultiWriter(tmp, h, counter))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", 0, nil, err
	}
	sum = hex.EncodeToString(h.Sum(nil))
	path = filepath.Join(dir, sum+ext)
	release = s.pinMirror(path)
	if err := os.Rename(tmp.Name(), path); err != nil {
		release()
		return "", "", 0, nil, err
	}
	s.used(path)
	s.evictMirrors(dir)
	return path, sum, counter.n, release, nil
}

// pinMirror excludes path from eviction until the caller has opened or read it.
func (s *Store) pinMirror(path string) func() {
	s.mu.Lock()
	s.mirrorPins[path]++
	s.mu.Unlock()
	return func() {
		dir := filepath.Dir(path)
		s.mu.Lock()
		var evict bool
		if s.mirrorPins[path]--; s.mirrorPins[path] == 0 {
			delete(s.mirrorPins, path)
			evict = s.mirrorOverBudget[dir]
		}
		s.mu.Unlock()
		if evict {
			s.evictMirrors(dir)
		}
	}
}

type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// evictMirrors removes the least recently used copies over the budget, and
// temp files a crash left. Writes scan the directory; releases scan only
// while pinned copies have left it over budget. The lock keeps the final
// over-budget flag and a concurrent final release from missing each other.
func (s *Store) evictMirrors(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type copyFile struct {
		path string
		size int64
		used time.Time
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
		used := info.ModTime()
		if at, ok := s.mirrorUsed[path]; ok && at.After(used) {
			used = at
		}
		copies = append(copies, copyFile{path: path, size: info.Size(), used: used})
		total += info.Size()
	}
	slices.SortFunc(copies, func(a, b copyFile) int { return a.used.Compare(b.used) })
	for _, c := range copies {
		if total <= s.opts.MirrorMaxBytes {
			break
		}
		if s.mirrorPins[c.path] > 0 {
			continue
		}
		if err := os.Remove(c.path); err == nil || os.IsNotExist(err) {
			total -= c.size
			delete(s.mirrorUsed, c.path)
		}
	}
	if total > s.opts.MirrorMaxBytes {
		s.mirrorOverBudget[dir] = true
	} else {
		delete(s.mirrorOverBudget, dir)
	}
}

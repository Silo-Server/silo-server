package keptsub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Kept streams are copied to the node's local disk, named by their SHA-256,
// so reading one again, or cutting a window from it, costs no storage
// request. A copy never goes stale: its name is its content. Its file is
// never touched after it is written, so the index cache, keyed by path,
// size and modification time, keeps finding it; recency for eviction is kept
// in memory, and the copies are held to a byte budget, least recently used
// first, with copies this process hasn't used ordered by when they were
// written. A copy being written holds its size of the budget from before
// its first byte until it is renamed in, so eviction makes room for it
// ahead and a burst of fetches can't run the directory past the budget.

const (
	defaultMirrorMaxBytes = 1 << 30
	mirrorTempPrefix      = ".kept-"
	// staleMirrorTempAge is when a temp file a crash left is removed.
	staleMirrorTempAge = time.Hour
	// Cold fetches reserve their recorded output size before starting. These
	// process-wide limits include workers whose callers have disconnected.
	maxMirrorFetches    = 8
	maxMirrorFetchBytes = 256 << 20
)

var errNoMirrorDir = errors.New("keptsub: no local directory for kept tracks")

// mirrorReservation is room in the copies' budget held for a copy being
// written, until it is renamed in, when it counts as a copy, or abandoned.
type mirrorReservation struct {
	s    *Store
	n    int64
	once sync.Once
}

// reserveMirror holds n bytes of dir's budget for a copy this node read
// itself, which is written whatever the budget says, and makes room for it.
func (s *Store) reserveMirror(dir string, n int64) *mirrorReservation {
	s.mu.Lock()
	s.mirrorReserved += n
	s.mu.Unlock()
	s.evictMirrors(dir)
	return &mirrorReservation{s: s, n: n}
}

// release returns the room; it may be called more than once, or on nil.
func (r *mirrorReservation) release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.s.mu.Lock()
		r.s.mirrorReserved -= r.n
		r.s.mu.Unlock()
	})
}

// errMirrorFetchLimit means a copy can't be fetched now: the process's
// fetch limits are reached, or copies being written hold the copies' whole
// budget. A whole track is then read from storage instead.
var errMirrorFetchLimit = fmt.Errorf("keptsub: too many mirror downloads: %w", playback.ErrSubtitleReadsBusy)

var mirrorFetchBudget struct {
	mu     sync.Mutex
	active int
	bytes  int64
}

func reserveMirrorFetch(size int64) (func(), error) {
	if size <= 0 || size > maxMirrorFetchBytes {
		return nil, fmt.Errorf("keptsub: invalid mirror size %d", size)
	}
	mirrorFetchBudget.mu.Lock()
	defer mirrorFetchBudget.mu.Unlock()
	if mirrorFetchBudget.active >= maxMirrorFetches || size > maxMirrorFetchBytes-mirrorFetchBudget.bytes {
		return nil, errMirrorFetchLimit
	}
	mirrorFetchBudget.active++
	mirrorFetchBudget.bytes += size
	return func() {
		mirrorFetchBudget.mu.Lock()
		defer mirrorFetchBudget.mu.Unlock()
		mirrorFetchBudget.active--
		mirrorFetchBudget.bytes -= size
	}, nil
}

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
// when the node has none. The fetched bytes must hash to t's SHA-256. A
// fetch the node has no room for now returns errMirrorFetchLimit.
func (s *Store) mirror(ctx context.Context, t *Track) (local string, release func(), err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
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
		// Copies being written may not hold more than the copies' budget.
		if s.mirrorReserved+t.Bytes > s.opts.MirrorMaxBytes {
			s.mu.Unlock()
			return "", nil, errMirrorFetchLimit
		}
		finish, err := reserveMirrorFetch(t.Bytes)
		if err != nil {
			s.mu.Unlock()
			return "", nil, err
		}
		s.mirrorReserved += t.Bytes
		res := &mirrorReservation{s: s, n: t.Bytes}
		f = &mirrorFetch{done: make(chan struct{})}
		s.mirrors[path] = f
		go func() {
			defer func() {
				res.release()
				s.mu.Lock()
				delete(s.mirrors, path)
				s.mu.Unlock()
				finish()
				close(f.done)
			}()
			// Room for the copy is made before any of it is fetched.
			s.evictMirrors(dir)
			fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			f.path, f.err = s.fetchMirror(fetchCtx, dir, t, res)
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

func (s *Store) fetchMirror(ctx context.Context, dir string, t *Track, res *mirrorReservation) (string, error) {
	r, _, err := s.blobs.Get(ctx, t.BlobKey)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	path, sum, _, release, err := s.writeMirror(dir, streamExt(t.StreamFormat), res, func(w io.Writer) error {
		_, err := io.CopyN(w, r, t.Bytes)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("keptsub: %s holds fewer than %d bytes", t.BlobKey, t.Bytes)
		}
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
// to its hash with ext; res is the room it holds until then. The caller
// releases its eviction pin after consuming it.
func (s *Store) writeMirror(dir, ext string, res *mirrorReservation, write func(io.Writer) error) (path, sum string, size int64, release func(), err error) {
	defer res.release()
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
	path, release, err = s.placeMirror(tmp.Name(), dir, sum, ext, res)
	if err != nil {
		return "", "", 0, nil, err
	}
	return path, sum, counter.n, release, nil
}

// adoptMirror makes the finished size-byte file f a copy: it hashes the
// file where it is and moves it into dir, so a copy written beside the
// copies isn't written twice. A file on another file system is copied. A
// file that no longer holds size bytes is refused.
func (s *Store) adoptMirror(dir, ext string, f *os.File, size int64) (path, sum string, release func(), err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", nil, err
	}
	h := sha256.New()
	if n, err := io.Copy(h, io.LimitReader(f, size+1)); err != nil {
		return "", "", nil, err
	} else if n != size {
		return "", "", nil, fmt.Errorf("keptsub: %s is %d bytes, want %d", f.Name(), n, size)
	}
	sum = hex.EncodeToString(h.Sum(nil))
	if path, release, err := s.placeMirror(f.Name(), dir, sum, ext, nil); err == nil {
		return path, sum, release, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", nil, err
	}
	path, sum, _, release, err = s.writeMirror(dir, ext, s.reserveMirror(dir, size), func(w io.Writer) error {
		_, err := io.CopyN(w, f, size)
		return err
	})
	return path, sum, release, err
}

// placeMirror moves the finished file at src to dir's copy named sum+ext,
// pinned until the caller releases it. Once there it counts as a copy, so
// res, the room it held, is returned.
func (s *Store) placeMirror(src, dir, sum, ext string, res *mirrorReservation) (string, func(), error) {
	path := filepath.Join(dir, sum+ext)
	release := s.pinMirror(path)
	if err := os.Rename(src, path); err != nil {
		release()
		return "", nil, err
	}
	res.release()
	s.used(path)
	s.evictMirrors(dir)
	return path, release, nil
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

// evictMirrors removes the least recently used copies over the budget,
// counting the room copies being written hold, and temp files a crash left.
// Writes scan the directory; releases scan only while pinned copies have
// left it over budget. The lock keeps the final over-budget flag and a
// concurrent final release from missing each other.
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
	total := s.mirrorReserved
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

// openObject reads t's stream from storage, for when the node has no room
// to copy it. Reading fails past t.Bytes, and at the end when the bytes
// don't hash to t's SHA-256.
func (s *Store) openObject(ctx context.Context, t *Track) (io.ReadCloser, error) {
	r, _, err := s.blobs.Get(ctx, t.BlobKey)
	if err != nil {
		return nil, err
	}
	return &checkedObject{r: r, t: t, h: sha256.New()}, nil
}

type checkedObject struct {
	r io.ReadCloser
	t *Track
	h hash.Hash
	n int64
}

func (o *checkedObject) Read(p []byte) (int, error) {
	// One byte past the size shows an object that is too long.
	if left := o.t.Bytes + 1 - o.n; int64(len(p)) > left {
		p = p[:left]
	}
	n, err := o.r.Read(p)
	o.h.Write(p[:n])
	o.n += int64(n)
	switch {
	case o.n > o.t.Bytes:
		return n, fmt.Errorf("keptsub: %s holds more than %d bytes", o.t.BlobKey, o.t.Bytes)
	case errors.Is(err, io.EOF) && o.n < o.t.Bytes:
		return n, fmt.Errorf("keptsub: %s holds fewer than %d bytes", o.t.BlobKey, o.t.Bytes)
	case errors.Is(err, io.EOF):
		if sum := hex.EncodeToString(o.h.Sum(nil)); sum != o.t.SHA256 {
			return n, fmt.Errorf("keptsub: %s holds bytes with SHA-256 %s, want %s", o.t.BlobKey, sum, o.t.SHA256)
		}
	}
	return n, err
}

func (o *checkedObject) Close() error { return o.r.Close() }

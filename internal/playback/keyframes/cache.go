package keyframes

import (
	"container/list"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
)

// maxCachedFiles bounds the process-wide result cache. An index is a few
// kilobytes to a few hundred, one float per keyframe.
const maxCachedFiles = 256

type cacheKey struct {
	path    string
	size    int64
	modTime time.Time
}

type cacheEntry struct {
	key   cacheKey
	index Index
	err   error
}

// maxRejectedFiles bounds the rejections kept. A file is rejected only when a
// playback finds its Cues wrong, which mainstream muxers don't produce.
const maxRejectedFiles = 4096

// indexCache holds read results, including files without a usable index, so
// a later playback, a rebuilt session or a second viewer doesn't read the
// file again. An entry is keyed by the file's size and modification time, so
// a replaced file is read afresh. Rejections (Reject) are kept apart from the
// results, so evicting results never forgets one.
var indexCache = struct {
	sync.Mutex
	order    *list.List
	entries  map[cacheKey]*list.Element
	rejected map[cacheKey]struct{}
}{order: list.New(), entries: map[cacheKey]*list.Element{}, rejected: map[cacheKey]struct{}{}}

// Load returns the keyframe index of the media file at path (ReadMatroska).
// It reads only the file's metadata and a few clusters' block headers, so a
// playback can plan from it on its first start, and it reads nothing in the
// background (#2453). A file without a usable index, including any that isn't
// Matroska or WebM or is malformed, gets ErrNoIndex, as does one Reject
// marked.
func Load(path string) (Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return Index{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Index{}, err
	}
	key := cacheKey{path: path, size: info.Size(), modTime: info.ModTime()}
	if idx, err, ok := cached(key); ok {
		return idx, err
	}

	idx, err := ReadMatroska(f, info.Size())
	if err != nil && transient(err) {
		return Index{}, err
	}
	idx, err = settle(idx, err)
	return storeFirst(key, idx, err)
}

// Reject records that the file at path has an index FFmpeg doesn't cut at:
// its Cues leave out keyframes the checks didn't read. Until the file
// changes, Load reports ErrIncompleteIndex for it, so later playbacks keep
// FFmpeg's playlist. The cache is in memory, so a restart reads the file
// again.
func Reject(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	key := cacheKey{path: path, size: info.Size(), modTime: info.ModTime()}
	indexCache.Lock()
	defer indexCache.Unlock()
	if len(indexCache.rejected) >= maxRejectedFiles {
		// Forget an arbitrary one: its file is read and checked again.
		for k := range indexCache.rejected {
			delete(indexCache.rejected, k)
			break
		}
	}
	indexCache.rejected[key] = struct{}{}
	return nil
}

// transient reports an error reading the file, which says nothing lasting
// about it: a later playback tries again. Anything else describes the file.
func transient(err error) bool {
	return errors.As(err, new(*fs.PathError))
}

// settle turns a lasting failure into ErrNoIndex: the file isn't Matroska, is
// malformed, or has no complete index.
func settle(idx Index, err error) (Index, error) {
	if err == nil || errors.Is(err, ErrNoIndex) {
		return idx, err
	}
	return Index{}, fmt.Errorf("%w: %w", ErrNoIndex, err)
}

func cached(key cacheKey) (Index, error, bool) {
	indexCache.Lock()
	defer indexCache.Unlock()
	if _, ok := indexCache.rejected[key]; ok {
		return Index{}, ErrIncompleteIndex, true
	}
	el, ok := indexCache.entries[key]
	if !ok {
		return Index{}, nil, false
	}
	indexCache.order.MoveToFront(el)
	entry, _ := el.Value.(*cacheEntry)
	return entry.index, entry.err, true
}

// storeFirst stores a read result unless the cache already has one for key,
// and returns the result the cache holds. A Reject that landed while this
// read ran isn't undone.
func storeFirst(key cacheKey, idx Index, err error) (Index, error) {
	indexCache.Lock()
	defer indexCache.Unlock()
	if _, ok := indexCache.rejected[key]; ok {
		return Index{}, ErrIncompleteIndex
	}
	if el, ok := indexCache.entries[key]; ok {
		indexCache.order.MoveToFront(el)
		entry, _ := el.Value.(*cacheEntry)
		return entry.index, entry.err
	}
	storeLocked(key, idx, err)
	return idx, err
}

func storeLocked(key cacheKey, idx Index, err error) {
	if el, ok := indexCache.entries[key]; ok {
		entry, _ := el.Value.(*cacheEntry)
		entry.index, entry.err = idx, err
		indexCache.order.MoveToFront(el)
		return
	}
	indexCache.entries[key] = indexCache.order.PushFront(&cacheEntry{key: key, index: idx, err: err})
	for indexCache.order.Len() > maxCachedFiles {
		oldest := indexCache.order.Back()
		indexCache.order.Remove(oldest)
		if entry, ok := oldest.Value.(*cacheEntry); ok {
			delete(indexCache.entries, entry.key)
		}
	}
}

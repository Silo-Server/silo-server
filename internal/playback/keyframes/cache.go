package keyframes

import (
	"container/list"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ErrUnverified means the file's index passed the quick check and is being
// checked against every cluster in the background; until that finishes it
// isn't used. It is reported as ErrNoIndex.
var ErrUnverified = fmt.Errorf("%w: keyframe index not verified yet", ErrNoIndex)

// maxCachedFiles bounds the process-wide index cache. An index is a few
// kilobytes to a few hundred, one float per keyframe.
const maxCachedFiles = 256

type cacheKey struct {
	path    string
	size    int64
	modTime time.Time
}

type cacheEntry struct {
	key     cacheKey
	index   Index
	err     error
	pending bool // the full check is running
}

// indexCache holds verified results, including files without a usable
// index, so a later playback or a second viewer doesn't read the file again.
// An entry is keyed by the file's size and modification time, so a replaced
// file is read afresh.
var indexCache = struct {
	sync.Mutex
	order   *list.List
	entries map[cacheKey]*list.Element
}{order: list.New(), entries: map[cacheKey]*list.Element{}}

// verifySlots runs one full check at a time: each reads every block header
// of a file.
var verifySlots = make(chan struct{}, 1)

// Load returns the keyframe index of the media file at path once it has been
// checked against every cluster. The first call for a file runs the quick
// check and, if it passes, starts the full check in the background and
// returns ErrUnverified; calls after it finishes get the result from the
// cache. A file without a usable index, including any that isn't Matroska or
// WebM, gets ErrNoIndex.
func Load(path string) (Index, error) {
	return load(path, false)
}

// LoadVerified is Load that runs the full check itself instead of in the
// background.
func LoadVerified(path string) (Index, error) {
	return load(path, true)
}

func load(path string, wait bool) (Index, error) {
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

	if entry, ok := cached(key); ok {
		if !entry.pending {
			return entry.index, entry.err
		}
		if !wait {
			return Index{}, ErrUnverified
		}
	}

	if wait {
		idx, err := stable(VerifyMatroska(f, info.Size()))
		if err != nil && !errors.Is(err, ErrNoIndex) {
			return Index{}, err
		}
		store(key, idx, err, false)
		return idx, err
	}

	idx, err := stable(ReadMatroska(f, info.Size()))
	if err != nil {
		if !errors.Is(err, ErrNoIndex) {
			// A read failure says nothing lasting about the file.
			return Index{}, err
		}
		store(key, idx, err, false)
		return idx, err
	}
	if !store(key, Index{}, nil, true) {
		// Another caller started the check meanwhile.
		return Index{}, ErrUnverified
	}
	go verifyInBackground(key)
	return Index{}, ErrUnverified
}

// stable maps a file that isn't Matroska to ErrNoIndex: a lasting fact about
// the file, worth caching like a missing index.
func stable(idx Index, err error) (Index, error) {
	if errors.As(err, new(notMatroskaError)) {
		return idx, ErrNoIndex
	}
	return idx, err
}

func verifyInBackground(key cacheKey) {
	verifySlots <- struct{}{}
	defer func() { <-verifySlots }()

	idx, err := func() (Index, error) {
		f, err := os.Open(key.path)
		if err != nil {
			return Index{}, err
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return Index{}, err
		}
		if info.Size() != key.size || !info.ModTime().Equal(key.modTime) {
			return Index{}, errors.New("file changed during verification")
		}
		return stable(VerifyMatroska(f, info.Size()))
	}()
	if err != nil && !errors.Is(err, ErrNoIndex) {
		slog.Warn("keyframe index: verify file", "component", "playback", "path", key.path, "error", err)
		forget(key) // a later playback tries again
		return
	}
	store(key, idx, err, false)
}

func cached(key cacheKey) (cacheEntry, bool) {
	indexCache.Lock()
	defer indexCache.Unlock()
	el, ok := indexCache.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	indexCache.order.MoveToFront(el)
	entry, _ := el.Value.(*cacheEntry)
	return *entry, true
}

// store records a result for key. A pending marker is only stored when no
// entry exists; it reports whether it stored.
func store(key cacheKey, idx Index, err error, pending bool) bool {
	indexCache.Lock()
	defer indexCache.Unlock()
	if el, ok := indexCache.entries[key]; ok {
		if pending {
			return false
		}
		entry, _ := el.Value.(*cacheEntry)
		entry.index, entry.err, entry.pending = idx, err, false
		indexCache.order.MoveToFront(el)
		return true
	}
	indexCache.entries[key] = indexCache.order.PushFront(&cacheEntry{key: key, index: idx, err: err, pending: pending})
	for indexCache.order.Len() > maxCachedFiles {
		oldest := indexCache.order.Back()
		indexCache.order.Remove(oldest)
		if entry, ok := oldest.Value.(*cacheEntry); ok {
			delete(indexCache.entries, entry.key)
		}
	}
	return true
}

func forget(key cacheKey) {
	indexCache.Lock()
	defer indexCache.Unlock()
	if el, ok := indexCache.entries[key]; ok {
		indexCache.order.Remove(el)
		delete(indexCache.entries, key)
	}
}

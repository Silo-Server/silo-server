package keyframes

import (
	"container/list"
	"errors"
	"os"
	"sync"
	"time"
)

// maxCachedFiles bounds the process-wide index cache. An index is a few
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

// indexCache holds recent results, including files without an index, so a
// session restart or a second viewer doesn't read the file again. An entry is
// keyed by the file's size and modification time, so a replaced file is read
// afresh.
var indexCache = struct {
	sync.Mutex
	order   *list.List
	entries map[cacheKey]*list.Element
}{order: list.New(), entries: map[cacheKey]*list.Element{}}

// Load returns the keyframe index of the media file at path, from the cache
// when the file hasn't changed. It returns ErrNoIndex for a file without a
// usable index, including any file that isn't Matroska or WebM.
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

	indexCache.Lock()
	if el, ok := indexCache.entries[key]; ok {
		indexCache.order.MoveToFront(el)
		entry, _ := el.Value.(*cacheEntry)
		indexCache.Unlock()
		return entry.index, entry.err
	}
	indexCache.Unlock()

	idx, err := ReadMatroska(f, info.Size())
	if err != nil && !errors.Is(err, ErrNoIndex) {
		if errors.As(err, new(notMatroskaError)) {
			err = ErrNoIndex
		} else {
			// A read failure says nothing lasting about the file.
			return Index{}, err
		}
	}

	indexCache.Lock()
	defer indexCache.Unlock()
	if el, ok := indexCache.entries[key]; ok {
		indexCache.order.MoveToFront(el)
	} else {
		indexCache.entries[key] = indexCache.order.PushFront(&cacheEntry{key: key, index: idx, err: err})
		for indexCache.order.Len() > maxCachedFiles {
			oldest := indexCache.order.Back()
			indexCache.order.Remove(oldest)
			if entry, ok := oldest.Value.(*cacheEntry); ok {
				delete(indexCache.entries, entry.key)
			}
		}
	}
	return idx, err
}

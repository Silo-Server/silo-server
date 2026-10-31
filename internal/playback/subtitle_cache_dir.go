package playback

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/privatedir"
)

// openDir opens the cache dir for one operation, or returns nil when caching
// is disabled or the dir is refused. It is checked on every call because the
// transcode dir can change at runtime. The cache dir and the transcode dir
// above it must each be a real directory owned by this process's user and
// not writable by its group or other users, so no other local account can
// plant entries in the cache dir or rename it aside. ffmpeg opens entries and
// temp files by path, which only that second check keeps inside the cache
// dir. Entries reached through the returned root cannot resolve outside it.
// The caller closes it.
func (c *SubtitleCache) openDir() *os.Root {
	dir := c.dir()
	if dir == "" {
		return nil
	}
	var root *os.Root
	err := privatedir.Ensure(filepath.Dir(dir))
	if err == nil {
		root, err = privatedir.OpenRoot(dir)
	}
	refusal := ""
	if err != nil {
		refusal = err.Error()
	}
	c.mu.Lock()
	repeated := refusal == c.refusal
	c.refusal = refusal
	c.mu.Unlock()
	if err != nil {
		if !repeated {
			slog.Warn("subtitle cache disabled: cache dir refused", "dir", dir, "error", err)
		}
		return nil
	}
	return root
}

// openEntry opens a committed entry through the cache dir's root, so a
// symlink cannot resolve outside it, and accepts only a regular file. A hit
// bumps the entry's mtime to record recency for LRU eviction.
func openEntry(root *os.Root, key string) (*os.File, bool) {
	f, err := root.Open(key)
	if err != nil {
		return nil, false
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, false
	}
	// Best-effort: a failure (e.g. read-only remount) only degrades eviction
	// ordering, not correctness.
	now := time.Now()
	if err := root.Chtimes(key, now, now); err != nil {
		slog.Debug("subtitle cache recency bump failed", "path", f.Name(), "error", err)
	}
	return f, true
}

// removeStaleSiblings deletes committed entries for the same source+track
// with a different mtime/size suffix — the source was replaced, so those can
// never be served again.
func (c *SubtitleCache) removeStaleSiblings(root *os.Root, inputPath string, trackIndex int, keepKey string) {
	prefix := subtitleCacheKeyPrefix(inputPath, trackIndex)
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keepKey || !strings.HasPrefix(name, prefix) || filepath.Ext(name) != filepath.Ext(keepKey) {
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("subtitle cache stale entry remove failed", "name", name, "error", err)
		}
	}
}

// evict is the scan-on-write LRU pass: when committed entries exceed the
// byte budget, the oldest-mtime entries are removed until the total fits.
// It also sweeps orphaned .part temp files older than stalePartMaxAge
// (crash leftovers). No background daemon — commits are rare enough that a
// directory scan per commit is cheap.
func (c *SubtitleCache) evict(root *os.Root) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return
	}
	type cacheEnt struct {
		name  string
		size  int64
		mtime time.Time
	}
	var (
		ents  []cacheEnt
		total int64
	)
	now := time.Now()
	for _, e := range entries {
		info, err := root.Lstat(e.Name())
		if err != nil {
			continue
		}
		if strings.Contains(e.Name(), ".part-") {
			if now.Sub(info.ModTime()) > stalePartMaxAge {
				_ = root.Remove(e.Name())
			}
			continue
		}
		if !slices.Contains([]string{SubtitleExtPGSV3, SubtitleExtVTTV3, SubtitleExtASSV3, SubtitleExtSRTV3}, filepath.Ext(e.Name())) {
			continue
		}
		ents = append(ents, cacheEnt{name: e.Name(), size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
	}
	if total <= c.maxBytes {
		return
	}
	slices.SortFunc(ents, func(a, b cacheEnt) int { return a.mtime.Compare(b.mtime) })
	for _, e := range ents {
		if total <= c.maxBytes {
			break
		}
		path := filepath.Join(root.Name(), e.name)
		if err := root.Remove(e.name); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("subtitle cache eviction remove failed", "path", path, "error", err)
			}
			continue
		}
		slog.Info("evicted cached subtitle track (LRU)", "path", path, "bytes", e.size)
		total -= e.size
	}
}
